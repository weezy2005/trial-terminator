// main is the entry point for the TrialTerminator API server.
// It should do exactly three things:
//   1. Load configuration
//   2. Wire up dependencies (DB pool)
//   3. Start the server
//
// If main.go grows beyond ~50 lines, that's a sign business logic has leaked in.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/weezy2005/trial-terminator/internal/db"
	"github.com/weezy2005/trial-terminator/internal/server"
)

func main() {
	// Load .env file in development. In production (Docker), real env vars
	// are set directly — godotenv.Load() is a no-op if the file doesn't exist,
	// which is exactly the behavior we want.
	_ = godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// Use a background context for startup. We'll switch to a cancellable
	// context when we handle shutdown signals below.
	ctx := context.Background()

	logger.Info("connecting to database...")
	pool, err := db.NewPool(ctx)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	logger.Info("database connection established")

	srv := server.New(pool)

	// Graceful shutdown: handle SIGINT (Ctrl+C) and SIGTERM (Docker stop).
	//
	// ARCHITECTURAL DECISION: Why graceful shutdown?
	// When Kubernetes rolls out a new version, it sends SIGTERM to the old pod.
	// Without graceful shutdown, in-flight HTTP requests are killed mid-flight —
	// the client gets a connection reset error. With graceful shutdown:
	// 1. Stop accepting new connections
	// 2. Wait up to 15 seconds for in-flight requests to finish
	// 3. Exit cleanly
	// This is the difference between a 0-downtime deploy and a 500-error spike.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	// Start the server in a goroutine so it doesn't block the shutdown logic below.
	go func() {
		logger.Info("server starting", "port", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	// Block until we receive a shutdown signal.
	<-quit
	logger.Info("shutdown signal received")

	// Give in-flight requests 15 seconds to complete.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("forced shutdown", "error", err)
	}

	logger.Info("server stopped cleanly")
}
