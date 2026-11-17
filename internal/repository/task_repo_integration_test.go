//go:build integration

// Integration tests run against a real Postgres because the guarantees under
// test (unique constraints, row-level locking on UPDATE ... WHERE, interval
// parsing) live in the database, not in Go. A mock would only test the mock.
//
// Run with:
//
//	TEST_DATABASE_URL=postgres://user:pass@localhost:5432/db?sslmode=disable \
//	  go test -tags=integration ./internal/...
package repository

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/weezy2005/trial-terminator/internal/models"
	"github.com/weezy2005/trial-terminator/internal/pgtest"
)

// newTestRepo gives each test its own schema, migrated by the same runner
// the API uses at startup.
func newTestRepo(t *testing.T) (*PostgresTaskRepo, *pgxpool.Pool) {
	t.Helper()
	pool := pgtest.MigratedPool(t)
	return NewPostgresTaskRepo(pool), pool
}

func newRequest() *models.CreateTaskRequest {
	return &models.CreateTaskRequest{
		IdempotencyKey: uuid.NewString(),
		ServiceName:    "netflix",
		UserEmail:      "user@example.com",
	}
}

func mustCreate(t *testing.T, repo *PostgresTaskRepo, req *models.CreateTaskRequest) *models.Task {
	t.Helper()
	task, _, err := repo.CreateTask(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func TestCreateTask_ConcurrentSameKeyCreatesExactlyOnce(t *testing.T) {
	t.Parallel()
	repo, pool := newTestRepo(t)
	req := newRequest()

	const callers = 20
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		created int
		ids     = map[uuid.UUID]bool{}
	)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // release every goroutine at once to maximise contention
			task, isNew, err := repo.CreateTask(context.Background(), req)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			ids[task.ID] = true
			if isNew {
				created++
			}
		}()
	}
	close(start)
	wg.Wait()

	if created != 1 {
		t.Errorf("%d callers were told they created the task, want exactly 1", created)
	}
	if len(ids) != 1 {
		t.Errorf("callers saw %d different task IDs, want 1", len(ids))
	}
	var rows int
	if err := pool.QueryRow(context.Background(), "SELECT count(*) FROM tasks").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("table has %d rows, want 1", rows)
	}
}

// Regression: a replay of a task no worker has touched yet must still report
// created=false. The handler used to infer this from created_at == updated_at,
// which is true for exactly this case.
func TestCreateTask_ReplayOfUntouchedTaskIsNotNew(t *testing.T) {
	t.Parallel()
	repo, _ := newTestRepo(t)
	req := newRequest()

	first, created, err := repo.CreateTask(context.Background(), req)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	again, created, err := repo.CreateTask(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Error("replay reported created=true")
	}
	if again.ID != first.ID {
		t.Errorf("replay returned task %s, want %s", again.ID, first.ID)
	}
}

func TestClaimTask_OnlyOneWorkerWins(t *testing.T) {
	t.Parallel()
	repo, _ := newTestRepo(t)
	task := mustCreate(t, repo, newRequest())

	const workers = 10
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		winners []string
	)
	start := make(chan struct{})
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(workerID string) {
			defer wg.Done()
			<-start
			claimed, err := repo.ClaimTask(context.Background(), task.ID, workerID)
			if err != nil {
				t.Error(err)
				return
			}
			if claimed != nil {
				mu.Lock()
				winners = append(winners, workerID)
				mu.Unlock()
			}
		}(fmt.Sprintf("worker-%d", i))
	}
	close(start)
	wg.Wait()

	if len(winners) != 1 {
		t.Fatalf("%d workers claimed the task, want 1: %v", len(winners), winners)
	}
	got, err := repo.GetTaskByID(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StatusInProgress || got.Attempts != 1 || *got.LockedBy != winners[0] {
		t.Errorf("after claim: status=%s attempts=%d locked_by=%v", got.Status, got.Attempts, *got.LockedBy)
	}
}

func TestClaimTask_RefusesTaskScheduledInFuture(t *testing.T) {
	t.Parallel()
	repo, _ := newTestRepo(t)
	req := newRequest()
	later := time.Now().Add(time.Hour)
	req.ScheduledFor = &later
	task := mustCreate(t, repo, req)

	claimed, err := repo.ClaimTask(context.Background(), task.ID, "worker-early")
	if err != nil {
		t.Fatal(err)
	}
	if claimed != nil {
		t.Error("worker claimed a task an hour before it was due")
	}
}

