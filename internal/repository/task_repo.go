// Package repository contains all database access logic for the tasks domain.
// No SQL should live outside this package. Handlers call repository methods;
// they never construct queries themselves.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/wesil/trial-terminator/internal/models"
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
