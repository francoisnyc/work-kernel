package itemservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

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

// ListTypes returns every registered Type version, parsed, ordered by Type ID
// and then version. Types are global for now (docs/journal.md, Later: Types
// per namespace) and readable by anyone in a namespace: a definition holds no
// Item data, so this read is exempt from policy.Project.
func (k *Service) ListTypes(ctx context.Context) ([]*typedef.Type, error) {
	rows, err := k.pool.Query(ctx, `select id, version from types order by id, version`)
	if err != nil {
		return nil, fmt.Errorf("list types: %w", err)
	}
	// CollectRows reads every row and closes them, like a try-with-resources.
	type key struct {
		id      string
		version int
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (key, error) {
		var kk key
		err := r.Scan(&kk.id, &kk.version)
		return kk, err
	})
	if err != nil {
		return nil, fmt.Errorf("list types: %w", err)
	}
	types := make([]*typedef.Type, 0, len(keys))
	for _, kk := range keys {
		t, err := k.loadType(ctx, k.pool, kk.id, kk.version)
		if err != nil {
			return nil, err
		}
		types = append(types, t)
	}
	return types, nil
}

// namespaceName is what a namespace may be called: lower-case letters,
// digits and dashes, starting with a letter or digit. It ends up in resource
// names (namespaces/{ns}/items/...).
var namespaceName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

// ValidNamespace reports whether ns is a well-formed namespace name.
func ValidNamespace(ns string) bool { return namespaceName.MatchString(ns) }

// EnsureNamespace makes the namespace exist: it gets its one root scope, the
// scope every principal of the namespace reads. Doing it again does nothing.
func (k *Service) EnsureNamespace(ctx context.Context, ns string) error {
	if !ValidNamespace(ns) {
		return fmt.Errorf("%w: namespace %q must be lower-case letters, digits and dashes", ErrInvalid, ns)
	}
	// The partial unique index scopes_root allows one parentless scope per
	// namespace; ON CONFLICT names it by its columns and predicate.
	_, err := k.pool.Exec(ctx,
		`insert into scopes (id, namespace_id, parent_id, narrows) values (gen_random_uuid(), $1, null, false)
		 on conflict (namespace_id) where parent_id is null do nothing`, ns)
	if err != nil {
		return fmt.Errorf("ensure namespace %s: %w", ns, err)
	}
	return nil
}
