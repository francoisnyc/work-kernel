package itemservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// RegisterType stores one version of a Type. Registering the same definition
// again does nothing. A different definition under the same (id, version) is
// refused: Items are pinned to the version they were created with, so a
// version must never change. Publish a new version instead.
func (k *Service) RegisterType(ctx context.Context, t *typedef.Type) error {
	def, err := json.Marshal(t)
	if err != nil {
		return fmt.Errorf("encode type: %w", err)
	}
	var same bool
	err = pgx.BeginFunc(ctx, k.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`insert into types (id, version, definition) values ($1, $2, $3::jsonb)
			 on conflict (id, version) do nothing`,
			t.ID, t.Version, string(def))
		if err != nil {
			return fmt.Errorf("insert type: %w", err)
		}
		// jsonb equality ignores key order and whitespace.
		return tx.QueryRow(ctx,
			`select definition = $3::jsonb from types where id = $1 and version = $2`,
			t.ID, t.Version, string(def)).Scan(&same)
	})
	if err != nil {
		return err
	}
	if !same {
		return fmt.Errorf("%w: type %s v%d is already registered with a different definition",
			ErrInvalid, t.ID, t.Version)
	}
	return nil
}

// querier is satisfied by both the pool and a transaction. Go interfaces are
// implicit: a type fits if it has the methods, with no "implements" clause.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// loadType returns a registered Type, from the cache when it is enabled.
func (k *Service) loadType(ctx context.Context, q querier, id string, version int) (*typedef.Type, error) {
	read := func() (*typedef.Type, error) { return readType(ctx, q, id, version) }
	if k.types == nil {
		return read()
	}
	return k.types.Load(id, version, read)
}

// readType reads a registered Type from Postgres and parses it.
func readType(ctx context.Context, q querier, id string, version int) (*typedef.Type, error) {
	var def []byte
	err := q.QueryRow(ctx,
		`select definition from types where id = $1 and version = $2`, id, version).Scan(&def)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("%w: type %s v%d is not registered", ErrInvalid, id, version)
	}
	if err != nil {
		return nil, fmt.Errorf("read type: %w", err)
	}
	return typedef.FromJSON(def)
}
