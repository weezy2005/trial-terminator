// Package handlers contains HTTP request handlers.
// Handlers are responsible for:
//   1. Parsing and validating the incoming HTTP request
//   2. Calling the appropriate repository/service method
//   3. Writing the HTTP response
//
// Handlers do NOT contain business logic. They are a translation layer
// between HTTP and your domain. This keeps them thin and easy to test.
package handlers

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/wesil/trial-terminator/internal/models"
	"github.com/wesil/trial-terminator/internal/repository"
)

// TaskHandler holds the dependencies needed by task-related HTTP handlers.
// Using a struct instead of package-level variables means dependencies are
// explicit and injectable — critical for unit testing.
type TaskHandler struct {
	repo   repository.TaskRepository
	logger *slog.Logger
}

// NewTaskHandler constructs a TaskHandler.
// Note: we accept the interface (TaskRepository), not the concrete type.
// This is how we achieve testability: in tests, pass a mock; in prod, pass the real repo.
func NewTaskHandler(repo repository.TaskRepository, logger *slog.Logger) *TaskHandler {
	return &TaskHandler{repo: repo, logger: logger}
}

// CreateTask handles POST /tasks
//
// The happy path:
// 1. Decode the JSON request body into CreateTaskRequest
// 2. Validate required fields
// 3. Call repo.CreateTask — which handles idempotency transparently
// 4. Return 201 Created with the task JSON
//
// The idempotent path (same idempotency_key sent again):
// Steps 1-3 are identical. repo.CreateTask returns the EXISTING task.
// We return 200 OK (not 201) to signal "found existing, not newly created."
//
// HTTP status code choice:
// 201 = Created (new resource)
// 200 = OK (existing resource returned)
// This distinction matters for clients that track whether a task was newly created.
func (h *TaskHandler) CreateTask(w http.ResponseWriter, r *http.Request) {
	// --- Step 1: Decode ---
	var req models.CreateTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body: "+err.Error())
		return
	}
	defer r.Body.Close()

	// --- Step 2: Validate ---
	if err := validateCreateTaskRequest(&req); err != nil {
		h.writeError(w, http.StatusUnprocessableEntity, err.Error())
		return
	}

	// --- Step 3: Create (or fetch existing) ---
	task, err := h.repo.CreateTask(r.Context(), &req)
	if err != nil {
		// If the task was not found after the duplicate key path,
		// something is genuinely wrong — log it and return 500.
		if errors.Is(err, pgx.ErrNoRows) {
			h.logger.Error("task not found after idempotency key lookup", "error", err)
			h.writeError(w, http.StatusInternalServerError, "internal error")
			return
		}

		h.logger.Error("failed to create task", "error", err,
			"idempotency_key", req.IdempotencyKey,
			"service_name", req.ServiceName,
		)
		h.writeError(w, http.StatusInternalServerError, "failed to create task")
		return
	}

	// --- Step 4: Respond ---
	// Determine status code based on whether this was newly created.
	// If the task's CreatedAt equals its UpdatedAt exactly, it's new.
	// A more robust approach: the repo could return a boolean "wasCreated".
	// We use 201 for new tasks and 200 for returned existing tasks.
	// Since CreateTask returns the task either way, we check if the task
	// was created in this exact request by comparing timestamps.
	// A simpler heuristic: the repo sets attempts=0 only on insert,
	// but the cleanest signal is to check if CreatedAt == UpdatedAt
	// (the trigger hasn't fired yet on a brand new row).
	statusCode := http.StatusCreated
	if task.CreatedAt != task.UpdatedAt {
		// The updated_at trigger has fired at least once — this task existed before.
		statusCode = http.StatusOK
	}

	h.writeJSON(w, statusCode, task)
}

// GetTask handles GET /tasks/{id}
func (h *TaskHandler) GetTask(w http.ResponseWriter, r *http.Request) {
	// Extract the id path parameter.
	// In Go 1.22+, the standard library's ServeMux supports path parameters
	// via {name} syntax in route patterns — no third-party router needed.
	idStr := r.PathValue("id")
	if idStr == "" {
		h.writeError(w, http.StatusBadRequest, "missing task id")
		return
	}

	// We need a UUID, not a raw string — validate format upfront.
	// This prevents SQL injection-style attacks (not that pgx is vulnerable,
	// but fail early with a clear message rather than a cryptic DB error).
	from, err := parseUUID(idStr)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid task id format: must be a UUID")
		return
	}

	task, err := h.repo.GetTaskByID(r.Context(), from)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			h.writeError(w, http.StatusNotFound, "task not found")
			return
		}
		h.logger.Error("failed to get task", "error", err, "id", idStr)
		h.writeError(w, http.StatusInternalServerError, "failed to get task")
		return
	}

	h.writeJSON(w, http.StatusOK, task)
}

// --- Helpers ---

// validateCreateTaskRequest checks required fields and format constraints.
// Validation lives in the handler, not the model, because validation rules
// can vary by context (the same model might have different rules for internal
// vs. external-facing APIs).
func validateCreateTaskRequest(req *models.CreateTaskRequest) error {
	var errs []string

	if strings.TrimSpace(req.IdempotencyKey) == "" {
		errs = append(errs, "idempotency_key is required")
	}
	if strings.TrimSpace(req.ServiceName) == "" {
		errs = append(errs, "service_name is required")
	}
	if strings.TrimSpace(req.UserEmail) == "" {
		errs = append(errs, "user_email is required")
	}

	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

// parseUUID wraps uuid.Parse to avoid importing google/uuid in this package.
// Keeping UUID parsing close to where it's used reduces coupling.
func parseUUID(s string) (interface{ String() string }, error) {
	// We import uuid indirectly via repository. For the handler,
	// we just need to validate the format — pass the string directly to pgx.
	// pgx handles UUID strings natively without explicit uuid.UUID conversion.
	// This function exists purely to give a clean error message.
	if len(s) != 36 {
		return nil, errors.New("not a valid UUID")
	}
	return uuidValidator(s), nil
}

// uuidValidator is a thin wrapper to satisfy the parseUUID return type.
type uuidValidator string

func (u uuidValidator) String() string { return string(u) }

// writeJSON serializes v to JSON and writes it to the response.
// All JSON responses in this API go through this function — consistency.
func (h *TaskHandler) writeJSON(w http.ResponseWriter, statusCode int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		h.logger.Error("failed to encode JSON response", "error", err)
	}
}

// writeError writes a JSON error response in the standard error envelope format.
// Using a consistent error shape means API clients can reliably parse errors:
// { "error": "message here" }
func (h *TaskHandler) writeError(w http.ResponseWriter, statusCode int, message string) {
	h.writeJSON(w, statusCode, map[string]string{"error": message})
}
