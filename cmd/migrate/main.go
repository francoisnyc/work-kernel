// Command migrate applies the goose migrations compiled into it (the files
// in migrations/, embedded) to DATABASE_URL, then exits. The compose stack
// runs it once; the other services wait for it to finish successfully.
// DATABASE_URL has no default: the stack must say where the database is.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver for database/sql (used by goose)
	"github.com/pressly/goose/v3"

	"github.com/francoisnyc/work-kernel/migrations"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stderr); err != nil {
		slog.Error("migrate failed", "error", err)
		os.Exit(1)
	}
}

// run applies every pending migration and says what it applied on stderr.
func run(ctx context.Context, getenv func(string) string, stderr io.Writer) error {
	dsn := getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	// goose's Provider takes the migrations as an fs.FS: the embedded files,
	// so the binary needs nothing on disk.
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return err
	}
	results, err := provider.Up(ctx)
	if err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for _, r := range results {
		fmt.Fprintf(stderr, "applied %s (%s)\n", r.Source.Path, r.Duration.Round(1e6))
	}
	version, err := provider.GetDBVersion(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(stderr, "database at version %d (%d applied now)\n", version, len(results))
	return nil
}
