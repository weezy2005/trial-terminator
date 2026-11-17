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
	"github.com/weezy2005/trial-terminator/internal/queue"
	"github.com/weezy2005/trial-terminator/internal/server"
	"github.com/weezy2005/trial-terminator/migrations"
)

func main() {
	_ = godotenv.Load()

	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	// The root context is cancelled when a shutdown signal arrives.
	// We pass this context to server.New() so the requeue goroutine
	// shuts down alongside the HTTP server — no goroutine leaks.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger.Info("connecting to database...")
	pool, err := db.NewPool(ctx)
	if err != nil {
		logger.Error("failed to connect to database", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	logger.Info("database connection established")

	// Apply pending migrations before serving traffic, so no handler ever
	// runs against a schema older than the code expects.
	applied, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		logger.Error("failed to apply migrations", "error", err)
		os.Exit(1)
	}
	logger.Info("schema up to date", "applied", applied)

	logger.Info("connecting to redis...")
	redisClient, err := queue.NewRedisClient(ctx)
	if err != nil {
		logger.Error("failed to connect to redis", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()
	logger.Info("redis connection established")

	srv := server.New(ctx, pool, redisClient)

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		logger.Info("server starting", "addr", srv.Addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	logger.Info("shutdown signal received")

	// Cancel the root context — stops the requeue goroutine.
	cancel()

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.Error("forced shutdown", "error", err)
	}

	logger.Info("server stopped cleanly")
}
