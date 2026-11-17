//go:build integration

package db_test

import (
	"context"
	"io/fs"
	"slices"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/weezy2005/trial-terminator/internal/db"
	"github.com/weezy2005/trial-terminator/internal/pgtest"
	"github.com/weezy2005/trial-terminator/migrations"
)

func recorded(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), "SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatal(err)
		}
		out = append(out, v)
	}
	return out
}

func allVersions(t *testing.T) []string {
	t.Helper()
	names, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i, n := range names {
		names[i] = n[:len(n)-len(".sql")]
	}
	slices.Sort(names)
	return names
}

func TestMigrate_AppliesAllThenNothing(t *testing.T) {
	t.Parallel()
	pool := pgtest.Pool(t)
	ctx := context.Background()

	ran, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if want := allVersions(t); !slices.Equal(ran, want) {
		t.Fatalf("first run applied %v, want %v", ran, want)
	}

	ran, err = db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 0 {
		t.Errorf("second run applied %v, want nothing", ran)
	}
}

// Replicas starting together must not apply a migration twice or fail
// because another replica got there first.
func TestMigrate_ConcurrentStartsApplyEachMigrationOnce(t *testing.T) {
	t.Parallel()
	pool := pgtest.Pool(t)

	const replicas = 5
	var (
		wg    sync.WaitGroup
		mu    sync.Mutex
		total []string
	)
	for i := 0; i < replicas; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ran, err := db.Migrate(context.Background(), pool, migrations.FS)
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			total = append(total, ran...)
			mu.Unlock()
		}()
	}
	wg.Wait()

	slices.Sort(total)
	if want := allVersions(t); !slices.Equal(total, want) {
		t.Errorf("replicas applied %v in total, want each of %v exactly once", total, want)
	}
}

// A database created by Postgres's init directory already has 001 and 002
// but no schema_migrations table. The runner must record them, not re-run
// them (re-running 001 fails on CREATE TYPE).
func TestMigrate_BaselinesDatabaseCreatedByInitDir(t *testing.T) {
	t.Parallel()
	pool := pgtest.Pool(t)
	ctx := context.Background()

	for _, name := range []string{"001_create_tasks.sql", "002_add_scheduled_for.sql"} {
		sql, err := fs.ReadFile(migrations.FS, name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, string(sql)); err != nil {
			t.Fatal(err)
		}
	}

	ran, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatalf("migrating an init-dir database: %v", err)
	}
	for _, v := range ran {
		if v == "001_create_tasks" || v == "002_add_scheduled_for" {
			t.Errorf("re-ran legacy migration %s", v)
		}
	}
	if got, want := recorded(t, pool), allVersions(t); !slices.Equal(got, want) {
		t.Errorf("recorded %v, want %v", got, want)
	}
}

// An init-dir database from before 002 existed has 001 only. The baseline
// must stop there so 002 still gets applied.
func TestMigrate_BaselineStopsAtFirstMissingLegacyMigration(t *testing.T) {
	t.Parallel()
	pool := pgtest.Pool(t)
	ctx := context.Background()

	sql, err := fs.ReadFile(migrations.FS, "001_create_tasks.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(sql)); err != nil {
		t.Fatal(err)
	}

	ran, err := db.Migrate(ctx, pool, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ran, "002_add_scheduled_for") {
		t.Errorf("applied %v, want 002_add_scheduled_for among them", ran)
	}
}

func TestMigrate_FailedMigrationRollsBackAndIsRetried(t *testing.T) {
	t.Parallel()
	pool := pgtest.Pool(t)
	ctx := context.Background()

	files := fstest.MapFS{
		"001_ok.sql":     {Data: []byte("CREATE TABLE a (x INT);")},
		"002_broken.sql": {Data: []byte("CREATE TABLE b (x INT); SELECT no_such_function();")},
	}
	if _, err := db.Migrate(ctx, pool, files); err == nil {
		t.Fatal("expected the broken migration to fail")
	}

	var bExists bool
	if err := pool.QueryRow(ctx, "SELECT to_regclass('b') IS NOT NULL").Scan(&bExists); err != nil {
		t.Fatal(err)
	}
	if bExists {
		t.Error("table b exists: the failed migration was partially applied")
	}
	if got := recorded(t, pool); !slices.Equal(got, []string{"001_ok"}) {
		t.Errorf("recorded %v, want only 001_ok", got)
	}

	files["002_broken.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE b (x INT);")}
	ran, err := db.Migrate(ctx, pool, files)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []string{"002_broken"}) {
		t.Errorf("retry applied %v, want only 002_broken", ran)
	}
}
