package main

// Step 0, part 2: listing, history, filters, paging and agent sessions.

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"connectrpc.com/connect"
	"golang.org/x/term"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
)

// --- filters and paging ---

// filterOpts are the FILTERS flags, shared by item search and item list.
type filterOpts struct {
	types, states, categories, wheres, links listFlag
}

func filterFlags(fs *flag.FlagSet) *filterOpts {
	f := &filterOpts{}
	fs.Var(&f.types, "type", "a Type (ticket) or its name, any version; repeatable")
	fs.Var(&f.states, "state", "a state ID; repeatable")
	fs.Var(&f.categories, "category", "a state category (active, done); repeatable")
	fs.Var(&f.wheres, "where", "F=V, F>V, F>=V, F<V or F<=V on a field (on a principal field, V may be me); repeatable")
	fs.Var(&f.links, "linked", "out:NAME=ITEM (Items that link to ITEM) or in:NAME=ITEM (Items ITEM links to, by the inverse name); repeatable")
	return f
}

// filter builds the ItemFilter; nil when no flag was given.
func (f *filterOpts) filter(ns string) (*kernelv1.ItemFilter, error) {
	if len(f.types)+len(f.states)+len(f.categories)+len(f.wheres)+len(f.links) == 0 {
		return nil, nil
	}
	out := &kernelv1.ItemFilter{States: f.states}
	for _, t := range f.types {
		if !strings.Contains(t, "/") {
			t = "namespaces/" + ns + "/types/" + t
		}
		out.Types = append(out.Types, t)
	}
	for _, c := range f.categories {
		v, ok := kernelv1.StateCategory_value["STATE_CATEGORY_"+strings.ToUpper(c)]
		if !ok || v == 0 {
			return nil, usagef("--category wants a state category (triage, todo, active, done...), not %q", c)
		}
		out.Categories = append(out.Categories, kernelv1.StateCategory(v))
	}
	for _, w := range f.wheres {
		ff, err := where(w)
		if err != nil {
			return nil, err
		}
		out.Fields = append(out.Fields, ff)
	}
	for _, l := range f.links {
		lf, err := linked(ns, l)
		if err != nil {
			return nil, err
		}
		out.Links = append(out.Links, lf)
	}
	return out, nil
}

// where reads F=V, F>V, F>=V, F<V or F<=V. V is read like a --set value.
func where(w string) (*kernelv1.FieldFilter, error) {
	// Two-character operators first, so ">=" is not read as ">".
	for _, op := range []string{">=", "<=", ">", "<", "="} {
		field, raw, ok := strings.Cut(w, op)
		if !ok || field == "" || strings.ContainsAny(field, "<>=") {
			continue
		}
		ff, v := &kernelv1.FieldFilter{Field: field}, jsonValue(raw)
		switch op {
		case "=":
			ff.Eq = v
		case ">":
			ff.Gt = v
		case ">=":
			ff.Gte = v
		case "<":
			ff.Lt = v
		case "<=":
			ff.Lte = v
		}
		return ff, nil
	}
	return nil, usagef("--where wants F=V, F>V, F>=V, F<V or F<=V, not %q", w)
}

// pagingOpts are the PAGING flags. The size is read here, so a bad one is
// a usage error naming the flag.
type pagingOpts struct{ sizeRaw, token *string }

func pagingFlags(fs *flag.FlagSet) pagingOpts {
	return pagingOpts{
		sizeRaw: fs.String("page-size", "", "Items or events per page (default 50, at most 500)"),
		token:   fs.String("page-token", "", "the next_page_token of the previous page"),
	}
}

func (p pagingOpts) size() (int32, error) {
	if *p.sizeRaw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(*p.sizeRaw, 10, 32)
	if err != nil {
		return 0, usagef("--page-size wants a number, not %q", *p.sizeRaw)
	}
	return int32(n), nil
}

// --- item list and history ---

func (c *cli) itemList(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("item list", flag.ContinueOnError)
	fl, pg := filterFlags(fs), pagingFlags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	parent := "namespaces/" + c.ns // no ITEM: the top level
	switch len(pos) {
	case 0:
	case 1:
		parent = c.itemRef(pos[0])
	default:
		return usagef("want at most one ITEM, got %d arguments", len(pos))
	}
	filter, err := fl.filter(c.ns)
	if err != nil {
		return err
	}
	size, err := pg.size()
	if err != nil {
		return err
	}
	res, err := c.c.ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: parent, Filter: filter, PageSize: size, PageToken: *pg.token,
	}))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

