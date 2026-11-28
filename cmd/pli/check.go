package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/francoisnyc/work-kernel/internal/check"
	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// runCheck runs pli check (docs/roadmap.md, track 2) on the Types in the
// PATHs, files or folders read as type validate reads them:
//
//	pli check [--json] PATH...
//
// It reads the files and calls no server, so it needs no --as. It prints
// the report's text, or with --json the report; it exits 0 when no
// finding is an ERROR, 1 when one is, and 2 on a usage error or a Type
// that does not load (its problems printed as type validate prints them).
func runCheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	asJSON := fs.Bool("json", false, "print the report as JSON")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "pli check: %v\n", err)
		return exitUsage
	}
	paths := fs.Args()
	if len(paths) == 0 {
		fmt.Fprintln(stderr, "pli check: want at least one PATH (a Type file or a folder of Types)")
		return exitUsage
	}
	var types []*typedef.Type
	for _, p := range paths {
		got, err := loadTypes(p)
		if err != nil {
			// A Type's problems print one per line, as type validate's do;
			// a path that cannot be read is a usage error alike.
			var ps typedef.Problems
			if errors.As(err, &ps) {
				fmt.Fprintln(stderr, ps.Error())
			} else {
				fmt.Fprintf(stderr, "pli check: %v\n", err)
			}
			return exitUsage
		}
		types = append(types, got...)
	}
	r := check.Check(types, contracts.Default())
	if *asJSON {
		data, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "pli check: %v\n", err)
			return exitAPI
		}
		fmt.Fprintln(stdout, string(data))
	} else {
		fmt.Fprint(stdout, r.Text())
	}
	if r.HasErrors() {
		return exitAPI
	}
	return exitOK
}

// loadTypes reads a folder as the item service reads its Types folder, or
// one file as one Type version. A file's problems are named by its path.
func loadTypes(path string) ([]*typedef.Type, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return typedef.ParseDir(path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	ty, err := typedef.Parse(data)
	var ps typedef.Problems
	if errors.As(err, &ps) {
		for i := range ps {
			ps[i].Source = path
		}
		return nil, ps
	}
	if err != nil {
		return nil, err
	}
	return []*typedef.Type{ty}, nil
}
