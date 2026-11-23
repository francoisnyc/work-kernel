// Command contractsfrozen checks that no published action contract changed
// (Ventoux V1): every line of the base branch's contracts.lock must be in
// this one, unchanged. CI runs it on pull requests, beside buf breaking:
//
//	contractsfrozen BASE_LOCK HEAD_LOCK
//
// BASE_LOCK may be empty (the base branch has no lock yet). Exit 0 when
// nothing published changed, 1 when something did (each line named on
// stderr), 2 on a usage error or an unreadable file.
//
// Phase B stub.
package main

import (
	"fmt"
	"io"
	"os"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	fmt.Fprintln(stderr, "contractsfrozen: not implemented")
	return 2
}