func TestRequeue_OnlyReclaimsTasksPastTheHeartbeatThreshold(t *testing.T) {
	t.Parallel()
	repo, pool := newTestRepo(t)
	ctx := context.Background()
	const threshold = 30 * time.Second

	alive := mustCreate(t, repo, newRequest())
	dead := mustCreate(t, repo, newRequest())
	for _, id := range []uuid.UUID{alive.ID, dead.ID} {
		if c, err := repo.ClaimTask(ctx, id, "worker-"+id.String()[:4]); err != nil || c == nil {
			t.Fatalf("claim %s: %v", id, err)
		}
	}
	// Simulate a worker that stopped heartbeating a minute ago.
	if _, err := pool.Exec(ctx, "UPDATE tasks SET locked_at = NOW() - interval '1 minute' WHERE id = $1", dead.ID); err != nil {
		t.Fatal(err)
	}

	// threshold.String() is "30s"; this also checks Postgres parses Go's
	// duration format as an interval the way the requeuer assumes.
	stale, err := repo.GetStaleInProgressTasks(ctx, threshold)
	if err != nil {
		t.Fatal(err)
	}
	if len(stale) != 1 || stale[0].ID != dead.ID {
		t.Fatalf("stale scan returned %d tasks, want only the dead one", len(stale))
	}

	if ok, err := repo.RequeueTask(ctx, alive.ID, threshold); err != nil || ok {
		t.Errorf("requeued a task with a fresh heartbeat (ok=%v err=%v)", ok, err)
	}
	ok, err := repo.RequeueTask(ctx, dead.ID, threshold)
	if err != nil || !ok {
		t.Fatalf("did not requeue the dead task (ok=%v err=%v)", ok, err)
	}

	got, err := repo.GetTaskByID(ctx, dead.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != models.StatusPending || got.LockedAt != nil || got.LockedBy != nil {
		t.Errorf("requeued task not reset: status=%s locked_at=%v locked_by=%v", got.Status, got.LockedAt, got.LockedBy)
	}
	if got.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 (requeue must not reset the retry budget)", got.Attempts)
	}
}

// The requeuer scans, then updates. If the worker heartbeats in between, the
// UPDATE's own staleness check must make it a no-op.
func TestRequeue_HeartbeatBetweenScanAndUpdateKeepsOwnership(t *testing.T) {
	t.Parallel()
	repo, pool := newTestRepo(t)
	ctx := context.Background()
	const threshold = 30 * time.Second

	task := mustCreate(t, repo, newRequest())
	if c, err := repo.ClaimTask(ctx, task.ID, "worker-slow"); err != nil || c == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := pool.Exec(ctx, "UPDATE tasks SET locked_at = NOW() - interval '1 minute' WHERE id = $1", task.ID); err != nil {
		t.Fatal(err)
	}
	if stale, err := repo.GetStaleInProgressTasks(ctx, threshold); err != nil || len(stale) != 1 {
		t.Fatalf("expected 1 stale task, got %d (err=%v)", len(stale), err)
	}

	// The worker's heartbeat lands after the scan.
	if _, err := pool.Exec(ctx, "UPDATE tasks SET locked_at = NOW() WHERE id = $1 AND locked_by = 'worker-slow'", task.ID); err != nil {
		t.Fatal(err)
	}

	ok, err := repo.RequeueTask(ctx, task.ID, threshold)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("requeued a task whose worker heartbeated after the scan")
	}
}

func TestGetPendingScheduledTasks_ReturnsOnlyDueTasks(t *testing.T) {
	t.Parallel()
	repo, _ := newTestRepo(t)

	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	dueReq, laterReq := newRequest(), newRequest()
	dueReq.ScheduledFor, laterReq.ScheduledFor = &past, &future

	due := mustCreate(t, repo, dueReq)
	mustCreate(t, repo, laterReq)
	mustCreate(t, repo, newRequest()) // unscheduled: enqueued at creation, scheduler ignores it

	got, err := repo.GetPendingScheduledTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != due.ID {
		t.Errorf("got %d tasks, want only the one that is due", len(got))
	}
}
