package typedef

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

// typeFiles lists the .yaml files of dir and of its subfolders, one level
// deep: a subfolder is a package of Types with its README. IDs stay global.
// A folder two levels down is a problem (a Type nobody loads is a trap);
// folders whose name starts with a dot are skipped, as are other files.
func typeFiles(dir string) ([]string, Problems, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("typedef: read %s: %w", dir, err)
	}
	var (
		files []string
		ps    Problems
	)
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		switch {
		case strings.HasPrefix(e.Name(), "."):
		case e.IsDir():
			inside, err := os.ReadDir(path)
			if err != nil {
				return nil, nil, fmt.Errorf("typedef: read %s: %w", path, err)
			}
			for _, f := range inside {
				switch sub := filepath.Join(path, f.Name()); {
				case strings.HasPrefix(f.Name(), "."):
				case f.IsDir():
					var fp Problems
					newReporter(&fp, "", 0).fail(CodeFolderTooDeep, "", "Move its Types up one level, or into their own folder of the Types folder",
						"%s: Types are read from a folder and its subfolders, one level deep", sub)
					fp[0].Source = sub
					ps = append(ps, fp...)
				case strings.HasSuffix(f.Name(), ".yaml"):
					files = append(files, sub)
				}
			}
		case strings.HasSuffix(e.Name(), ".yaml"):
			files = append(files, path)
		}
	}
	return files, ps, nil
}

// ParseDir reads every .yaml file in dir, and in its subfolders one level
// deep, as one Type version, and returns them ordered by Type ID, then
// version. Other files (a README) are skipped.
//
// The error, if any, is Problems: every problem of every file (stages a and
// b) and, only when each Type is sound on its own, every problem of the
// Types together (stage c). Each names its file. An error reading the
// folder is returned as it is.
func ParseDir(dir string) ([]*Type, error) {
	files, ps, err := typeFiles(dir)
	if err != nil {
		return nil, err
	}
	type loaded struct {
		doc    *yaml.Node
		source string
	}
	var types []*Type
	where := map[string]loaded{} // "id@version" -> its document and file
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("typedef: %w", err)
		}
		t, doc, fps := parseDoc(data)
		if t != nil {
			key := fmt.Sprintf("%s@%d", t.ID, t.Version)
			if first, dup := where[key]; dup {
				newReporter(&fps, t.ID, t.Version).fail(CodeDuplicateVersion, "version",
					"Give one of the two files the next version number, or remove it",
					"%s v%d is declared by both %s and %s", t.ID, t.Version, first.source, path)
				position(fps, doc)
			} else {
				where[key] = loaded{doc, path}
				types = append(types, t)
			}
		}
		for i := range fps {
			fps[i].Source = path
		}
		ps = append(ps, fps...)
	}
	if len(ps) > 0 {
		sortProblems(ps)
		return nil, ps
	}

	// Stage (c). cmp.Or picks the first non-zero comparison: by ID, then by
	// version, so a problem across versions lands on the later one.
	slices.SortFunc(types, func(a, b *Type) int {
		return cmp.Or(strings.Compare(a.ID, b.ID), cmp.Compare(a.Version, b.Version))
	})
	for _, check := range []func([]*Type) error{
		// Links name other Types, and keep their inverses across versions.
		CheckLinkTargets, CheckLinkInverses,
		// So do incoming declarations: the links other Types declare.
		CheckIncoming,
		// One value or several is part of what a field is, in every version.
		CheckFieldShapes,
		// Reactions and link resolution name other Types too.
		CheckReactions,
		// So do offers: a host offers what the hooks of the Items it creates
		// inside itself read from their container.
		CheckOffers,
		// And what a container admits customers to.
		CheckCustomers,
	} {
		var cps Problems
		if errors.As(check(types), &cps) {
			ps = append(ps, cps...)
		}
	}
	if len(ps) > 0 {
		for i := range ps {
			l := where[fmt.Sprintf("%s@%d", ps[i].TypeID, ps[i].Version)]
			ps[i].Source = l.source
			ps[i].Line, ps[i].Column = locate(l.doc, ps[i].Path)
		}
		sortProblems(ps)
		return nil, ps
	}
	return types, nil
}
