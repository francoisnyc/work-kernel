package main

import (
	"context"
	"flag"
	"fmt"
	"strconv"
	"strings"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// typeExplain prints a Type version in plain language (ExplainType):
//
//	pli type explain [--json] TYPE@VERSION
//	pli type explain [--json] --draft TYPE@VERSION PATH...
//
// The reading's text, exactly; with --draft, the PATHs are read as type
// validate reads them, and a draft with problems prints them as type
// validate does (exit 1). --json prints the response.
func (c *cli) typeExplain(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("type explain", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the response as JSON")
	draft := fs.Bool("draft", false, "read TYPE@VERSION from the PATHs that follow, a draft")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	req := &kernelv1.ExplainTypeRequest{}
	if *draft {
		if len(pos) == 0 {
			return usagef("want TYPE@VERSION (incident@4), then at least one PATH")
		}
		id, v, ok := strings.Cut(pos[0], "@")
		n, err := strconv.Atoi(v)
		if !ok || id == "" || err != nil || n < 1 {
			return usagef("want TYPE@VERSION (incident@4), not %q", pos[0])
		}
		if len(pos) == 1 {
			return usagef("want at least one PATH (a Type file or a folder of Types) after %s", pos[0])
		}
		sources, local, err := readSources(pos[1:])
		if err != nil {
			return err
		}
		if len(local) > 0 {
			for _, p := range local {
				fmt.Fprintln(c.stdout, problemLine(p))
			}
			return errProblems
		}
		req.Parent, req.Sources, req.TypeId, req.Version = "namespaces/"+c.ns, sources, id, int32(n)
	} else {
		ref, err := one(pos, "TYPE@VERSION")
		if err != nil {
			return err
		}
		if !strings.Contains(ref, "/") && !strings.Contains(ref, "@") {
			return usagef("want TYPE@VERSION (vacation_request@3) or a Type version's name, not %q", ref)
		}
		if req.Name, err = c.typeRef(ref); err != nil {
			return err
		}
	}
	res, err := c.c.ExplainType(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	if *asJSON {
		if err := c.print(res.Msg); err != nil {
			return err
		}
	} else if res.Msg.Reading != nil {
		fmt.Fprint(c.stdout, res.Msg.Reading.Text)
	} else {
		for _, p := range res.Msg.Problems {
			fmt.Fprintln(c.stdout, problemLine(p))
		}
	}
	if len(res.Msg.Problems) > 0 {
		return errProblems
	}
	return nil
}
