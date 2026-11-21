package typedef

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// typeFiles lists the .yaml files of dir and of its subfolders, one level
// deep: a subfolder is a package of Types with its README. IDs stay global.
// A folder two levels down is an error (a Type nobody loads is a trap);
// folders whose name starts with a dot are skipped, as are other files.
func typeFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("typedef: read %s: %w", dir, err)
	}
	var files []string
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		switch {
		case strings.HasPrefix(e.Name(), "."):
		case e.IsDir():
			inside, err := os.ReadDir(path)
			if err != nil {
				return nil, fmt.Errorf("typedef: read %s: %w", path, err)
			}
			for _, f := range inside {
				switch sub := filepath.Join(path, f.Name()); {
				case strings.HasPrefix(f.Name(), "."):
				case f.IsDir():
					return nil, fmt.Errorf("typedef: %s: Types are read from a folder and its subfolders, one level deep", sub)
				case strings.HasSuffix(f.Name(), ".yaml"):
					files = append(files, sub)
				}
			}
		case strings.HasSuffix(e.Name(), ".yaml"):
			files = append(files, path)
		}
	}
	return files, nil
}

// ParseDir reads every .yaml file in dir, and in its subfolders one level
// deep, as one Type version, and returns them ordered by Type ID, then
// version. A file that does not parse stops the load with an error naming
// it; so does a Type version declared twice. Other files (a README) are
// skipped.
func ParseDir(dir string) ([]*Type, error) {
	files, err := typeFiles(dir)
	if err != nil {
		return nil, err
	}
	var types []*Type
	from := map[string]string{} // "id@version" -> the file that declared it
	for _, path := range files {
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
	// So do incoming declarations: the links other Types declare.
	if err := CheckIncoming(types); err != nil {
		return nil, err
	}
	// One value or several is part of what a field is, in every version.
	if err := CheckFieldShapes(types); err != nil {
		return nil, err
	}
	// Reactions and link resolution name other Types too.
	if err := CheckReactions(types); err != nil {
		return nil, err
	}
	// So do offers: a host offers what the hooks of the Items it creates
	// inside itself read from their container.
	if err := CheckOffers(types); err != nil {
		return nil, err
	}
	// And what a container admits customers to.
	if err := CheckCustomers(types); err != nil {
		return nil, err
	}
	// cmp.Or picks the first non-zero comparison: by ID, then by version.
	slices.SortFunc(types, func(a, b *Type) int {
		return cmp.Or(strings.Compare(a.ID, b.ID), cmp.Compare(a.Version, b.Version))
	})
	return types, nil
}
