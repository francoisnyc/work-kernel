package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/francoisnyc/work-kernel/internal/typefmt"
)

// exitFailed: pli fmt could not format a file, or --check found one that
// differs.
const exitFailed = 1

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
	usageErr := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "pli: "+format+"\n%s\n", append(a, usage)...)
		return exitUsage
	}
	if err := fs.Parse(args); err != nil {
		return usageErr("%v", err)
	}
	if *check && *write {
		return usageErr("fmt: --check writes nothing; use -w or --check, not both")
	}
	listing := *list || *check

	paths := fs.Args()
	if len(paths) == 0 {
		if *write {
			return usageErr("fmt: -w needs a PATH (stdin has no file to write)")
		}
		src, err := io.ReadAll(stdin)
		if err != nil {
			fmt.Fprintf(stderr, "pli: fmt: %v\n", err)
			return exitFailed
		}
		out, err := typefmt.Format(src)
		if err != nil {
			fmt.Fprintln(stderr, fmtError("<standard input>", err))
			return exitFailed
		}
		differs := !bytes.Equal(src, out)
		switch {
		case listing && differs:
			fmt.Fprintln(stdout, "<standard input>")
		case !listing:
			stdout.Write(out)
		}
		if *check && differs {
			return exitFailed
		}
		return exitOK
	}

	// Every PATH is read before anything is done: a usage error does
	// nothing at all.
	var files, deep []string
	for _, p := range paths {
		f, d, err := typeFiles(p)
		if err != nil {
			return usageErr("fmt: %v", err)
		}
		files, deep = append(files, f...), append(deep, d...)
	}

	failed, anyDiffers := false, false
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintf(stderr, "%s: %v\n", f, err)
			failed = true
			continue
		}
		out, err := typefmt.Format(src)
		if err != nil {
			fmt.Fprintln(stderr, fmtError(f, err))
			failed = true
			continue
		}
		differs := !bytes.Equal(src, out)
		anyDiffers = anyDiffers || differs
		if listing && differs {
			fmt.Fprintln(stdout, f)
		}
		if *write && differs {
			// WriteFile keeps an existing file's mode.
			if err := os.WriteFile(f, out, 0o644); err != nil {
				fmt.Fprintf(stderr, "%s: %v\n", f, err)
				failed = true
			}
		}
		if !listing && !*write {
			stdout.Write(out)
		}
	}
	for _, d := range deep {
		fmt.Fprintf(stderr, "%s: a folder more than one level below the folder given; its Types were not formatted\n", d)
		failed = true
	}
	if failed || (*check && anyDiffers) {
		return exitFailed
	}
	return exitOK
}

// fmtError is a file's error as pli prints it: PATH:LINE: for a comment
// with no place, else PATH: .
func fmtError(path string, err error) string {
	var ce *typefmt.CommentError
	if errors.As(err, &ce) {
		return fmt.Sprintf("%s:%d: cannot place comment %q", path, ce.Line, ce.Text)
	}
	return fmt.Sprintf("%s: %v", path, err)
}
