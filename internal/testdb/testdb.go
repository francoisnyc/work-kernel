// Package testdb starts a throwaway Postgres with the real migrations applied.
// It is for tests and cmd/bench; internal/itemservice's tests predate it and have their own copy.
package testdb

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver for database/sql (used by goose)
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/francoisnyc/work-kernel/migrations"
)

// Start returns a pool connected to a fresh, migrated database, and a
// function that shuts everything down. Call it from TestMain.
func Start(ctx context.Context) (*pgxpool.Pool, func(), error) {
	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("kernel"),
		postgres.WithUsername("kernel"),
		postgres.WithPassword("kernel"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, nil, fmt.Errorf("start postgres: %w", err)
	}
	stop := func() { _ = testcontainers.TerminateContainer(pg) }

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("connection string: %w", err)
	}
	if err := migrate(ctx, dsn); err != nil {
		stop()
		return nil, nil, fmt.Errorf("migrate: %w", err)
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		stop()
		return nil, nil, fmt.Errorf("connect: %w", err)
	}
	return pool, func() { pool.Close(); stop() }, nil
}

func migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}
