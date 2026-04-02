-- Migration 002: add scheduled_for to tasks
--
-- scheduled_for is the time at which the task should be picked up and run.
-- NULL means "run immediately" (existing behaviour — all current tasks are unaffected).
-- A future timestamp means "hold this task until that time, then enqueue it".
--
-- The scheduler goroutine in the API server polls every minute for tasks where
-- scheduled_for <= NOW() and status = 'PENDING', then pushes them into Redis.

ALTER TABLE tasks ADD COLUMN scheduled_for TIMESTAMPTZ;

-- Partial index: only index rows that have a schedule and are still waiting.
-- Rows where scheduled_for IS NULL (i.e. run-immediately tasks) are excluded —
-- they are never queried by the scheduler.
CREATE INDEX idx_tasks_scheduled_for
    ON tasks (scheduled_for)
    WHERE scheduled_for IS NOT NULL AND status = 'PENDING';
