package db

import (
	"context"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockKey identifies this app's migration lock among all advisory
// locks in the database. The value is arbitrary; it only has to be stable
// and not collide with other users of advisory locks on the same server.
const migrationLockKey int64 = 0x7472_6961_6c74 // "trialt"

// Migrate applies every *.sql file in files that hasn't been applied yet,
// in filename order, and returns the versions it applied.
//
// ARCHITECTURAL DECISION: Why run migrations from the API at startup?
// Postgres's docker-entrypoint-initdb.d only runs for a brand-new volume, so
// a new migration never reached an existing database, and managed databases
// like Cloud SQL have no init directory at all. Embedding the files in the
// binary means the code and the schema it expects always ship together.
//
// Several replicas can start at the same moment. A session-level advisory
// lock makes exactly one of them apply migrations while the rest wait; when
// they get the lock, everything is already recorded and they do nothing.
//
// Each migration runs in its own transaction together with the row that
// records it, so a failure leaves the schema at the previous version, never
// half-applied, and the next start retries it.
func Migrate(ctx context.Context, pool *pgxpool.Pool, files fs.FS) ([]string, error) {
	// Advisory locks belong to a session, so lock, migrate and unlock must
	// all happen on the same connection rather than whatever the pool hands out.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return nil, fmt.Errorf("db: acquire migration lock: %w", err)
	}
	// Background context: the unlock must run even if ctx was cancelled,
	// or the lock stays held until this pooled connection is closed.
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey)

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version    TEXT        PRIMARY KEY,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		)`); err != nil {
		return nil, fmt.Errorf("db: create schema_migrations: %w", err)
	}

	applied, err := appliedVersions(ctx, conn)
	if err != nil {
		return nil, err
	}
	if len(applied) == 0 {
		if err := baselineLegacy(ctx, conn, applied); err != nil {
			return nil, err
		}
	}

	names, err := fs.Glob(files, "*.sql")
	if err != nil {
		return nil, fmt.Errorf("db: list migrations: %w", err)
	}
	sort.Strings(names)

	var ran []string
	for _, name := range names {
		version := strings.TrimSuffix(name, ".sql")
		if applied[version] {
			continue
		}
		sql, err := fs.ReadFile(files, name)
		if err != nil {
			return ran, fmt.Errorf("db: read migration %s: %w", name, err)
		}
		err = pgx.BeginFunc(ctx, conn, func(tx pgx.Tx) error {
			// No arguments, so pgx uses the simple protocol, which allows
			// a file to contain many statements.
			if _, err := tx.Exec(ctx, string(sql)); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version)
			return err
		})
		if err != nil {
			return ran, fmt.Errorf("db: apply migration %s: %w", name, err)
		}
		ran = append(ran, version)
	}
	return ran, nil
}

func appliedVersions(ctx context.Context, conn *pgxpool.Conn) (map[string]bool, error) {
	rows, err := conn.Query(ctx, "SELECT version FROM schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}
	versions, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}
	applied := make(map[string]bool, len(versions))
	for _, v := range versions {
		applied[v] = true
	}
	return applied, nil
}

// legacyMigrations covers databases created before this runner existed.
// Postgres's init directory applied these files on first start and nothing
// recorded them, so re-running 001 would fail on "type task_status already
// exists". Each probe reports whether that migration's effect is present.
// The list is closed: every migration after these is tracked from the start.
var legacyMigrations = []struct{ version, probe string }{
	{"001_create_tasks", `SELECT to_regclass('tasks') IS NOT NULL`},
	{"002_add_scheduled_for", `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = current_schema()
			AND   table_name   = 'tasks'
			AND   column_name  = 'scheduled_for'
		)`},
}

// baselineLegacy records legacy migrations whose effects already exist,
// stopping at the first one that's missing so the rest apply normally.
func baselineLegacy(ctx context.Context, conn *pgxpool.Conn, applied map[string]bool) error {
	for _, m := range legacyMigrations {
		var present bool
		if err := conn.QueryRow(ctx, m.probe).Scan(&present); err != nil {
			return fmt.Errorf("db: probe legacy migration %s: %w", m.version, err)
		}
		if !present {
			return nil
		}
		if _, err := conn.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", m.version); err != nil {
			return fmt.Errorf("db: baseline legacy migration %s: %w", m.version, err)
		}
		applied[m.version] = true
	}
	return nil
}
