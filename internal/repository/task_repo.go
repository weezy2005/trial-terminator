// Package repository contains all database access logic for the tasks domain.
// No SQL should live outside this package. Handlers call repository methods;
// they never construct queries themselves.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/weezy2005/trial-terminator/internal/models"
)

// ErrDuplicateIdempotencyKey is returned when a task with the same
// idempotency key already exists. This is NOT an error in the traditional sense —
// it's the normal outcome of an idempotent system. The handler receives this
// and fetches + returns the existing task instead of failing.
var ErrDuplicateIdempotencyKey = errors.New("task with this idempotency key already exists")

// TaskRepository defines the interface for task data access.
//
// ARCHITECTURAL DECISION: Why define an interface in the repository package?
// Go interfaces are satisfied implicitly — any type with the right methods
// automatically satisfies the interface. Defining it here lets us:
// 1. Pass *PostgresTaskRepo in production code
// 2. Pass a hand-written mock in unit tests (Sprint 5)
// 3. Express clearly what the handler actually needs from storage
//    (Liskov Substitution Principle in practice)
type TaskRepository interface {
	CreateTask(ctx context.Context, req *models.CreateTaskRequest) (*models.Task, error)
	GetTaskByID(ctx context.Context, id uuid.UUID) (*models.Task, error)
	GetTaskByIdempotencyKey(ctx context.Context, key uuid.UUID) (*models.Task, error)

	// Sprint 2: worker lifecycle methods

	// ClaimTask atomically transitions a task from PENDING → IN_PROGRESS.
	// Only one worker can claim a given task — the UPDATE's WHERE clause
	// acts as an optimistic lock. Returns nil if the task was already claimed.
	ClaimTask(ctx context.Context, taskID uuid.UUID, workerID string) (*models.Task, error)

	// UpdateTaskStatus sets the status and optional error message on a task.
	// Used by workers to record SUCCESS, FAILED, or DEAD_LETTER outcomes.
	UpdateTaskStatus(ctx context.Context, taskID uuid.UUID, status models.TaskStatus, errMsg *string) error

	// GetStaleInProgressTasks returns tasks stuck in IN_PROGRESS for longer
	// than the given threshold — evidence that the worker which claimed them crashed.
	GetStaleInProgressTasks(ctx context.Context, threshold time.Duration) ([]*models.Task, error)

	// RequeueTask resets a stale task back to PENDING so it can be re-picked.
	RequeueTask(ctx context.Context, taskID uuid.UUID) error
}

// PostgresTaskRepo is the production implementation of TaskRepository.
// It holds a *pgxpool.Pool — the pool is safe for concurrent use and
// manages connection lifecycle automatically.
type PostgresTaskRepo struct {
	db *pgxpool.Pool
}

// NewPostgresTaskRepo constructs a PostgresTaskRepo.
// Returning the concrete type here (not the interface) is idiomatic Go.
// The caller decides whether to hold it as the interface or the concrete type.
func NewPostgresTaskRepo(db *pgxpool.Pool) *PostgresTaskRepo {
	return &PostgresTaskRepo{db: db}
}

