package typedef

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// ParseDir reads every .yaml file in dir as one Type version, and returns
// them ordered by Type ID, then version. A file that does not parse stops
// the load with an error naming it; so does a Type version declared twice.
// Other files (a README) are skipped.
func ParseDir(dir string) ([]*Type, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("typedef: read %s: %w", dir, err)
	}
	var types []*Type
	from := map[string]string{} // "id@version" -> the file that declared it
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("typedef: %w", err)
		}
		t, err := Parse(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		key := fmt.Sprintf("%s@%d", t.ID, t.Version)
		if first, dup := from[key]; dup {
			return nil, fmt.Errorf("typedef: %s v%d is declared by both %s and %s", t.ID, t.Version, first, path)
		}
		from[key] = path
		types = append(types, t)
	}
	// Links name other Types, and keep their inverses across versions:
	// both are only known once every file is read.
	if err := CheckLinkTargets(types); err != nil {
		return nil, err
	}
	if err := CheckLinkInverses(types); err != nil {
		return nil, err
	}
	// Reactions and link resolution name other Types too.
	if err := CheckReactions(types); err != nil {
		return nil, err
	}
	// cmp.Or picks the first non-zero comparison: by ID, then by version.
	slices.SortFunc(types, func(a, b *Type) int {
		return cmp.Or(strings.Compare(a.ID, b.ID), cmp.Compare(a.Version, b.Version))
	})
	return types, nil
}