func (c *cli) itemHistory(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("item history", flag.ContinueOnError)
	pg := pagingFlags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ref, err := one(pos, "ITEM")
	if err != nil {
		return err
	}
	size, err := pg.size()
	if err != nil {
		return err
	}
	res, err := c.c.ListItemEvents(ctx, connect.NewRequest(&kernelv1.ListItemEventsRequest{
		Parent: c.itemRef(ref), PageSize: size, PageToken: *pg.token,
	}))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

// --- agent sessions ---

// sessionPath is where the session of an --as name is saved: under
// $XDG_CONFIG_HOME when it is set to an absolute path, on every OS (so a
// script can keep sessions out of the user's config; os.UserConfigDir reads
// it on Linux only), else under os.UserConfigDir.
func sessionPath(as string) (string, error) {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" || !filepath.IsAbs(dir) {
		var err error
		if dir, err = os.UserConfigDir(); err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, "pli", "sessions", filepath.Base(as)), nil
}

// savedSession is the session saved for an --as name; "" if none.
func savedSession(as string) string {
	path, err := sessionPath(as)
	if err != nil {
		return ""
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func saveSession(as, id string) error {
	path, err := sessionPath(as)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(id+"\n"), 0o600)
}

// scopeRef turns a scope argument into a name: "namespace" is the whole
// namespace, anything else an ITEM.
func (c *cli) scopeRef(ref string) string {
	if ref == "namespace" {
		return "namespaces/" + c.ns
	}
	return c.itemRef(ref)
}

func (c *cli) agentStart(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent start", flag.ContinueOnError)
	user := fs.String("for", "", "the user the agent acts for (user:ana); empty: itself")
	scopes := fs.String("scopes", "", "what the session may touch: ITEM or namespace, comma-separated")
	powers := fs.String("powers", "", "read, create, edit_fields, transition:<state>, transition:*, move, comment, outbound")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	switch {
	case len(pos) > 0:
		return usagef("takes no arguments, got %d", len(pos))
	case *scopes == "":
		return usagef("--scopes is required: ITEM or namespace, comma-separated")
	case *powers == "":
		return usagef("--powers is required: read, edit_fields, ..., comma-separated")
	}
	req := &kernelv1.StartAgentSessionRequest{
		Parent: "namespaces/" + c.ns, ActingFor: *user, Powers: principals(*powers),
	}
	for _, s := range principals(*scopes) {
		req.Scopes = append(req.Scopes, c.scopeRef(s))
	}
	res, err := c.c.StartAgentSession(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	// Later commands as this --as name send the new session.
	if err := saveSession(c.as, res.Msg.Session.GetId()); err != nil {
		return err
	}
	return c.print(res.Msg)
}

// agentApprove approves one scope for an agent session. Approving an Item
// approves its audience, so pli first asks the server who that is
// (validate_only), or takes it from --expect-audience, says it, and goes on
// only with --yes, or a "y" typed at a terminal. It sends the audience it
// said, and the server approves only for that audience.
func (c *cli) agentApprove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("agent approve", flag.ContinueOnError)
	requestID := fs.String("request-id", newRequestID(), "idempotency key")
	yes := fs.Bool("yes", false, "approve without asking (required when not at a terminal)")
	expect := fs.String("expect-audience", "",
		"approve only for this audience (namespace or restricted), without asking the server for it first")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return usagef("want SESSION SCOPE (the scope a NEEDS_APPROVAL named); " +
			"run it --as the user who approves: the approver is whoever the token says")
	}
	switch *expect {
	case "", "namespace", "restricted":
	default:
		return usagef("--expect-audience wants namespace or restricted, not %q", *expect)
	}
	req := &kernelv1.ApproveWidenRequest{
		Parent: "namespaces/" + c.ns, Session: pos[0], Scope: c.scopeRef(pos[1]),
	}
	// The audience pli says, and sends: the server commits the approval only
	// if it is still the audience then (else AUDIENCE_CHANGED).
	aud := *expect
	if aud == "" {
		req.ValidateOnly = true
		check, err := c.c.ApproveWiden(ctx, connect.NewRequest(req))
		if err != nil {
			return err
		}
		aud = check.Msg.Audience
	}
	who := "everyone in namespace " + c.ns
	if aud != "namespace" {
		who = "the readers of " + req.Scope
	}
	said := fmt.Sprintf("Content from this agent session will become visible to %s.\n", who)
	// Without --yes pli says the audience before it asks (or refuses to go
	// on from a script). With --yes it says it once the server has approved
	// for it; a refusal's metadata names the audience sent instead.
	if !*yes {
		fmt.Fprint(c.stderr, said)
		if !interactive() {
			return usagef("not approved: pass --yes to approve from a script")
		}
		fmt.Fprint(c.stderr, "Approve? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			return usagef("not approved")
		}
	}
	req.ValidateOnly, req.RequestId, req.ExpectedAudience = false, *requestID, aud
	res, err := c.c.ApproveWiden(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	if *yes {
		fmt.Fprint(c.stderr, said)
	}
	return c.print(res.Msg)
}

// interactive reports whether stdin is a terminal, where pli may ask.
// term.IsTerminal asks the terminal driver itself, so a pipe, a file or
// /dev/null (a script, CI) never counts.
func interactive() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}