// CreateTask inserts a new task into the database.
//
// THE CORE IDEMPOTENCY LOGIC IS HERE — read this carefully.
//
// The SQL uses INSERT ... ON CONFLICT DO NOTHING.
// ON CONFLICT: triggers when the UNIQUE constraint on idempotency_key is violated.
// DO NOTHING: the insert is a no-op — no error, no update, no side effects.
//
// After the INSERT, we check how many rows were affected:
// - 1 row affected → new task was created → return it
// - 0 rows affected → a task with this key already existed → look it up and return it
//
// This two-step approach (insert, then check) is safe under concurrent load.
// Even if two goroutines race to insert the same key simultaneously,
// the database UNIQUE constraint is the final arbiter — exactly one will win,
// the other will hit ON CONFLICT and return 0 rows. No duplicate tasks possible.
func (r *PostgresTaskRepo) CreateTask(ctx context.Context, req *models.CreateTaskRequest) (*models.Task, error) {
	// Parse the idempotency key from the request string into a UUID type.
	// We do this validation here (in the repo) rather than the handler
	// because the DB constraint is on a UUID column — pgx needs a uuid.UUID, not a string.
	idempotencyKey, err := uuid.Parse(req.IdempotencyKey)
	if err != nil {
		return nil, fmt.Errorf("repository: invalid idempotency_key format: %w", err)
	}

	// Serialize the payload map to JSON bytes for the JSONB column.
	// If payload is nil/empty, we store NULL in the DB (cleaner than empty "{}").
	var payloadBytes []byte
	if len(req.Payload) > 0 {
		payloadBytes, err = json.Marshal(req.Payload)
		if err != nil {
			return nil, fmt.Errorf("repository: failed to marshal payload: %w", err)
		}
	}

	// The INSERT query.
	// Notice: we do NOT insert id, status, attempts, created_at, or updated_at.
	// Those have DEFAULT values in the schema — letting the DB handle defaults
	// means the application never has to worry about clock skew or default values
	// being out of sync between app versions.
	const insertSQL = `
		INSERT INTO tasks (idempotency_key, service_name, user_email, payload)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (idempotency_key) DO NOTHING
		RETURNING id, idempotency_key, service_name, user_email, status,
		          attempts, max_attempts, payload, error_message, evidence_path,
		          created_at, updated_at, locked_at, locked_by
	`

	task := &models.Task{}
	err = r.db.QueryRow(ctx, insertSQL,
		idempotencyKey,
		req.ServiceName,
		req.UserEmail,
		payloadBytes,
	).Scan(
		&task.ID,
		&task.IdempotencyKey,
		&task.ServiceName,
		&task.UserEmail,
		&task.Status,
		&task.Attempts,
		&task.MaxAttempts,
		&task.Payload,
		&task.ErrorMessage,
		&task.EvidencePath,
		&task.CreatedAt,
		&task.UpdatedAt,
		&task.LockedAt,
		&task.LockedBy,
	)

	// pgx.ErrNoRows is returned when ON CONFLICT DO NOTHING fires
	// and RETURNING returns zero rows. This is how we know the key already existed.
	if errors.Is(err, pgx.ErrNoRows) {
		// Fetch and return the existing task — the caller gets a consistent response
		// whether this is the first request or the 100th retry.
		return r.GetTaskByIdempotencyKey(ctx, idempotencyKey)
	}

	if err != nil {
		// Check for a PostgreSQL unique violation (error code 23505).
		// This handles a race condition: if two requests arrive
		// simultaneously and both pass the ON CONFLICT check but
		// one commits before the other can read via RETURNING.
		// In practice this is rare with ON CONFLICT, but being defensive is correct.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return r.GetTaskByIdempotencyKey(ctx, idempotencyKey)
		}
		return nil, fmt.Errorf("repository: failed to insert task: %w", err)
	}

	return task, nil
}

// GetTaskByID retrieves a task by its internal UUID primary key.
func (r *PostgresTaskRepo) GetTaskByID(ctx context.Context, id uuid.UUID) (*models.Task, error) {
	const query = `
		SELECT id, idempotency_key, service_name, user_email, status,
		       attempts, max_attempts, payload, error_message, evidence_path,
		       created_at, updated_at, locked_at, locked_by
		FROM tasks
		WHERE id = $1
	`

	task := &models.Task{}
	err := r.db.QueryRow(ctx, query, id).Scan(
		&task.ID,
		&task.IdempotencyKey,
		&task.ServiceName,
		&task.UserEmail,
		&task.Status,
		&task.Attempts,
		&task.MaxAttempts,
		&task.Payload,
		&task.ErrorMessage,
		&task.EvidencePath,
		&task.CreatedAt,
		&task.UpdatedAt,
		&task.LockedAt,
		&task.LockedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("repository: task not found: %w", pgx.ErrNoRows)
	}
	if err != nil {
		return nil, fmt.Errorf("repository: failed to get task by id: %w", err)
	}

	return task, nil
}

// ClaimTask atomically transitions a task from PENDING to IN_PROGRESS.
//
// The WHERE status = 'PENDING' clause is the distributed lock.
// If two workers race to claim the same task simultaneously:
// - Worker A: UPDATE runs, finds status='PENDING', succeeds → gets the task
// - Worker B: UPDATE runs, finds status='IN_PROGRESS' (already changed) → 0 rows affected
// Worker B gets nil back and moves on to the next task in the queue.
// No coordination protocol needed — the database serializes the writes.
//
// We also increment attempts here (not in the worker) so the count is accurate
// even if the worker crashes before it can update the DB itself.
func (r *PostgresTaskRepo) ClaimTask(ctx context.Context, taskID uuid.UUID, workerID string) (*models.Task, error) {
	const query = `
		UPDATE tasks
		SET    status    = 'IN_PROGRESS',
		       locked_at = NOW(),
		       locked_by = $2,
		       attempts  = attempts + 1
		WHERE  id     = $1
		AND    status = 'PENDING'
		RETURNING id, idempotency_key, service_name, user_email, status,
		          attempts, max_attempts, payload, error_message, evidence_path,
		          created_at, updated_at, locked_at, locked_by
	`

	task := &models.Task{}
	err := r.db.QueryRow(ctx, query, taskID, workerID).Scan(
		&task.ID, &task.IdempotencyKey, &task.ServiceName, &task.UserEmail,
		&task.Status, &task.Attempts, &task.MaxAttempts, &task.Payload,
		&task.ErrorMessage, &task.EvidencePath,
		&task.CreatedAt, &task.UpdatedAt, &task.LockedAt, &task.LockedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		// Task was already claimed by another worker — this is normal, not an error.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("repository: failed to claim task: %w", err)
	}
	return task, nil
}

