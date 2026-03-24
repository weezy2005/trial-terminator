// Package queue handles all Redis interactions: publishing tasks to the queue
// and the background requeue loop that reclaims orphaned tasks.
//
// ARCHITECTURAL DECISION: Why a separate queue package?
// Redis concerns (serialization, queue key names, connection) are completely
// separate from HTTP concerns (handlers) and storage concerns (repository).
// If we later swap Redis for SQS or RabbitMQ, only this package changes.
package queue

import (
	"context"
	"fmt"
	"os"

	"github.com/redis/go-redis/v9"
)

const (
	// TaskQueueKey is the Redis list that acts as our task queue.
	// Workers BRPOP from this list — blocking until a task ID appears.
	//
	// Naming convention: "app:domain:purpose"
	// This makes it easy to find all keys belonging to this app in Redis CLI:
	//   redis-cli KEYS "trial-terminator:*"
	TaskQueueKey = "trial-terminator:tasks:pending"
)

// NewRedisClient creates a Redis client from environment variables.
// We use go-redis/v9 which supports Redis 6+ and has first-class
// context support (every operation is cancellable).
//
// ARCHITECTURAL DECISION: Why not a connection pool config like pgxpool?
// go-redis manages its own internal connection pool automatically.
// The default pool size is 10 * runtime.GOMAXPROCS connections — appropriate
// for most workloads. Unlike Postgres, Redis connections are cheap (no TLS
// handshake, no auth protocol), so we don't need to tune this aggressively.
func NewRedisClient(ctx context.Context) (*redis.Client, error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		addr = "localhost:6379"
	}

	client := redis.NewClient(&redis.Options{
		Addr: addr,

		// DB 0 is the default Redis database. Using DB 0 for prod and
		// DB 1 for tests allows running tests without wiping prod data.
		DB: 0,
	})

	// Verify the connection is alive before returning.
	// Same pattern as our Postgres pool — fail fast at startup, not at runtime.
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("queue: redis ping failed — is redis running? %w", err)
	}

	return client, nil
}
