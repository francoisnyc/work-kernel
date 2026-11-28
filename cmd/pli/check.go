package main

import "io"

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
	return exitOK // Phase B stub
}
