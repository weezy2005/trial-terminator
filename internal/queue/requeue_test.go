package queue

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/weezy2005/trial-terminator/internal/models"
	"github.com/weezy2005/trial-terminator/internal/repository"
)

// fakeRepo embeds the interface so only the methods under test need stubs;
// an unexpected call panics on the nil embedded value.
type fakeRepo struct {
	repository.TaskRepository
	stale     []*models.Task
	staleErr  error
	requeue   func(id uuid.UUID) (bool, error)
	requeued  []uuid.UUID
	due       []*models.Task
	dueErr    error
	threshold time.Duration
}

func (f *fakeRepo) GetStaleInProgressTasks(_ context.Context, threshold time.Duration) ([]*models.Task, error) {
	f.threshold = threshold
	return f.stale, f.staleErr
}

func (f *fakeRepo) RequeueTask(_ context.Context, id uuid.UUID, _ time.Duration) (bool, error) {
	f.requeued = append(f.requeued, id)
	return f.requeue(id)
}

func (f *fakeRepo) GetPendingScheduledTasks(context.Context) ([]*models.Task, error) {
	return f.due, f.dueErr
}

type fakeEnqueuer struct {
	enqueued []uuid.UUID
	failFor  map[uuid.UUID]bool
}

func (f *fakeEnqueuer) Enqueue(_ context.Context, id uuid.UUID) error {
	if f.failFor[id] {
		return errors.New("redis down")
	}
	f.enqueued = append(f.enqueued, id)
	return nil
}

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

func tasks(n int) []*models.Task {
	out := make([]*models.Task, n)
	for i := range out {
		out[i] = &models.Task{ID: uuid.New(), Status: models.StatusInProgress}
	}
	return out
}

func TestRequeuer_ReclaimsStaleTasks(t *testing.T) {
	stale := tasks(3)
	repo := &fakeRepo{stale: stale, requeue: func(uuid.UUID) (bool, error) { return true, nil }}
	q := &fakeEnqueuer{}

	NewRequeuer(repo, q, discard).runOnce(context.Background())

	if len(q.enqueued) != 3 {
		t.Fatalf("enqueued %d tasks, want 3", len(q.enqueued))
	}
	if repo.threshold != staleLockThreshold {
		t.Errorf("scanned with threshold %v, want %v", repo.threshold, staleLockThreshold)
	}
}

// A heartbeat can land between the scan and the UPDATE. RequeueTask re-checks
// staleness and reports false; pushing the ID anyway would hand a live task
// to a second worker, which is the duplicate-processing bug this guards against.
func TestRequeuer_SkipsTasksThatHeartbeatedAfterScan(t *testing.T) {
	stale := tasks(2)
	alive := stale[0].ID
	repo := &fakeRepo{stale: stale, requeue: func(id uuid.UUID) (bool, error) {
		return id != alive, nil
	}}
	q := &fakeEnqueuer{}

	NewRequeuer(repo, q, discard).runOnce(context.Background())

	if len(q.enqueued) != 1 || q.enqueued[0] != stale[1].ID {
		t.Errorf("enqueued %v, want only %v", q.enqueued, stale[1].ID)
	}
}

func TestRequeuer_DoesNotEnqueueWhenDBResetFails(t *testing.T) {
	stale := tasks(2)
	broken := stale[0].ID
	repo := &fakeRepo{stale: stale, requeue: func(id uuid.UUID) (bool, error) {
		if id == broken {
			return false, errors.New("deadlock detected")
		}
		return true, nil
	}}
	q := &fakeEnqueuer{}

	NewRequeuer(repo, q, discard).runOnce(context.Background())

	// The failed task must stay out of Redis (it's still IN_PROGRESS in the DB)
	// and the loop must keep going for the rest of the batch.
	if len(q.enqueued) != 1 || q.enqueued[0] != stale[1].ID {
		t.Errorf("enqueued %v, want only %v", q.enqueued, stale[1].ID)
	}
}

func TestRequeuer_ScanFailureIsNonFatal(t *testing.T) {
	repo := &fakeRepo{staleErr: errors.New("connection reset")}
	q := &fakeEnqueuer{}

	NewRequeuer(repo, q, discard).runOnce(context.Background())

	if len(repo.requeued) != 0 || len(q.enqueued) != 0 {
		t.Error("requeuer acted on a failed scan")
	}
}

func TestRequeuer_StopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		NewRequeuer(&fakeRepo{}, &fakeEnqueuer{}, discard).loop(ctx)
		close(done)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not exit after context cancel")
	}
}

func TestScheduler_EnqueuesDueTasksAndContinuesPastFailures(t *testing.T) {
	due := tasks(3)
	repo := &fakeRepo{due: due}
	q := &fakeEnqueuer{failFor: map[uuid.UUID]bool{due[1].ID: true}}

	NewScheduler(repo, q, discard).runOnce(context.Background())

	if len(q.enqueued) != 2 || q.enqueued[0] != due[0].ID || q.enqueued[1] != due[2].ID {
		t.Errorf("enqueued %v, want tasks 0 and 2", q.enqueued)
	}
}

func TestScheduler_QueryFailureIsNonFatal(t *testing.T) {
	q := &fakeEnqueuer{}
	NewScheduler(&fakeRepo{dueErr: errors.New("timeout")}, q, discard).runOnce(context.Background())

	if len(q.enqueued) != 0 {
		t.Error("scheduler enqueued after a failed query")
	}
}
