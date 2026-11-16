// Tests for Step 0, part 3: cmd/migrate, the compose stack's one-shot
// migration service. It applies the embedded migrations (the same files
// make migrate and the tests use) to DATABASE_URL and exits; running it
// again applies nothing and still succeeds; a database it cannot reach is
// an error, so the services waiting on it never start.
package main

import (
	"bytes"
	"context"
	"database/sql"
	"io/fs"
	"os"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/francoisnyc/work-kernel/internal/testdb"
	"github.com/francoisnyc/work-kernel/migrations"
)

var dsn string

func TestMain(m *testing.M) {
	url, stop, err := testdb.StartEmpty(context.Background())
	if err != nil {
		println("start database:", err.Error())
		os.Exit(1)
	}
	dsn = url
	code := m.Run()
	stop()
	os.Exit(code)
}

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestRun_AppliesEveryMigrationThenNothing(t *testing.T) {
	files, err := fs.Glob(migrations.FS, "*.sql")
	if err != nil || len(files) == 0 {
		t.Fatalf("embedded migrations: %v, %d files", err, len(files))
	}
	var stderr bytes.Buffer
	if err := run(context.Background(), env(map[string]string{"DATABASE_URL": dsn}), &stderr); err != nil {
		t.Fatalf("first run: %v; stderr %q", err, stderr.String())
	}

	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var applied int
	if err := db.QueryRow(`select count(*) from goose_db_version where version_id > 0 and is_applied`).Scan(&applied); err != nil {
		t.Fatalf("read goose_db_version: %v", err)
	}
	if applied != len(files) {
		t.Errorf("migrations applied = %d, want %d", applied, len(files))
	}
	var items bool
	if err := db.QueryRow(`select to_regclass('items') is not null`).Scan(&items); err != nil || !items {
		t.Errorf("table items exists = %v, %v; want true", items, err)
	}

	if err := run(context.Background(), env(map[string]string{"DATABASE_URL": dsn}), &stderr); err != nil {
		t.Fatalf("second run: %v", err)
	}
	var rows int
	if err := db.QueryRow(`select count(*) from goose_db_version where version_id > 0`).Scan(&rows); err != nil || rows != len(files) {
		t.Errorf("goose_db_version rows after a second run = %d, %v; want %d", rows, err, len(files))
	}
}

func TestRun_UnreachableDatabaseFails(t *testing.T) {
	const nowhere = "postgres://kernel:kernel@127.0.0.1:1/kernel?sslmode=disable&connect_timeout=1"
	if err := run(context.Background(), env(map[string]string{"DATABASE_URL": nowhere}), &bytes.Buffer{}); err == nil {
		t.Error("run against an unreachable database succeeded")
	}
}

func TestRun_RequiresDatabaseURL(t *testing.T) {
	err := run(context.Background(), env(nil), &bytes.Buffer{})
	if err == nil {
		t.Error("run without DATABASE_URL succeeded; the stack must say where the database is")
	}
}
