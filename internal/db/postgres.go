// Package db handles database connection setup and lifecycle.
// It owns one concern: giving the rest of the application a ready-to-use
// connection pool. Nothing else lives here.
package db

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// NewPool creates a PostgreSQL connection pool from environment variables.
//
// ARCHITECTURAL DECISION: Why a connection pool instead of a single connection?
// A connection pool keeps N connections open and reuses them.
// Opening a TCP connection + TLS handshake + Postgres auth takes ~10ms.
// Under load, creating a new connection per request would add 10ms to every
// API call and exhaust the DB's connection limit fast (Postgres default: 100).
// pgxpool manages this automatically — it opens connections lazily, reuses them,
// and handles reconnection if a connection drops.
//
// ARCHITECTURAL DECISION: Why read config from env vars, not a config struct?
// The Twelve-Factor App methodology (used at Shopify, Uber, etc.) mandates
// storing config in the environment. This means:
// - Local dev: .env file loaded by godotenv
// - Docker:    environment block in docker-compose.yml
// - Prod:      Kubernetes Secrets / AWS Parameter Store
// The application code is identical in all environments.
func NewPool(ctx context.Context) (*pgxpool.Pool, error) {
	dsn := buildDSN()

	// pgxpool.ParseConfig parses the DSN and returns a config struct.
	// We use this two-step approach (parse, then configure, then connect)
	// so we can set pool-specific options like MaxConns before dialing.
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: failed to parse dsn: %w", err)
	}

	// MaxConns: the maximum number of connections in the pool.
	// Rule of thumb: (number of CPU cores * 2) + number of disk spindles.
	// For a dev/staging environment, 10 is safe.
	config.MaxConns = 10

	// MinConns: keep at least 2 connections warm so the first requests
	// after a cold start don't pay the connection setup cost.
	config.MinConns = 2

	// MaxConnIdleTime: close connections that have been idle for more than 5 minutes.
	// This prevents the pool from holding open connections that the DB has
	// already timed out on its end (which causes "connection reset by peer" errors).
	config.MaxConnIdleTime = 5 * time.Minute

	// HealthCheckPeriod: pgxpool pings idle connections on this interval.
	// This catches stale connections before your application tries to use them.
	config.HealthCheckPeriod = 1 * time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("db: failed to connect: %w", err)
	}

	// Verify the connection is actually alive before returning.
	// Without this ping, the pool "connects" lazily — errors only appear
	// when the first query runs, which is much harder to debug at startup.
	if err := pool.Ping(ctx); err != nil {
		return nil, fmt.Errorf("db: ping failed — is postgres running? %w", err)
	}

	return pool, nil
}

// buildDSN assembles the PostgreSQL connection string from environment variables.
// Using individual env vars (not a single DATABASE_URL) is more portable:
// some orchestration platforms inject host/port/user separately.
func buildDSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s?sslmode=disable",
		getEnv("POSTGRES_USER", "trialterminator"),
		getEnv("POSTGRES_PASSWORD", "secret"),
		getEnv("POSTGRES_HOST", "localhost"),
		getEnv("POSTGRES_PORT", "5432"),
		getEnv("POSTGRES_DB", "trialterminator"),
	)
}

// getEnv returns the value of an environment variable or a fallback default.
// This pattern is idiomatic Go for optional config — it keeps the code readable
// and avoids nil pointer panics from os.LookupEnv in the hot path.
func getEnv(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}
