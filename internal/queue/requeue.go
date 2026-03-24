package queue

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/weezy2005/trial-terminator/internal/repository"
)

const (
	// staleLockThreshold is how long a task can stay IN_PROGRESS with no update
	// before we declare the worker dead and reclaim the task.
	//
	// ARCHITECTURAL DECISION: Why 2 minutes?
	// The worker (Sprint 3) will update locked_at every 30 seconds while running.
	// 2 minutes = 4 missed heartbeats. This is conservative enough to avoid
	// false positives (a slow-but-alive worker) while still recovering quickly
	// from real crashes. In production, tune this based on your p99 task duration.
	staleLockThreshold = 2 * time.Minute

	// requeueInterval is how often the requeue goroutine wakes up and scans for stale tasks.
	requeueInterval = 30 * time.Second
)

// Requeuer is the background goroutine that implements the heartbeat safety net.
// It runs for the lifetime of the application and continuously scans for tasks
// whose workers have died, resetting them to PENDING so they can be retried.
type Requeuer struct {
	repo     repository.TaskRepository
	producer *Producer
	logger   *slog.Logger
}

// NewRequeuer constructs a Requeuer.
func NewRequeuer(repo repository.TaskRepository, client *redis.Client, logger *slog.Logger) *Requeuer {
	return &Requeuer{
		repo:     repo,
		producer: NewProducer(client),
		logger:   logger,
	}
}

// Start launches the requeue loop in a goroutine.
// It returns immediately — the loop runs in the background.
//
// The ctx parameter controls the loop's lifetime. When the server receives
// SIGTERM and calls cancel(), the loop exits cleanly on the next tick.
// This ties the requeue goroutine's lifecycle to the application's graceful
// shutdown — no goroutine leaks.
//
// ARCHITECTURAL DECISION: Why run requeue logic in the API server instead of
// a separate service?
// For this project, simplicity wins — one binary to deploy and monitor.
// At Shopify/Uber scale, you'd run this as a dedicated "janitor" service so it
// can be scaled independently and doesn't compete with API request goroutines.
// The code would be identical; only the deployment changes.
func (r *Requeuer) Start(ctx context.Context) {
	go r.loop(ctx)
}

// loop is the internal implementation — separated so Start can be non-blocking.
func (r *Requeuer) loop(ctx context.Context) {
	r.logger.Info("requeue loop started", "interval", requeueInterval, "threshold", staleLockThreshold)

	// time.NewTicker fires every requeueInterval.
	// Using a ticker (not time.Sleep) is important: if a scan takes 5 seconds,
	// the next scan starts 30 seconds after the previous one *finished*,
	// not 30 seconds after it *started*. This prevents overlapping scans.
	ticker := time.NewTicker(requeueInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Context was cancelled (server shutting down). Exit cleanly.
			r.logger.Info("requeue loop stopped")
			return

		case <-ticker.C:
			// Tick fired — run a scan.
			r.runOnce(ctx)
		}
	}
}

// runOnce performs a single scan-and-requeue cycle.
// Extracted from loop so it can be called in tests without a ticker.
func (r *Requeuer) runOnce(ctx context.Context) {
	staleTasks, err := r.repo.GetStaleInProgressTasks(ctx, staleLockThreshold)
	if err != nil {
		// Log and continue — a failed scan is not fatal. The next tick will retry.
		// ARCHITECTURAL DECISION: Why not crash on scan failure?
		// A temporary DB blip would take down the entire API server if we treated
		// this as fatal. The requeue goroutine is a background best-effort safety
		// net, not a critical path. Log it, alert on it in Grafana (Sprint 4), fix it.
		r.logger.Error("requeue: failed to get stale tasks", "error", err)
		return
	}

	if len(staleTasks) == 0 {
		return // Nothing to do.
	}

	r.logger.Warn("requeue: found stale tasks", "count", len(staleTasks))

	for _, task := range staleTasks {
		// Step 1: Reset the task to PENDING in Postgres.
		if err := r.repo.RequeueTask(ctx, task.ID); err != nil {
			r.logger.Error("requeue: failed to reset task",
				"task_id", task.ID,
				"worker", task.LockedBy,
				"error", err,
			)
			continue // Don't push to Redis if the DB update failed — would create a ghost task.
		}

		// Step 2: Push the task ID back onto the Redis queue.
		// Workers will pick it up on their next BRPOP.
		if err := r.producer.Enqueue(ctx, task.ID); err != nil {
			r.logger.Error("requeue: failed to re-enqueue task",
				"task_id", task.ID,
				"error", err,
			)
			// The task is PENDING in Postgres but not in Redis.
			// This is acceptable — the NEXT requeue scan will catch it again
			// because locked_at is now NULL (set by RequeueTask) and status is PENDING.
			// A future enhancement: a startup recovery scan that pushes all PENDING
			// tasks not in Redis back into the queue.
			continue
		}

		r.logger.Info("requeue: task reclaimed",
			"task_id", task.ID,
			"previous_worker", task.LockedBy,
			"attempts", task.Attempts,
		)
	}
}
