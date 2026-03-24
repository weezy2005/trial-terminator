package server

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/weezy2005/trial-terminator/internal/handlers"
	"github.com/weezy2005/trial-terminator/internal/metrics"
	"github.com/weezy2005/trial-terminator/internal/queue"
	"github.com/weezy2005/trial-terminator/internal/repository"
)

func New(ctx context.Context, db *pgxpool.Pool, redisClient *redis.Client) *http.Server {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	taskRepo := repository.NewPostgresTaskRepo(db)
	producer := queue.NewProducer(redisClient)
	taskHandler := handlers.NewTaskHandler(taskRepo, producer, logger)

	requeuer := queue.NewRequeuer(taskRepo, redisClient, logger)
	requeuer.Start(ctx)

	mux := http.NewServeMux()

	// Wrap each route with InstrumentHandler so every request records its
	// latency and status code as a Prometheus histogram observation.
	// The path string passed to InstrumentHandler becomes the "path" label —
	// it must be the PATTERN, not the actual URL, to avoid cardinality explosion.
	mux.HandleFunc("POST /tasks", metrics.InstrumentHandler("POST /tasks", taskHandler.CreateTask))
	mux.HandleFunc("GET /tasks/{id}", metrics.InstrumentHandler("GET /tasks/{id}", taskHandler.GetTask))

	// /metrics is the Prometheus scrape endpoint.
	// promhttp.Handler() serves all registered metrics in the Prometheus text format.
	// Prometheus scrapes this URL every 15 seconds (configured in prometheus.yml).
	//
	// ARCHITECTURAL DECISION: Why expose metrics on the same port as the API?
	// Simple deployments (one container, one port) are easier to manage.
	// In production you'd often expose metrics on a separate internal port
	// (e.g. :9090) so the /metrics endpoint is never publicly reachable.
	// For this project, same port is fine.
	mux.Handle("GET /metrics", promhttp.Handler())

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
