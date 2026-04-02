package queue

import (
	"context"
	"log/slog"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/weezy2005/trial-terminator/internal/repository"
)

const schedulerInterval = 1 * time.Minute

// Scheduler is a background goroutine that enqueues scheduled tasks when their
// scheduled_for time arrives. It runs a poll every minute and pushes any due
// tasks into Redis so a worker picks them up.
//
// ARCHITECTURAL DECISION: Why poll Postgres instead of a cron or Redis sorted set?
// Postgres is already our source of truth for task state. A separate cron or
// sorted-set approach would introduce a second system that can disagree with the DB.
// Polling every minute is fine for "cancel before my card is charged" use cases —
// the granularity users care about is hours/days, not seconds.
type Scheduler struct {
	repo     repository.TaskRepository
	producer *Producer
	logger   *slog.Logger
}

// NewScheduler constructs a Scheduler.
func NewScheduler(repo repository.TaskRepository, client *redis.Client, logger *slog.Logger) *Scheduler {
	return &Scheduler{
		repo:     repo,
		producer: NewProducer(client),
		logger:   logger,
	}
}

// Start launches the scheduler loop in a background goroutine.
// It returns immediately. The loop runs until ctx is cancelled (server shutdown).
func (s *Scheduler) Start(ctx context.Context) {
	go s.loop(ctx)
}

func (s *Scheduler) loop(ctx context.Context) {
	s.logger.Info("scheduler loop started", "interval", schedulerInterval)

	ticker := time.NewTicker(schedulerInterval)
	defer ticker.Stop()

	// Run once immediately on startup so any tasks that came due while the
	// server was down are picked up within seconds, not up to a minute later.
	s.runOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			s.logger.Info("scheduler loop stopped")
			return
		case <-ticker.C:
			s.runOnce(ctx)
		}
	}
}

// runOnce fetches all due scheduled tasks and enqueues them.
func (s *Scheduler) runOnce(ctx context.Context) {
	tasks, err := s.repo.GetPendingScheduledTasks(ctx)
	if err != nil {
		s.logger.Error("scheduler: failed to query due tasks", "error", err)
		return
	}
	if len(tasks) == 0 {
		return
	}

	s.logger.Info("scheduler: enqueueing due tasks", "count", len(tasks))

	for _, task := range tasks {
		if err := s.producer.Enqueue(ctx, task.ID); err != nil {
			s.logger.Error("scheduler: failed to enqueue task",
				"task_id", task.ID,
				"error", err,
			)
			continue
		}
		s.logger.Info("scheduler: task enqueued",
			"task_id", task.ID,
			"service", task.ServiceName,
			"scheduled_for", task.ScheduledFor,
		)
	}
}
