-- Migration: 001_create_tasks
-- Purpose: Create the core tasks table that drives the entire state machine.
--
-- ARCHITECTURAL DECISION: Why a PostgreSQL ENUM for status?
-- An ENUM enforces valid states at the DATABASE level — not just the application level.
-- Even if a bug in Go code tries to write "RUNING" (typo), Postgres rejects it.
-- This is a data integrity guarantee you cannot get from a plain VARCHAR column.

CREATE TYPE task_status AS ENUM (
    'PENDING',      -- Task created, waiting to be picked up by a worker
    'IN_PROGRESS',  -- A worker has claimed the task and is executing it
    'SUCCESS',      -- Cancellation completed successfully
    'FAILED',       -- Cancellation failed, but retries remain
    'RETRY',        -- Explicitly scheduled for retry (exponential backoff)
    'DEAD_LETTER'   -- All retries exhausted; requires human intervention
);

-- ARCHITECTURAL DECISION: Why UUID primary key instead of SERIAL (auto-increment)?
-- Auto-increment IDs (1, 2, 3...) are sequential and predictable — a security risk
-- (someone can enumerate all task IDs) and a distributed systems problem
-- (who owns the counter when you have multiple DB nodes?).
-- UUIDs are globally unique, safe to generate on the client, and portable.
CREATE TABLE tasks (
    -- Identity
    id               UUID        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- IDEMPOTENCY KEY: This is the most important column in the entire schema.
    -- The UNIQUE constraint is enforced by the database engine via a B-tree index.
    -- If two requests arrive simultaneously with the same key, one will WIN and
    -- the other will get a unique constraint violation — which we catch in Go
    -- and return the *existing* task instead of an error. This is idempotency.
    idempotency_key  UUID        NOT NULL UNIQUE,

    -- What we're cancelling
    service_name     VARCHAR(255) NOT NULL,
    user_email       VARCHAR(255) NOT NULL,

    -- State machine
    status           task_status NOT NULL DEFAULT 'PENDING',

    -- Retry tracking — we never loop forever
    attempts         INT         NOT NULL DEFAULT 0,
    max_attempts     INT         NOT NULL DEFAULT 3,

    -- JSONB allows us to store arbitrary service-specific data
    -- (e.g., Netflix account ID, Spotify subscription tier).
    -- ARCHITECTURAL DECISION: Why JSONB over separate columns?
    -- Each subscription service has different cancellation inputs.
    -- Adding a column per service would cause table bloat and require migrations
    -- every time we add a new service. JSONB lets us stay flexible while keeping
    -- the core schema stable. We can always add indexed columns later if needed.
    payload          JSONB,

    -- Audit trail
    error_message    TEXT,
    evidence_path    TEXT,   -- Screenshot path for dead-letter tasks (Sprint 3)

    -- Timestamps
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    -- Heartbeat / distributed locking fields (used in Sprint 2)
    -- locked_at: when a worker claimed this task
    -- locked_by: which worker instance holds the lock (e.g., "worker-abc123")
    locked_at        TIMESTAMPTZ,
    locked_by        VARCHAR(255)
);

-- Index for the most common query pattern: "give me all PENDING tasks"
-- Without this index, Postgres does a full table scan on every worker poll.
CREATE INDEX idx_tasks_status ON tasks(status);

-- Index for idempotency key lookups on POST /tasks
-- pgx creates an index automatically for UNIQUE constraints,
-- but naming it explicitly makes it easier to monitor in pg_stat_user_indexes.
CREATE INDEX idx_tasks_idempotency_key ON tasks(idempotency_key);

-- Index for the heartbeat query in Sprint 2:
-- "find IN_PROGRESS tasks that haven't been updated in X minutes"
CREATE INDEX idx_tasks_locked_at ON tasks(locked_at) WHERE locked_at IS NOT NULL;

-- Automatically update updated_at on any row modification.
-- ARCHITECTURAL DECISION: Why a trigger instead of doing it in Go?
-- If you ever update a row directly in psql (debugging, manual fix),
-- the trigger still fires. The application is not the only thing that touches prod data.
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = NOW();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER tasks_updated_at
    BEFORE UPDATE ON tasks
    FOR EACH ROW
    EXECUTE FUNCTION update_updated_at_column();
