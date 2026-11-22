package itemservice

import (
	"context"
	"errors"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// ValidateTypes checks draft Types against every registered version and
// writes nothing (Ventoux V2b). Stub: not implemented yet.
func (k *Service) ValidateTypes(ctx context.Context, sources []typedef.Source) (typedef.Problems, error) {
	return nil, errors.New("ValidateTypes: not implemented")
}
