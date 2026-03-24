package queue

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Producer publishes task IDs to the Redis queue for workers to consume.
//
// ARCHITECTURAL DECISION: Why publish only the task ID, not the full task?
// Sending the full task payload in Redis would create two sources of truth.
// If a worker reads a task from Redis and the DB has since been updated
// (e.g., status changed to CANCELLED), the worker acts on stale data.
// Publishing only the ID forces the worker to fetch the latest state from
// Postgres before doing any work. Redis is the *notification* channel;
// Postgres is the *data* channel.
type Producer struct {
	client *redis.Client
}

// NewProducer constructs a Producer.
func NewProducer(client *redis.Client) *Producer {
	return &Producer{client: client}
}

// Enqueue pushes a task ID onto the left side of the pending queue list.
//
// Redis data structure choice: LIST
// We use LPUSH (push to head) + BRPOP (pop from tail) = FIFO queue.
// This gives us first-in-first-out ordering — tasks are processed in the
// order they were created, which is the fairest default behavior.
//
// Why not Redis Streams or Pub/Sub?
// - Pub/Sub: fire-and-forget. If no worker is listening, the message is lost.
// - Streams: more powerful (consumer groups, message acknowledgement) but
//   adds complexity. We get equivalent reliability from our Postgres heartbeat
//   pattern — the requeue goroutine is our acknowledgement safety net.
// - LIST + BRPOP: simple, battle-tested, sufficient for our needs.
//   Upgrade to Streams in a future sprint if throughput demands it.
func (p *Producer) Enqueue(ctx context.Context, taskID uuid.UUID) error {
	// LPUSH pushes the task ID string to the HEAD of the list.
	// The value is the UUID as a string — workers parse it back to uuid.UUID.
	if err := p.client.LPush(ctx, TaskQueueKey, taskID.String()).Err(); err != nil {
		return fmt.Errorf("producer: failed to enqueue task %s: %w", taskID, err)
	}
	return nil
}
