# TrialTerminator

[![CI](https://github.com/weezy2005/trial-terminator/actions/workflows/ci.yml/badge.svg)](https://github.com/weezy2005/trial-terminator/actions/workflows/ci.yml)

A distributed task system that cancels subscriptions for you. You submit a cancellation, a Go API records it in a Postgres state machine, Redis hands it to a pool of browser workers, and a Gemini vision agent clicks through the site's cancellation flow while you watch live screenshots. Failures retry, then land in a dead-letter queue with screenshot evidence.

The cancelling is the demo. The interesting part is everything that keeps it correct when workers crash, requests get retried, and agent runs take minutes.

## Architecture

```mermaid
flowchart LR
    UI[Next.js dashboard] -->|POST /tasks<br/>idempotency key| API[Go API]
    UI -->|poll status + live screenshot| API
    API -->|INSERT … ON CONFLICT| PG[(Postgres<br/>source of truth)]
    API -->|LPUSH task id| R[(Redis list)]
    R -->|BRPOP| W1[Worker]
    R -->|BRPOP| W2[Worker]
    W1 & W2 -->|claim · heartbeat · fenced result| PG
    W1 & W2 -->|screenshot → action| G[Gemini vision]
    W1 & W2 -->|drive| B[Target site]
    subgraph API background goroutines
      RQ[Requeuer · every 10s] 
      SC[Scheduler · every 1m]
    end
    RQ -->|stale IN_PROGRESS → PENDING| PG
    RQ -->|re-push| R
    SC -->|due scheduled tasks| R
    API -->|/metrics| P[Prometheus] --> GF[Grafana]
```

| Component | Tech | Responsibility |
|---|---|---|
| API | Go 1.22, `net/http`, pgx, go-redis | Accepts tasks idempotently, runs the requeuer and scheduler, serves metrics |
| Worker | Node 20, Playwright, Gemini | Claims tasks, runs the agent, heartbeats, writes results |
| Queue | Redis list | Wake-up notifications only. Carries task IDs, never task state |
| Store | Postgres 16 | Every task's state, lock owner, and heartbeat |
| Frontend | Next.js 14, Tailwind | Create and schedule tasks, Watch Mode for live agent screenshots |
| Observability | Prometheus, Grafana | Task rate, completions by status, DLQ size, p50/p99 latency |

## Task lifecycle

```mermaid
stateDiagram-v2
    [*] --> PENDING: POST /tasks
    PENDING --> IN_PROGRESS: worker claims (attempts++)
    IN_PROGRESS --> SUCCESS
    IN_PROGRESS --> RETRY: failed, attempts < max
    RETRY --> IN_PROGRESS: re-claimed
    IN_PROGRESS --> DEAD_LETTER: failed, attempts = max (+ screenshot)
    IN_PROGRESS --> PENDING: heartbeat lost for 30s (requeuer)
    IN_PROGRESS --> PENDING: trial end date detected (scheduled_for set)
```

## Design decisions

### Postgres is the source of truth and Redis only says "go look"
The queue carries task IDs, not payloads. A worker always reads the current row before acting, so Redis can lose or duplicate messages without corrupting anything. A lost message gets re-pushed by the requeuer. A duplicate loses the claim race and is dropped. This is why a plain Redis list is enough and Streams with consumer groups weren't needed.

### Idempotent creates in one statement
`POST /tasks` requires a client-generated `idempotency_key`. The insert is `INSERT … ON CONFLICT (idempotency_key) DO NOTHING RETURNING …`. If no row comes back, the key already existed, so the API fetches and returns the original task with `200` instead of `201`. The database's unique index makes the final call, so 20 concurrent requests with one key produce exactly one row (tested in `task_repo_integration_test.go`).

The repository reports *whether* it inserted rather than letting the caller infer it. The handler originally compared `created_at == updated_at`. That is also true for a replay of a task no worker has touched yet, so a fast client retry re-enqueued the task and double-counted it in metrics. The integration and handler tests both pin this.

### Claiming is a conditional UPDATE, not a lock service
```sql
UPDATE tasks SET status='IN_PROGRESS', locked_by=$worker, locked_at=NOW(), attempts=attempts+1
WHERE id=$1 AND status IN ('PENDING','RETRY') RETURNING *;
```
Postgres row locking serializes concurrent updates to the same row. Exactly one worker sees a row come back. The others get nothing and move on. Attempts are counted at claim time, so a worker that crashes mid-run still uses up an attempt.

### Heartbeats, not timeouts, detect dead workers
A Gemini agent run can take minutes, so "IN_PROGRESS for more than N minutes" can't distinguish a slow worker from a dead one. Workers refresh `locked_at` every 10s while running. The requeuer reclaims a task only after 30s of silence (three missed beats), and it re-checks staleness inside the `UPDATE` so a heartbeat that lands between the scan and the reset keeps the task with its live worker.

### Fencing so a "dead" worker can't overwrite the new owner
If a worker stalls (GC pause, network partition) long enough to be declared dead, its task goes to someone else. When the original worker wakes up and reports a result, every result write includes `WHERE locked_by = $me`. The stale write matches zero rows and is discarded. Without this, the slower worker's late result could overwrite the newer worker's outcome.

### Dead-letter queue with evidence
After `max_attempts` failures the task moves to `DEAD_LETTER` with the error and a screenshot of what the agent saw. Automation failures are usually UI changes or human-verification walls, and a screenshot explains them faster than logs. The agent detects CAPTCHA and 2FA walls and fails fast instead of burning its retries.

### Scheduling in Postgres, not cron
Tasks can carry `scheduled_for` (set by the user or auto-detected by the agent reading the trial end date). A scheduler goroutine polls a partial index once a minute and enqueues due tasks. Keeping the schedule in the same table as the state means there's no second system that can disagree with the database.

## Running locally

Requires Docker and a [Gemini API key](https://aistudio.google.com/apikey).

```bash
cp .env.example .env              # add GEMINI_API_KEY=...
docker compose up --build
```

| Service | URL |
|---|---|
| Dashboard | http://localhost:3000 |
| API | http://localhost:8081 |
| Mock subscription site (safe demo target) | http://localhost:3002 |
| Grafana | http://localhost:3001 |
| Prometheus | http://localhost:9090 |

To see the agent work end to end, pick **AI agent** in the dashboard, set the URL to `http://mock-subscription:3002` (the worker reaches it over the Docker network), and open Watch Mode on the task.

## Tests

```bash
# Go unit tests: handlers, requeuer, scheduler (no external services)
go test ./...

# Go integration tests: idempotency, claim races, requeue/heartbeat SQL
# against a real Postgres. Each test gets its own schema, so they run in parallel.
TEST_DATABASE_URL=postgres://user:pass@localhost:5432/db?sslmode=disable \
  go test -tags=integration ./internal/repository/...

# End-to-end against the running compose stack
export API_URL=http://localhost:8081
node tests/e2e/happy-path.mjs     # create → claim → SUCCESS, plus idempotent replay
node tests/e2e/failure-path.mjs   # CAPTCHA wall → 3 attempts → DEAD_LETTER with screenshot
```

CI runs gofmt, vet, unit tests with `-race`, integration tests against a Postgres service container, a frontend typecheck and build, and a build of every Docker image.

## API

| Method | Path | Description |
|---|---|---|
| `POST` | `/tasks` | Create a task. Body: `idempotency_key` (UUID), `service_name`, `user_email`, optional `payload`, `scheduled_for`. Returns `201` if new, `200` for a replay |
| `GET` | `/tasks/{id}` | Current task state |
| `GET` | `/tasks/{id}/screenshot` | Latest agent frame (Watch Mode) |
| `GET` | `/metrics` | Prometheus scrape endpoint |
| `GET` | `/health` | Liveness |

## Project layout

```
cmd/api/              API entrypoint, graceful shutdown
internal/handlers/    HTTP layer (thin: parse, validate, call repo, respond)
internal/repository/  All SQL lives here
internal/queue/       Redis producer, requeuer, scheduler
internal/metrics/     Every Prometheus metric in one place
migrations/           Schema, applied by Postgres on first start
worker/               Node worker and Gemini vision agent
mock-subscription/    Fake SaaS with a retention dark pattern, for safe demos
frontend/             Next.js dashboard
grafana/ prometheus/  Provisioned dashboards and scrape config
tests/e2e/            Black-box tests against the compose stack
```

## Roadmap

- **Auth and a worker API.** Users, API keys, and an HTTP claim/heartbeat/result API so workers no longer need direct database access.
- **Chrome extension worker.** Run the agent in the user's own logged-in browser so cancellations use their real session and they never hand over a password.
- **Cloud deployment.**
- **Dead-letter crash loops.** The requeuer should move a task to `DEAD_LETTER` once `attempts` reaches `max_attempts`; today only the worker's failure path does, so a task that crashes its worker every time is requeued indefinitely.
