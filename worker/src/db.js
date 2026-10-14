// db.js manages the PostgreSQL connection for the worker.
//
// ARCHITECTURAL DECISION: Why does the worker talk directly to Postgres
// instead of calling the Go API?
// The Go API is an external-facing HTTP interface — it's for clients like
// the frontend. Internal services (like this worker) should communicate
// through the shared data layer (Postgres + Redis) directly.
// Going through the HTTP API would add latency, create a coupling between
// worker and API deployment, and make the API a single point of failure
// for task processing. Direct DB access is the right call here.
import pg from 'pg';
import { config } from './config.js';

const { Pool } = pg;

// Create a connection pool. Same reasoning as the Go pgxpool —
// reusing connections is far cheaper than creating new ones per operation.
const pool = new Pool(config.postgres);

pool.on('error', (err) => {
  console.error('Unexpected Postgres client error', err);
});

// --- Task queries ---

// claimTask atomically transitions a task from PENDING/RETRY → IN_PROGRESS.
// This is the same optimistic locking pattern as the Go ClaimTask method.
// Only one worker wins when multiple workers race for the same task.
//
// Accepting both PENDING and RETRY means a task that failed on attempt 1
// (status becomes RETRY via updateTaskRetry) can be re-claimed on attempt 2.
// If we only accepted PENDING, retried tasks would be stuck forever.
export async function claimTask(taskId, workerId) {
  const result = await pool.query(
    `UPDATE tasks
     SET    status    = 'IN_PROGRESS',
            locked_at = NOW(),
            locked_by = $2,
            attempts  = attempts + 1
     WHERE  id     = $1
     AND    status IN ('PENDING', 'RETRY')
     RETURNING *`,
    [taskId, workerId]
  );
  // Returns the task row if claimed, null if another worker got there first.
  return result.rows[0] ?? null;
}

// heartbeat refreshes locked_at while a task is running so the Go requeuer
// can tell a slow-but-alive worker apart from a dead one. Guarded by
// locked_by so a worker can't keep a task alive after losing ownership.
export async function heartbeat(taskId, workerId) {
  const result = await pool.query(
    `UPDATE tasks
     SET    locked_at = NOW()
     WHERE  id        = $1
     AND    locked_by = $2
     AND    status    = 'IN_PROGRESS'`,
    [taskId, workerId]
  );
  return result.rowCount > 0;
}

// --- Result writes ---
//
// Every result write below is fenced on locked_by. If this worker was declared
// dead and the task was handed to another worker, the WHERE clause matches
// zero rows and the stale result is discarded instead of overwriting the
// new owner's state. Each function returns true if the write landed.

// updateTaskSuccess marks a task as successfully completed.
export async function updateTaskSuccess(taskId, workerId) {
  const result = await pool.query(
    `UPDATE tasks
     SET  status    = 'SUCCESS',
          locked_at = NULL,
          locked_by = NULL
     WHERE id = $1
     AND   locked_by = $2`,
    [taskId, workerId]
  );
  return result.rowCount > 0;
}

// updateTaskRetry increments the failure count and marks for retry.
// The Go requeue goroutine will NOT pick this up (it only watches IN_PROGRESS).
// We push the task back to Redis ourselves immediately after calling this.
export async function updateTaskRetry(taskId, workerId, errorMessage) {
  const result = await pool.query(
    `UPDATE tasks
     SET  status        = 'RETRY',
          error_message = $3,
          locked_at     = NULL,
          locked_by     = NULL
     WHERE id = $1
     AND   locked_by = $2`,
    [taskId, workerId, errorMessage]
  );
  return result.rowCount > 0;
}

// scheduleTask stores the detected trial end date and resets the task to PENDING.
// The Go scheduler goroutine will enqueue it when scheduled_for arrives.
//
// ARCHITECTURAL DECISION: Why set status back to PENDING instead of a new "SCHEDULED" status?
// PENDING already means "waiting to be processed". The scheduler already knows to only
// enqueue tasks where scheduled_for <= NOW(). Adding a new status would require
// changes to the Go API, the frontend badge colours, and the worker claim logic —
// all for no additional correctness. PENDING + scheduled_for together are sufficient.
export async function scheduleTask(taskId, workerId, scheduledFor) {
  const result = await pool.query(
    `UPDATE tasks
     SET  status        = 'PENDING',
          scheduled_for = $3,
          locked_at     = NULL,
          locked_by     = NULL
     WHERE id = $1
     AND   locked_by = $2`,
    [taskId, workerId, scheduledFor]
  );
  return result.rowCount > 0;
}

// updateTaskDeadLetter moves a task to the Dead Letter state.
// Called when attempts >= max_attempts. The evidencePath is the relative
// path to the screenshot taken at the moment of failure.
export async function updateTaskDeadLetter(taskId, workerId, errorMessage, evidencePath) {
  const result = await pool.query(
    `UPDATE tasks
     SET  status         = 'DEAD_LETTER',
          error_message  = $3,
          evidence_path  = $4,
          locked_at      = NULL,
          locked_by      = NULL
     WHERE id = $1
     AND   locked_by = $2`,
    [taskId, workerId, errorMessage, evidencePath]
  );
  return result.rowCount > 0;
}

export { pool };
