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
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/weezy2005/trial-terminator/internal/metrics"
	"github.com/weezy2005/trial-terminator/internal/models"
	"github.com/weezy2005/trial-terminator/internal/queue"
	"github.com/weezy2005/trial-terminator/internal/repository"
)

// TaskHandler holds the dependencies needed by task-related HTTP handlers.
// Using a struct instead of package-level variables means dependencies are
// explicit and injectable — critical for unit testing.
type TaskHandler struct {
	repo     repository.TaskRepository
	producer *queue.Producer
	logger   *slog.Logger
}

// NewTaskHandler constructs a TaskHandler.
// Note: we accept the interface (TaskRepository), not the concrete type.
// This is how we achieve testability: in tests, pass a mock; in prod, pass the real repo.
func NewTaskHandler(repo repository.TaskRepository, producer *queue.Producer, logger *slog.Logger) *TaskHandler {
	return &TaskHandler{repo: repo, producer: producer, logger: logger}
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

	// --- Step 4: Record metrics ---
	// Only increment for genuinely new tasks — not idempotent replays.
	isNew := task.CreatedAt.Equal(task.UpdatedAt)
	if isNew {
		metrics.TasksCreatedTotal.WithLabelValues(task.ServiceName).Inc()
		metrics.TasksInProgress.Inc() // worker will Dec() when it finishes
	}

	// --- Step 5: Enqueue (only for new, non-scheduled tasks) ---
	// Scheduled tasks (scheduled_for is in the future) are held in Postgres
	// until the scheduler goroutine enqueues them at the right time.
	// Immediate tasks are pushed to Redis right now so a worker picks them up.
	isScheduledFuture := task.ScheduledFor != nil && task.ScheduledFor.After(time.Now())
	if isNew && !isScheduledFuture {
		if err := h.producer.Enqueue(r.Context(), task.ID); err != nil {
			// Non-fatal: log it. The requeue goroutine is our safety net.
			h.logger.Error("failed to enqueue task after creation",
				"task_id", task.ID,
				"error", err,
			)
		}
	}

	// --- Step 5: Respond ---
	statusCode := http.StatusCreated
	if !isNew {
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

	// Validate UUID format upfront — fail with a clear message rather than
	// letting a malformed string reach the DB layer.
	id, err := parseUUID(idStr)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid task id format: must be a UUID")
		return
	}

	task, err := h.repo.GetTaskByID(r.Context(), id)
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

// GetTaskScreenshot handles GET /tasks/{id}/screenshot
//
// Returns the most recent screenshot of the AI agent's browser view for a task.
// Used by Watch Mode in the UI — the frontend polls this endpoint every ~1.5s
// while a task is IN_PROGRESS so the user can watch the agent work.
//
// Reads evidence/{id}-latest.png from the shared evidence volume.
// Returns 404 if the file doesn't exist yet (task hasn't taken a screenshot).
//
// Security note: id is validated as a UUID before being used in the file path,
// so path-traversal attempts like "../etc/passwd" are rejected upstream.
func (h *TaskHandler) GetTaskScreenshot(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	if _, err := parseUUID(idStr); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid task id format: must be a UUID")
		return
	}

	evidenceDir := os.Getenv("EVIDENCE_PATH")
	if evidenceDir == "" {
		evidenceDir = "./evidence"
	}

	// filepath.Join is safe here because idStr has been validated as a UUID
	// (only hex chars and dashes — no slashes or dots).
	imagePath := filepath.Join(evidenceDir, idStr+"-latest.png")

	// Tell browsers never to cache — Watch Mode polls this URL repeatedly and
	// we always want the freshest frame.
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Expires", "0")

	f, err := os.Open(imagePath)
	if err != nil {
		if os.IsNotExist(err) {
			// Not an error — the task may not have taken a screenshot yet.
			// The frontend treats 404 as "poll again later."
			http.NotFound(w, r)
			return
		}
		h.logger.Error("failed to open screenshot", "error", err, "path", imagePath)
		h.writeError(w, http.StatusInternalServerError, "failed to read screenshot")
		return
	}
	defer f.Close()

	w.Header().Set("Content-Type", "image/png")
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, f); err != nil {
		// Headers already sent; just log and move on.
		h.logger.Error("failed to stream screenshot", "error", err)
	}
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

// parseUUID parses a UUID string, returning a clear error on bad format.
func parseUUID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, errors.New("not a valid UUID")
	}
	return id, nil
}

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
