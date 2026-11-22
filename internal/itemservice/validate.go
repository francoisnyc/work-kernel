package itemservice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// ValidateTypes checks draft Types against every registered version
// (typedef.Check) and writes nothing (Ventoux V2b). A draft's problems are
// the result, not an error: the error is for the kernel's own failures (the
// database, an unreadable registered definition). A problem on a registered
// version has no Source; the caller names it. Like ListTypes, it reads no
// Items, so it is exempt from policy.Project (a Type definition holds no
// Item data).
func (k *Service) ValidateTypes(ctx context.Context, sources []typedef.Source) (typedef.Problems, error) {
	// The stored definitions as they are: "identical" is decided against
	// them, as RegisterType decides it, not against a re-encoding.
	rows, err := k.pool.Query(ctx, `select definition from types order by id, version`)
	if err != nil {
		return nil, fmt.Errorf("read types: %w", err)
	}
	defs, err := pgx.CollectRows(rows, pgx.RowTo[[]byte])
	if err != nil {
		return nil, fmt.Errorf("read types: %w", err)
	}
	_, err = typedef.Check(sources, defs)
	var ps typedef.Problems
	switch {
	case err == nil:
		return nil, nil
	case errors.As(err, &ps):
		return ps, nil
	default:
		return nil, err
	}
}
