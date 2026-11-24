package main

import (
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// errProblems ends a type validate that found an ERROR: exit 1, with the
// problems already printed.
var errProblems = errors.New("the drafts have problems")

// typeValidate sends draft Types to ValidateTypes (Ventoux V2b):
//
//	type validate [--json] PATH...
//
// A PATH is a file or a folder. A folder is read as the item service reads
// its Types folder: its .yaml files and those of its subfolders, one level
// deep; names starting with a dot are skipped; a deeper folder is
// FOLDER_TOO_DEEP, found here. Each source is named by its path as given,
// joined. pli prints one line per problem (docs/design.md, "Type
// problems"), or with --json the response; it exits 1 on any ERROR.
func (c *cli) typeValidate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("type validate", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the response as JSON")
	paths, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return usagef("want at least one PATH (a Type file or a folder of Types)")
	}
	sources, local, err := readSources(paths)
	if err != nil {
		return err
	}
	res := &kernelv1.ValidateTypesResponse{}
	if len(sources) > 0 {
		r, err := c.c.ValidateTypes(ctx, connect.NewRequest(&kernelv1.ValidateTypesRequest{
			Parent: "namespaces/" + c.ns, Sources: sources,
		}))
		if err != nil {
			return err
		}
		res = r.Msg
	}
	res.Problems = append(res.Problems, local...)
	slices.SortStableFunc(res.Problems, func(a, b *kernelv1.TypeProblem) int {
		return cmp.Or(strings.Compare(a.Source, b.Source), cmp.Compare(a.Line, b.Line), cmp.Compare(a.Column, b.Column))
	})

	if *asJSON {
		if err := c.print(res); err != nil {
			return err
		}
	} else {
		for _, p := range res.Problems {
			fmt.Fprintln(c.stdout, problemLine(p))
		}
	}
	for _, p := range res.Problems {
		if p.Severity == kernelv1.TypeProblemSeverity_TYPE_PROBLEM_SEVERITY_ERROR {
			return errProblems
		}
	}
	return nil
}

// readSources reads the Type files of PATHs as sources named by their
// paths, and the folders too deep to read as problems found here.
func readSources(paths []string) (sources []*kernelv1.TypeSource, local []*kernelv1.TypeProblem, err error) {
	for _, path := range paths {
		files, deep, err := typeFiles(path)
		if err != nil {
			return nil, nil, err
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				return nil, nil, usagef("%v", err)
			}
			sources = append(sources, &kernelv1.TypeSource{Name: f, Content: string(data)})
		}
		for _, d := range deep {
			local = append(local, &kernelv1.TypeProblem{
				Source: d, Severity: kernelv1.TypeProblemSeverity_TYPE_PROBLEM_SEVERITY_ERROR, Code: "FOLDER_TOO_DEEP",
				Message: "Types are read from a folder and its subfolders, one level deep. " +
					"Move its Types up one level, or into their own folder of the Types folder",
			})
		}
	}
	if len(sources) == 0 && len(local) == 0 {
		return nil, nil, usagef("no .yaml file in %s", strings.Join(paths, ", "))
	}
	return sources, local, nil
}

// typeFiles lists the Type files of a PATH: the file itself, or a folder's
// .yaml files and its subfolders' (one level deep, dot-names skipped), and
// the folders below that, which are too deep. A PATH that cannot be read is
// a usage error: nothing is sent.
func typeFiles(path string) (files, deep []string, err error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, usagef("%v", err)
	}
	if !info.IsDir() {
		return []string{path}, nil, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, nil, usagef("%v", err)
	}
	for _, e := range entries {
		p := filepath.Join(path, e.Name())
		switch {
		case strings.HasPrefix(e.Name(), "."):
		case e.IsDir():
			inside, err := os.ReadDir(p)
			if err != nil {
				return nil, nil, usagef("%v", err)
			}
			for _, f := range inside {
				switch sub := filepath.Join(p, f.Name()); {
				case strings.HasPrefix(f.Name(), "."):
				case f.IsDir():
					deep = append(deep, sub)
				case strings.HasSuffix(f.Name(), ".yaml"):
					files = append(files, sub)
				}
			}
		case strings.HasSuffix(e.Name(), ".yaml"):
			files = append(files, p)
		}
	}
	return files, deep, nil
}

// problemLine is a problem as docs/design.md writes it in a log:
//
//	<source>:<line>:<col>: <CODE> typedef <id> v<n>: <message>
//
// leaving out :<col> when the column is unknown, <line>:<col> when the line
// is, and "typedef <id> v<n>: " when the Type is.
func problemLine(p *kernelv1.TypeProblem) string {
	where := []string{p.Source}
	if p.Line > 0 {
		where = append(where, strconv.Itoa(int(p.Line)))
		if p.Column > 0 {
			where = append(where, strconv.Itoa(int(p.Column)))
		}
	}
	var b strings.Builder
	b.WriteString(strings.Join(where, ":") + ": " + p.Code + " ")
	// namespaces/{ns}/types/{id}/versions/{n}
	if parts := strings.Split(p.Type, "/"); len(parts) == 6 {
		fmt.Fprintf(&b, "typedef %s v%s: ", parts[3], parts[5])
	}
	b.WriteString(p.Message)
	return b.String()
}
