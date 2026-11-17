//go:build integration

// Package pgtest gives integration tests an isolated Postgres schema each,
// so tests run in parallel against one database without seeing each other's
// rows. Only compiled with -tags=integration.
package pgtest

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/weezy2005/trial-terminator/internal/db"
	"github.com/weezy2005/trial-terminator/migrations"
)

// Pool returns a pool whose connections all use a fresh, empty schema that
// is dropped when the test ends. Skips the test if TEST_DATABASE_URL is unset.
func Pool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()

	schema := "test_" + uuid.NewString()[:8]
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 25 // enough for the concurrency tests to actually race
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// MigratedPool is Pool with the real migrations applied through the same
// runner the API uses at startup, so tests and production can't drift.
func MigratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := Pool(t)
	if _, err := db.Migrate(context.Background(), pool, migrations.FS); err != nil {
		t.Fatal(err)
	}
	return pool
}
