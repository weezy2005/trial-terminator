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

// claimTask atomically transitions a task from PENDING → IN_PROGRESS.
// This is the same optimistic locking pattern as the Go ClaimTask method.
// Only one worker wins when multiple workers race for the same task.
export async function claimTask(taskId, workerId) {
  const result = await pool.query(
    `UPDATE tasks
     SET    status    = 'IN_PROGRESS',
            locked_at = NOW(),
            locked_by = $2,
            attempts  = attempts + 1
     WHERE  id     = $1
     AND    status = 'PENDING'
     RETURNING *`,
    [taskId, workerId]
  );
  // Returns the task row if claimed, null if another worker got there first.
  return result.rows[0] ?? null;
}

// updateTaskSuccess marks a task as successfully completed.
export async function updateTaskSuccess(taskId) {
  await pool.query(
    `UPDATE tasks
     SET  status    = 'SUCCESS',
          locked_at = NULL,
          locked_by = NULL
     WHERE id = $1`,
    [taskId]
  );
}

// updateTaskRetry increments the failure count and marks for retry.
// The Go requeue goroutine will NOT pick this up (it only watches IN_PROGRESS).
// We push the task back to Redis ourselves immediately after calling this.
export async function updateTaskRetry(taskId, errorMessage) {
  await pool.query(
    `UPDATE tasks
     SET  status        = 'RETRY',
          error_message = $2,
          locked_at     = NULL,
          locked_by     = NULL
     WHERE id = $1`,
    [taskId, errorMessage]
  );
}

// updateTaskDeadLetter moves a task to the Dead Letter state.
// Called when attempts >= max_attempts. The evidencePath is the relative
// path to the screenshot taken at the moment of failure.
export async function updateTaskDeadLetter(taskId, errorMessage, evidencePath) {
  await pool.query(
    `UPDATE tasks
     SET  status         = 'DEAD_LETTER',
          error_message  = $2,
          evidence_path  = $3,
          locked_at      = NULL,
          locked_by      = NULL
     WHERE id = $1`,
    [taskId, errorMessage, evidencePath]
  );
}

export { pool };
