package story

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Env is what runs share: the database, with every Type of a Types folder
// registered, and the action contracts.
type Env struct {
	pool    *pgxpool.Pool
	types   []*typedef.Type
	byName  map[string]*typedef.Type // id@version
	catalog *contracts.Catalog
}

// NewEnv registers every Type of typesDir (read as the item service reads
// its folder) in the database behind pool.
func NewEnv(ctx context.Context, pool *pgxpool.Pool, typesDir string) (*Env, error) {
	types, err := typedef.ParseDir(typesDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", typesDir, err)
	}
	k := itemservice.New(pool)
	e := &Env{pool: pool, types: types, byName: map[string]*typedef.Type{}, catalog: contracts.Default()}
	for _, ty := range types {
		if err := k.RegisterType(ctx, ty); err != nil {
			return nil, fmt.Errorf("register %s@%d: %w", ty.ID, ty.Version, err)
		}
		e.byName[fmt.Sprintf("%s@%d", ty.ID, ty.Version)] = ty
	}
	return e, nil
}

// Types are the Types a story may use.
func (e *Env) Types() []*typedef.Type { return e.types }

// Catalog is the action contracts fakes are held to.
func (e *Env) Catalog() *contracts.Catalog { return e.catalog }
