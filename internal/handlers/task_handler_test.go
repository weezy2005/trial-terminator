package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/weezy2005/trial-terminator/internal/models"
	"github.com/weezy2005/trial-terminator/internal/repository"
)

// fakeRepo embeds the interface so it satisfies TaskRepository without
// implementing every method. Any method a test didn't stub panics on the nil
// embedded value, which surfaces unexpected calls instead of hiding them.
type fakeRepo struct {
	repository.TaskRepository
	createTask  func(ctx context.Context, req *models.CreateTaskRequest) (*models.Task, bool, error)
	getTaskByID func(ctx context.Context, id uuid.UUID) (*models.Task, error)
}

func (f *fakeRepo) CreateTask(ctx context.Context, req *models.CreateTaskRequest) (*models.Task, bool, error) {
	return f.createTask(ctx, req)
}

func (f *fakeRepo) GetTaskByID(ctx context.Context, id uuid.UUID) (*models.Task, error) {
	return f.getTaskByID(ctx, id)
}

// fakeEnqueuer records every enqueued ID so tests can assert exactly how many
// times a task was pushed to the queue.
type fakeEnqueuer struct {
	enqueued []uuid.UUID
	err      error
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, id uuid.UUID) error {
	f.enqueued = append(f.enqueued, id)
	return f.err
}

func newTestHandler(repo *fakeRepo, q *fakeEnqueuer) *TaskHandler {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewTaskHandler(repo, q, logger)
}

func validBody(t *testing.T) *bytes.Buffer {
	t.Helper()
	b, err := json.Marshal(models.CreateTaskRequest{
		IdempotencyKey: uuid.NewString(),
		ServiceName:    "netflix",
		UserEmail:      "user@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewBuffer(b)
}

func sampleTask() *models.Task {
	now := time.Now()
	return &models.Task{
		ID:          uuid.New(),
		ServiceName: "netflix",
		Status:      models.StatusPending,
		MaxAttempts: 3,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

func TestCreateTask(t *testing.T) {
	future := time.Now().Add(24 * time.Hour)

	tests := []struct {
		name         string
		created      bool
		scheduledFor *time.Time
		enqueueErr   error
		wantStatus   int
		wantEnqueued int
	}{
		{
			name:         "new task is created and enqueued",
			created:      true,
			wantStatus:   http.StatusCreated,
			wantEnqueued: 1,
		},
		{
			// Regression: replays used to be detected by created_at == updated_at,
			// which is still true for a task no worker has touched yet. That made
			// a replay look new and pushed the same task to Redis twice.
			name:         "idempotent replay of untouched task returns 200 and is not re-enqueued",
			created:      false,
			wantStatus:   http.StatusOK,
			wantEnqueued: 0,
		},
		{
			name:         "future scheduled task is held for the scheduler",
			created:      true,
			scheduledFor: &future,
			wantStatus:   http.StatusCreated,
			wantEnqueued: 0,
		},
		{
			name:         "enqueue failure is non-fatal because the requeuer is the safety net",
			created:      true,
			enqueueErr:   errors.New("redis down"),
			wantStatus:   http.StatusCreated,
			wantEnqueued: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			task := sampleTask()
			task.ScheduledFor = tc.scheduledFor
			repo := &fakeRepo{createTask: func(context.Context, *models.CreateTaskRequest) (*models.Task, bool, error) {
				return task, tc.created, nil
			}}
			q := &fakeEnqueuer{err: tc.enqueueErr}

			rec := httptest.NewRecorder()
			newTestHandler(repo, q).CreateTask(rec, httptest.NewRequest(http.MethodPost, "/tasks", validBody(t)))

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d (body: %s)", rec.Code, tc.wantStatus, rec.Body)
			}
			if len(q.enqueued) != tc.wantEnqueued {
				t.Errorf("enqueued %d times, want %d", len(q.enqueued), tc.wantEnqueued)
			}
		})
	}
}

func TestCreateTask_RejectsBadInput(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantStatus int
	}{
		{"malformed JSON", `{not json`, http.StatusBadRequest},
		{"missing all fields", `{}`, http.StatusUnprocessableEntity},
		{"blank service name", fmt.Sprintf(`{"idempotency_key":%q,"service_name":"  ","user_email":"a@b.c"}`, uuid.NewString()), http.StatusUnprocessableEntity},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// No stubs: validation must reject the request before the repo is called.
			h := newTestHandler(&fakeRepo{}, &fakeEnqueuer{})
			rec := httptest.NewRecorder()
			h.CreateTask(rec, httptest.NewRequest(http.MethodPost, "/tasks", bytes.NewBufferString(tc.body)))

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestCreateTask_RepoErrorReturns500(t *testing.T) {
	repo := &fakeRepo{createTask: func(context.Context, *models.CreateTaskRequest) (*models.Task, bool, error) {
		return nil, false, errors.New("connection refused")
	}}
	q := &fakeEnqueuer{}

	rec := httptest.NewRecorder()
	newTestHandler(repo, q).CreateTask(rec, httptest.NewRequest(http.MethodPost, "/tasks", validBody(t)))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	if len(q.enqueued) != 0 {
		t.Error("task was enqueued even though it was never persisted")
	}
}

func TestGetTask(t *testing.T) {
	task := sampleTask()

	tests := []struct {
		name       string
		id         string
		repoErr    error
		wantStatus int
	}{
		{"found", task.ID.String(), nil, http.StatusOK},
		{"not found", uuid.NewString(), fmt.Errorf("repository: task not found: %w", pgx.ErrNoRows), http.StatusNotFound},
		{"db error", uuid.NewString(), errors.New("timeout"), http.StatusInternalServerError},
		{"malformed id", "not-a-uuid", nil, http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeRepo{getTaskByID: func(context.Context, uuid.UUID) (*models.Task, error) {
				if tc.repoErr != nil {
					return nil, tc.repoErr
				}
				return task, nil
			}}

			req := httptest.NewRequest(http.MethodGet, "/tasks/"+tc.id, nil)
			req.SetPathValue("id", tc.id)
			rec := httptest.NewRecorder()
			newTestHandler(repo, &fakeEnqueuer{}).GetTask(rec, req)

			if rec.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
		})
	}
}

func TestGetTaskScreenshot(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("EVIDENCE_PATH", dir)

	withShot := uuid.NewString()
	png := []byte("\x89PNG fake image bytes")
	if err := os.WriteFile(filepath.Join(dir, withShot+"-latest.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		id         string
		wantStatus int
	}{
		{"serves latest frame", withShot, http.StatusOK},
		{"no frame yet", uuid.NewString(), http.StatusNotFound},
		{"path traversal is rejected before touching the filesystem", "../../etc/passwd", http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/tasks/x/screenshot", nil)
			req.SetPathValue("id", tc.id)
			rec := httptest.NewRecorder()
			newTestHandler(&fakeRepo{}, &fakeEnqueuer{}).GetTaskScreenshot(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tc.wantStatus)
			}
			if tc.wantStatus != http.StatusOK {
				return
			}
			if !bytes.Equal(rec.Body.Bytes(), png) {
				t.Error("response body does not match the file on disk")
			}
			// Watch Mode polls the same URL; a cached frame would freeze the view.
			if got := rec.Header().Get("Cache-Control"); got == "" {
				t.Error("missing Cache-Control header")
			}
		})
	}
}
