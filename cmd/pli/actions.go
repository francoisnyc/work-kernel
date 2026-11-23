package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// actionList prints the action catalogue (Ventoux V1, ListActions):
//
//	action list [--json]
//
// One block per action, separated by a blank line: a heading with its
// kinds, risk and side effects; its description; each input and output with
// its type; each input's trust; its reasons. --json prints the response.
func (c *cli) actionList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("action list", flag.ContinueOnError)
	asJSON := fs.Bool("json", false, "print the response as JSON")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		return usagef("unexpected arguments %v", pos)
	}
	res, err := c.c.ListActions(ctx, connect.NewRequest(&kernelv1.ListActionsRequest{Parent: "namespaces/" + c.ns}))
	if err != nil {
		return err
	}
	if *asJSON {
		return c.print(res.Msg)
	}
	for i, a := range res.Msg.Actions {
		if i > 0 {
			fmt.Fprintln(c.stdout)
		}
		fmt.Fprint(c.stdout, actionBlock(a))
	}
	return nil
}

// actionBlock is one action as text.
func actionBlock(a *kernelv1.Action) string {
	var b strings.Builder
	kinds := make([]string, len(a.Kinds))
	for i, k := range a.Kinds {
		kinds[i] = strings.ToLower(strings.TrimPrefix(k.String(), "HOOK_KIND_"))
	}
	effects := "no side effects"
	if a.SideEffects {
		effects = "side effects"
	}
	risk := strings.ToLower(strings.TrimPrefix(a.Risk.String(), "ACTION_RISK_"))
	fmt.Fprintf(&b, "%s (%s; risk %s; %s)\n", a.Action, strings.Join(kinds, ", "), risk, effects)
	fmt.Fprintf(&b, "  %s\n", a.Description)
	for _, v := range a.Inputs {
		trust := "any source"
		if v.Trust == kernelv1.ActionInputTrust_ACTION_INPUT_TRUST_TRUSTED {
			trust = "trusted (declared, not enforced until Ventoux item 4)"
		}
		fmt.Fprintf(&b, "  in  %s: %s; %s\n", v.Name, valueType(v), trust)
	}
	for _, v := range a.Outputs {
		fmt.Fprintf(&b, "  out %s: %s\n", v.Name, valueType(v))
	}
	if len(a.Reasons) > 0 {
		fmt.Fprintf(&b, "  reasons: %s\n", strings.Join(a.Reasons, ", "))
	}
	return b.String()
}

// valueType is "principal", "enum (a, b)", "principal or null".
func valueType(v *kernelv1.ActionValue) string {
	t := strings.ToLower(strings.TrimPrefix(v.Type.String(), "FIELD_TYPE_"))
	if len(v.Options) > 0 {
		t += " (" + strings.Join(v.Options, ", ") + ")"
	}
	if v.Nullable {
		t += " or null"
	}
	return t
}
