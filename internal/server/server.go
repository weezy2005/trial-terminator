// Package server wires together the HTTP router with its handlers.
// This is the "composition root" — the place where all dependencies
// are assembled into a working application.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/weezy2005/trial-terminator/internal/handlers"
	"github.com/weezy2005/trial-terminator/internal/queue"
	"github.com/weezy2005/trial-terminator/internal/repository"
)

// New constructs and returns a configured *http.Server.
// ctx is used to bind the requeue goroutine's lifetime to the application's
// shutdown signal — when ctx is cancelled, the goroutine exits cleanly.
func New(ctx context.Context, db *pgxpool.Pool, redisClient *redis.Client) *http.Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Build the dependency chain bottom-up.
	taskRepo := repository.NewPostgresTaskRepo(db)
	producer := queue.NewProducer(redisClient)
	taskHandler := handlers.NewTaskHandler(taskRepo, producer, logger)

	// Start the requeue goroutine — it runs for the lifetime of ctx.
	// When main.go cancels ctx on SIGTERM, this goroutine exits on its next tick.
	requeuer := queue.NewRequeuer(taskRepo, redisClient, logger)
	requeuer.Start(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("POST /tasks", taskHandler.CreateTask)
	mux.HandleFunc("GET /tasks/{id}", taskHandler.GetTask)
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	port := os.Getenv("SERVER_PORT")
	if port == "" {
		port = "8080"
	}

	return &http.Server{
		Addr:    ":" + port,
		Handler: mux,
	}
}
