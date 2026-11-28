package main

import (
	"flag"
	"fmt"
	"io"
)

// runFmt is "pli fmt [-w] [-l] [--check] [PATH...]": Type files rewritten
// in one canonical layout (package typefmt), as gofmt does for Go. It reads
// files, never the server, so it needs no --as.
//
//   - no flag: each file's formatted content on stdout; no file changes
//   - -w: each file that differs is rewritten in place
//   - -l: the files that differ, one per line
//   - --check: as -l, and exit 1 if any file differs (for CI)
//   - no PATH: stdin, formatted, on stdout
//
// A PATH is a file or a folder, read as "type validate" reads them. A file
// that cannot be formatted (not YAML, or a comment with no place) is
// reported on stderr as PATH[:LINE]: message and left as it is; the others
// are still done. Exit 0 when done, 1 when a file differs under --check or
// one could not be formatted, 2 on a usage error (nothing done).
func runFmt(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("pli fmt", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	write := fs.Bool("w", false, "rewrite the files that differ")
	list := fs.Bool("l", false, "list the files that differ")
	check := fs.Bool("check", false, "list the files that differ; exit 1 if any does")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "pli: %v\n%s\n", err, usage)
		return exitUsage
	}
	_, _, _ = write, list, check
	fmt.Fprintln(stderr, "pli fmt: not built yet")
	return exitAPI
}
