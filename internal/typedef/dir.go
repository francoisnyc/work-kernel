package typedef

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	sources := make([]Source, 0, len(files))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("typedef: %w", err)
		}
		sources = append(sources, Source{Name: path, Content: data})
	}
	// The folder's problems (FOLDER_TOO_DEEP) hold stage (c) back as a
	// file's would. Nothing is registered: the folder is checked alone.
	return check(sources, nil, ps)
}