// UpdateTaskStatus sets a task's status and records an optional error message.
// Called by workers to record the final outcome of a cancellation attempt.
func (r *PostgresTaskRepo) UpdateTaskStatus(ctx context.Context, taskID uuid.UUID, status models.TaskStatus, errMsg *string) error {
	const query = `
		UPDATE tasks
		SET  status        = $2,
		     error_message = $3,
		     locked_at     = NULL,
		     locked_by     = NULL
		WHERE id = $1
	`
	_, err := r.db.Exec(ctx, query, taskID, status, errMsg)
	if err != nil {
		return fmt.Errorf("repository: failed to update task status: %w", err)
	}
	return nil
}

// GetStaleInProgressTasks finds tasks that have been IN_PROGRESS for longer
// than the given threshold — indicating the worker that claimed them is dead.
//
// ARCHITECTURAL DECISION: Why query Postgres for stale tasks instead of using
// Redis TTLs or a separate heartbeat key?
// Postgres is our source of truth. If we used Redis TTLs to detect dead workers,
// we'd have a split-brain scenario: what if Redis and Postgres disagree on a task's
// state? By storing locked_at in Postgres and querying it, we stay single-source.
// The trade-off: this query runs every 30 seconds — at high volume, add a partial
// index on (locked_at) WHERE status = 'IN_PROGRESS' (already done in migration 001).
func (r *PostgresTaskRepo) GetStaleInProgressTasks(ctx context.Context, threshold time.Duration) ([]*models.Task, error) {
	const query = `
		SELECT id, idempotency_key, service_name, user_email, status,
		       attempts, max_attempts, payload, error_message, evidence_path,
		       created_at, updated_at, locked_at, locked_by
		FROM   tasks
		WHERE  status    = 'IN_PROGRESS'
		AND    locked_at < NOW() - $1::interval
	`

	// Convert Go duration to a Postgres interval string (e.g., "2m0s" → "2 minutes")
	intervalStr := threshold.String()

	rows, err := r.db.Query(ctx, query, intervalStr)
	if err != nil {
		return nil, fmt.Errorf("repository: failed to query stale tasks: %w", err)
	}
	defer rows.Close()

	var tasks []*models.Task
	for rows.Next() {
		task := &models.Task{}
		if err := rows.Scan(
			&task.ID, &task.IdempotencyKey, &task.ServiceName, &task.UserEmail,
			&task.Status, &task.Attempts, &task.MaxAttempts, &task.Payload,
			&task.ErrorMessage, &task.EvidencePath,
			&task.CreatedAt, &task.UpdatedAt, &task.LockedAt, &task.LockedBy,
		); err != nil {
			return nil, fmt.Errorf("repository: failed to scan stale task: %w", err)
		}
		tasks = append(tasks, task)
	}
	return tasks, rows.Err()
}

// RequeueTask resets a stale IN_PROGRESS task back to PENDING.
// The requeue goroutine calls this, then pushes the task ID back to Redis.
// We clear locked_at and locked_by so the task looks fresh to the next worker.
func (r *PostgresTaskRepo) RequeueTask(ctx context.Context, taskID uuid.UUID) error {
	const query = `
		UPDATE tasks
		SET  status    = 'PENDING',
		     locked_at = NULL,
		     locked_by = NULL
		WHERE id     = $1
		AND   status = 'IN_PROGRESS'
	`
	_, err := r.db.Exec(ctx, query, taskID)
	if err != nil {
		return fmt.Errorf("repository: failed to requeue task: %w", err)
	}
	return nil
}

// GetTaskByIdempotencyKey retrieves an existing task by its idempotency key.
// Called internally by CreateTask when a duplicate key is detected.
func (r *PostgresTaskRepo) GetTaskByIdempotencyKey(ctx context.Context, key uuid.UUID) (*models.Task, error) {
	const query = `
		SELECT id, idempotency_key, service_name, user_email, status,
		       attempts, max_attempts, payload, error_message, evidence_path,
		       created_at, updated_at, locked_at, locked_by
		FROM tasks
		WHERE idempotency_key = $1
	`

	task := &models.Task{}
	err := r.db.QueryRow(ctx, query, key).Scan(
		&task.ID,
		&task.IdempotencyKey,
		&task.ServiceName,
		&task.UserEmail,
		&task.Status,
		&task.Attempts,
		&task.MaxAttempts,
		&task.Payload,
		&task.ErrorMessage,
		&task.EvidencePath,
		&task.CreatedAt,
		&task.UpdatedAt,
		&task.LockedAt,
		&task.LockedBy,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("repository: task not found by idempotency key: %w", pgx.ErrNoRows)
	}
	if err != nil {
		return nil, fmt.Errorf("repository: failed to get task by idempotency key: %w", err)
	}

	return task, nil
}
