// Command contractsfrozen checks that no published action contract changed
// (Ventoux V1): every line of the base branch's contracts.lock must be in
// this one, unchanged. CI runs it on pull requests, beside buf breaking:
//
//	contractsfrozen BASE_LOCK HEAD_LOCK
//
// BASE_LOCK may be empty (the base branch has no lock yet). Exit 0 when
// nothing published changed, 1 when something did (each line named on
// stderr), 2 on a usage error or an unreadable file.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/francoisnyc/work-kernel/internal/contracts"
)

func main() { os.Exit(run(os.Args[1:], os.Stderr)) }

func run(args []string, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "usage: contractsfrozen BASE_LOCK HEAD_LOCK")
		return 2
	}
	base, err := os.ReadFile(args[0])
	if err != nil {
		fmt.Fprintf(stderr, "contractsfrozen: %v\n", err)
		return 2
	}
	head, err := os.ReadFile(args[1])
	if err != nil {
		fmt.Fprintf(stderr, "contractsfrozen: %v\n", err)
		return 2
	}
	if err := contracts.CheckFrozen(base, head); err != nil {
		fmt.Fprintf(stderr, "contractsfrozen: a published contract changed:\n%v\n", err)
		return 1
	}
	return 0
}
