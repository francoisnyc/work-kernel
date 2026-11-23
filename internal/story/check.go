package story

import (
	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Check holds a story against the Types it may use and the action
// contracts, before it runs: its people, Items, Types, fields, states and
// values; every fake answer against its action's contract; a fake for
// every action its Types' hooks may call. It returns every problem found.
func Check(s *Story, types []*typedef.Type, catalog *contracts.Catalog) Problems {
	_, _, _ = s, types, catalog
	return Problems{{Message: errNotBuilt.Error()}}
}
