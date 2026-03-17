// Package server wires together the HTTP router with its handlers.
// This is the "composition root" — the place where all dependencies
// are assembled into a working application.
//
// ARCHITECTURAL DECISION: Why a separate server package?
// main.go should be thin: parse config, call server.New(), call server.Run().
// If you put routing in main.go, you can't test the routing without
// running the whole application. A separate server package lets you
// instantiate the HTTP server in tests with a test DB pool.
package server

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/weezy2005/trial-terminator/internal/handlers"
	"github.com/weezy2005/trial-terminator/internal/repository"
)

// New constructs and returns a configured *http.Server.
// All routes are registered here. When you add a new feature in Sprint 2+,
// you add a handler and register its routes here — nothing else changes.
func New(db *pgxpool.Pool) *http.Server {
	// ARCHITECTURAL DECISION: Why slog (structured logging)?
	// slog is Go 1.21's built-in structured logger. Structured logs emit JSON
	// (or key=value pairs) instead of plain strings. This means log aggregation
	// tools (Datadog, Grafana Loki, Splunk) can index and query individual fields.
	// Example: find all errors for a specific service_name in the last hour.
	// You can't do that with fmt.Println("error for " + serviceName).
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	// Dependency injection: build the chain bottom-up.
	// repo depends on db; handler depends on repo.
	// main.go doesn't need to know this chain exists.
	taskRepo := repository.NewPostgresTaskRepo(db)
	taskHandler := handlers.NewTaskHandler(taskRepo, logger)

	// Go 1.22 ServeMux supports:
	// - Method-specific routes: "POST /tasks"
	// - Path parameters: "GET /tasks/{id}"
	// No third-party router (gorilla/mux, chi) needed for this project.
	// ARCHITECTURAL DECISION: Avoiding unnecessary dependencies keeps the
	// binary small and the dependency graph clean. Add a router library
	// only when you need middleware chaining or complex parameter extraction.
	mux := http.NewServeMux()

	mux.HandleFunc("POST /tasks", taskHandler.CreateTask)
	mux.HandleFunc("GET /tasks/{id}", taskHandler.GetTask)

	// Health check endpoint — required for Docker/Kubernetes liveness probes.
	// A load balancer needs to know if this instance is ready to serve traffic.
	// Returning 200 {} is the standard contract.
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
