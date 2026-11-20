// Command pli is the command-line client of the public API v1. It reaches
// Items only through the API, like any other client.
//
//	pli [--server URL] [--tokens FILE] --as NAME [--session ID] <command>
//	  item create --type TYPE [--in ITEM] [--narrow P,P] [--set ID=VALUE]... [--request-id ID]
//	  item get ITEM
//	  item update ITEM [--set ID=VALUE]... [--add ID=VALUE]... [--remove ID=VALUE]... [--readers P,P | --inherit]
//	                   [--move-to ITEM | --move-to-top] [--confirm-widen] [--etag E] [--request-id ID]
//	  item transition ITEM --to STATE [--set ID=VALUE]... [--add ID=VALUE]... [--remove ID=VALUE]... [--wait D] [--etag E] [--request-id ID]
//	  item search [WORDS...] [--min-seq N] [FILTERS] [PAGING]
//	  item list [ITEM] [FILTERS] [PAGING]           (no ITEM: the top level)
//	  item history ITEM [PAGING]
//	  item link SRC LINK_TYPE TARGET [--etag E] [--request-id ID]
//	  item unlink SRC LINK_TYPE TARGET [--etag E] [--request-id ID]
//	  type get TYPE
//	  type list
//	  agent start [--for USER] --scopes S,S --powers P,P
//	  agent approve SESSION SCOPE [--expect-audience A] [--yes] [--request-id ID]  (run it --as the user who approves)
//
//	FILTERS: [--type TYPE]... [--state S]... [--category C]... [--where F=V|F>V|F>=V|F<V|F<=V]... [--empty F]...
//	         [--linked out:NAME=ITEM | in:NAME=ITEM]...
//	PAGING:  [--page-size N] [--page-token T]
//
// --server, --tokens and --as default to PLI_SERVER (http://localhost:8080),
// PLI_TOKENS (dev-tokens.yaml) and PLI_AS. --as names an entry of the
// dev-token file. ITEM is an Item's name or its bare ID (in the caller's
// namespace); TYPE is a Type version's name or ticket@3. A --set value is
// read as JSON when it parses (3, true, null, "3"), else as a string.
//
// In FILTERS, TYPE is a Type's name or ID without a version (ticket) and C a
// category (active, done). A scope S is an ITEM or "namespace" (all of it);
// POWERS are read, create, edit_fields, transition:<state>, transition:*,
// move, comment, outbound.
//
// Agents: "agent start", called --as an agent, starts a session and saves
// its ID for that --as name in pli's config directory ($XDG_CONFIG_HOME on
// every OS when set, else os.UserConfigDir; then pli/sessions); every later
// command --as that name sends it in the
// Pli-Agent-Session header. --session ID sends another. "agent approve" is
// run --as the user the agent acts for: the approver is whoever the token
// says, never a flag. It first says who will see the agent's content
// (everyone in the namespace, or the readers of the scope's Item) and needs
// --yes to go on from a script. It sends that audience, and the server
// approves only if it is still the audience (else AUDIENCE_CHANGED).
// --expect-audience A (namespace or restricted) names the audience instead
// of asking the server for it first.
//
// Each response is printed as JSON (protobuf's JSON mapping, with the proto
// field names). An API error goes to stderr as "error: CODE REASON: message"
// and exits 1; a usage error exits 2. After SESSION_INVALID pli adds a hint.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/structpb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
	"github.com/francoisnyc/work-kernel/internal/devtoken"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

// Exit statuses.
const (
	exitOK    = 0
	exitAPI   = 1 // the API refused, or could not be reached
	exitUsage = 2 // the command line is wrong; nothing was sent
)

const usage = `usage: pli [--server URL] [--tokens FILE] --as NAME [--session ID] <command>
  item create --type TYPE [--in ITEM] [--narrow P,P] [--set ID=VALUE]... [--request-id ID]
  item get ITEM
  item update ITEM [--set ID=VALUE]... [--readers P,P | --inherit]
                   [--move-to ITEM | --move-to-top] [--confirm-widen] [--etag E] [--request-id ID]
  item transition ITEM --to STATE [--set ID=VALUE]... [--add ID=VALUE]... [--remove ID=VALUE]... [--wait D] [--etag E] [--request-id ID]
  item search [WORDS...] [--min-seq N] [FILTERS] [PAGING]
  item list [ITEM] [FILTERS] [PAGING]           (no ITEM: the top level)
  item history ITEM [PAGING]
  item link SRC LINK_TYPE TARGET [--etag E] [--request-id ID]     (LINK_TYPE: the declared name)
  item unlink SRC LINK_TYPE TARGET [--etag E] [--request-id ID]
  rank ITEM before|after OTHER     (one write; ITEM and OTHER in one container; the field f_rank)
  rank ITEM top|bottom
  rank rebalance --in CONTAINER    (rewrite the container's rank keys, short and even; run it when nobody is dragging)
  type get TYPE
  type list
  agent start [--for USER] --scopes S,S --powers P,P
  agent approve SESSION SCOPE [--expect-audience A] [--yes] [--request-id ID]  (run it --as the user who approves)

FILTERS: [--type TYPE]... [--state S]... [--category C]... [--where F=V|F>V|F>=V|F<V|F<=V]... [--empty F]...
         [--linked out:NAME=ITEM | in:NAME=ITEM]...   (on a principal field, --where F=me is the caller)
         [--order-by 'FIELD [asc|desc]']             (default: newest first)
PAGING:  [--page-size N] [--page-token T]`

// usageError is a wrong command line: exit 2, nothing sent.
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error { return usageError{fmt.Sprintf(format, args...)} }

// cli is one invocation: the client, the caller's namespace and the output.
type cli struct {
	c      kernelv1connect.ItemServiceClient
	ns     string
	as     string // the --as name, under which an agent session is saved
	stdout io.Writer
	stderr io.Writer // what pli says besides the response
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	global := flag.NewFlagSet("pli", flag.ContinueOnError)
	global.SetOutput(io.Discard) // errors are reported below, once
	server := global.String("server", envOr("PLI_SERVER", "http://localhost:8080"), "API server URL")
	tokensPath := global.String("tokens", envOr("PLI_TOKENS", "dev-tokens.yaml"), "dev-token file")
	as := global.String("as", os.Getenv("PLI_AS"), "the dev-token entry to call as")
	sessionFlag := global.String("session", "", "an agent session ID to send (default: the one saved for --as)")
	if err := global.Parse(args); err != nil {
		fmt.Fprintf(stderr, "pli: %v\n%s\n", err, usage)
		return exitUsage
	}
	rest := global.Args()
	if len(rest) < 2 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	if *as == "" {
		fmt.Fprintf(stderr, "pli: --as is required\n%s\n", usage)
		return exitUsage
	}
	tokens, err := devtoken.Load(*tokensPath)
	if err != nil {
		fmt.Fprintf(stderr, "pli: %v\n", err)
		return exitUsage
	}
	i := slices.IndexFunc(tokens, func(t devtoken.Token) bool { return t.Name == *as })
	if i < 0 {
		fmt.Fprintf(stderr, "pli: no token named %q in %s\n", *as, *tokensPath)
		return exitUsage
	}
	tok := tokens[i]

	// An agent sends its session: the one named, else the one saved for
	// this --as name. A user sends none unless told to.
	session := *sessionFlag
	if session == "" && strings.HasPrefix(tok.Principal, "agent:") {
		session = savedSession(*as)
	}
	c := &cli{
		c: kernelv1connect.NewItemServiceClient(http.DefaultClient, *server,
			connect.WithProtoJSON(), connect.WithInterceptors(bearer(tok.Token, session))),
		ns:     tok.Namespace,
		as:     *as,
		stdout: stdout,
		stderr: stderr,
	}
	commands := map[string]func(context.Context, []string) error{
		"item create":     c.itemCreate,
		"item get":        c.itemGet,
		"item update":     c.itemUpdate,
		"item transition": c.itemTransition,
		"item search":     c.itemSearch,
		"item list":       c.itemList,
		"item history":    c.itemHistory,
		"item link":       c.itemLink,
		"item unlink":     c.itemUnlink,
		"agent start":     c.agentStart,
		"agent approve":   c.agentApprove,
		"type get":        c.typeGet,
		"type list":       c.typeList,
	}
	name := rest[0] + " " + rest[1]
	cmd, ok := commands[name]
	cmdArgs := rest[2:]
	if rest[0] == "rank" {
		// rank's second word is an Item (or "rebalance"), not a verb.
		name, cmd, ok, cmdArgs = "rank", c.rank, true, rest[1:]
	}
	if !ok {
		fmt.Fprintf(stderr, "pli: unknown command %q\n%s\n", name, usage)
		return exitUsage
	}

	err = cmd(ctx, cmdArgs)
	var ue usageError
	var ce *connect.Error
	switch {
	case err == nil:
		return exitOK
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "pli %s: %v\n", name, err)
		return exitUsage
	case errors.As(err, &ce):
		printAPIError(stderr, ce)
		return exitAPI
	default:
		fmt.Fprintf(stderr, "error: %v\n", err)
		return exitAPI
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// sessionHeader is the header that carries an agent's session ID.
const sessionHeader = "Pli-Agent-Session"

// bearer sends the token on every call, and the agent session if any, but
// not when starting a session: a session is started without one.
func bearer(token, session string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			if session != "" && !strings.HasSuffix(req.Spec().Procedure, "/StartAgentSession") {
				req.Header().Set(sessionHeader, session)
			}
			return next(ctx, req)
		}
	}
}

// printAPIError writes "error: CODE REASON: message", then the ErrorInfo's
// metadata and any field violations, one per line, then a hint where one
// helps.
func printAPIError(w io.Writer, ce *connect.Error) {
	reason := "UNKNOWN"
	var lines []string
	for _, d := range ce.Details() {
		v, err := d.Value()
		if err != nil {
			continue
		}
		switch v := v.(type) {
		case *errdetails.ErrorInfo:
			reason = v.Reason
			for _, k := range slices.Sorted(maps.Keys(v.Metadata)) {
				lines = append(lines, fmt.Sprintf("  %s: %s", k, v.Metadata[k]))
			}
		case *errdetails.BadRequest:
			for _, fv := range v.FieldViolations {
				lines = append(lines, fmt.Sprintf("  %s: %s", fv.Field, fv.Description))
			}
		}
	}
	fmt.Fprintf(w, "error: %s %s: %s\n", strings.ToUpper(ce.Code().String()), reason, ce.Message())
	for _, l := range lines {
		fmt.Fprintln(w, l)
	}
	if reason == "SESSION_INVALID" {
		fmt.Fprintln(w, "hint: the saved agent session is gone, or belongs to another namespace or user; "+
			`run "pli agent start" again`)
	}
}

// print writes a response as JSON.
func (c *cli) print(m proto.Message) error {
	b, err := protojson.MarshalOptions{UseProtoNames: true, Multiline: true, Indent: "  "}.Marshal(m)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.stdout, "%s\n", b)
	return err
}

// --- arguments ---

// listFlag collects a repeated flag (--set a=1 --set b=2). A type with a
// String and a Set method satisfies flag.Value.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, ",") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// parse parses flags that may come before, between or after the positional
// arguments, which the flag package alone does not allow, and returns the
// positional ones.
func parse(fs *flag.FlagSet, args []string) ([]string, error) {
	fs.SetOutput(io.Discard)
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, usageError{err.Error()}
		}
		if fs.NArg() == 0 {
			return pos, nil
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

// one returns the single positional argument, named what.
func one(pos []string, what string) (string, error) {
	if len(pos) != 1 {
		return "", usagef("want one %s, got %d arguments", what, len(pos))
	}
	return pos[0], nil
}

// itemRef turns ITEM into a name: a bare ID is in the caller's namespace.
func (c *cli) itemRef(ref string) string {
	if strings.Contains(ref, "/") {
		return ref
	}
	return fmt.Sprintf("namespaces/%s/items/%s", c.ns, ref)
}

// typeRef turns TYPE (ticket@3, or a name) into a name.
func (c *cli) typeRef(ref string) (string, error) {
	if strings.Contains(ref, "/") {
		return ref, nil
	}
	id, v, ok := strings.Cut(ref, "@")
	if n, err := strconv.Atoi(v); !ok || id == "" || err != nil || n < 1 {
		return "", usagef("--type wants TYPE@VERSION (ticket@3) or a Type name, not %q", ref)
	}
	return fmt.Sprintf("namespaces/%s/types/%s/versions/%s", c.ns, id, v), nil
}

// fieldsOf turns --set ID=VALUE pairs into fields. A value is JSON when it
// parses, else a string.
func fieldsOf(sets []string) (*structpb.Struct, error) {
	if len(sets) == 0 {
		return nil, nil
	}
	out := &structpb.Struct{Fields: map[string]*structpb.Value{}}
	for _, s := range sets {
		id, raw, ok := strings.Cut(s, "=")
		if !ok || id == "" {
			return nil, usagef("--set wants ID=VALUE, not %q", s)
		}
		out.Fields[id] = jsonValue(raw)
	}
	return out, nil
}

// withSetOps adds the --add and --remove flags to a request's fields: for
// each field named, a set operation {"add": [...], "remove": [...]}. The
// values are text, never read as JSON: a label 2026 or true stays a label.
// A field may not be both set (--set) and changed (--add, --remove).
func withSetOps(fields *structpb.Struct, adds, removes listFlag) (*structpb.Struct, error) {
	if len(adds)+len(removes) == 0 {
		return fields, nil
	}
	ops := map[string]map[string][]any{}
	for kind, list := range map[string]listFlag{"add": adds, "remove": removes} {
		for _, s := range list {
			id, v, ok := strings.Cut(s, "=")
			if !ok || id == "" {
				return nil, usagef("--%s wants ID=VALUE, not %q", kind, s)
			}
			if _, set := fields.GetFields()[id]; set {
				return nil, usagef("%s: --set replaces the whole set; do not --%s to it in the same command", id, kind)
			}
			if ops[id] == nil {
				ops[id] = map[string][]any{}
			}
			ops[id][kind] = append(ops[id][kind], v)
		}
	}
	if fields == nil {
		fields = &structpb.Struct{Fields: map[string]*structpb.Value{}}
	}
	for id, op := range ops {
		m := map[string]any{}
		for kind, members := range op {
			m[kind] = members
		}
		v, err := structpb.NewValue(m)
		if err != nil {
			return nil, err
		}
		fields.Fields[id] = v
	}
	return fields, nil
}

// jsonValue reads raw as a JSON value, or as a string if it is not one. A
// whole number too big for a JSON number to carry exactly is sent as a
// numeric string, which the API accepts for an int.
func jsonValue(raw string) *structpb.Value {
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil || dec.More() {
		return structpb.NewStringValue(raw)
	}
	if n, ok := v.(json.Number); ok {
		if i, err := n.Int64(); err == nil && (i > 1<<53-1 || i < -(1<<53-1)) {
			return structpb.NewStringValue(n.String())
		}
		f, err := n.Float64()
		if err != nil {
			return structpb.NewStringValue(n.String())
		}
		return structpb.NewNumberValue(f)
	}
	pv, err := structpb.NewValue(v)
	if err != nil {
		return structpb.NewStringValue(raw)
	}
	return pv
}

func principals(list string) []string {
	var out []string
	for _, p := range strings.Split(list, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func newRequestID() string { return "pli-" + uuid.NewString() }

// --- commands ---

func (c *cli) itemCreate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("item create", flag.ContinueOnError)
	typ := fs.String("type", "", "TYPE (ticket@3) or a Type name")
	in := fs.String("in", "", "the container ITEM")
	narrow := fs.String("narrow", "", "readers, comma-separated (user:ana,user:bob)")
	requestID := fs.String("request-id", newRequestID(), "idempotency key")
	var sets listFlag
	fs.Var(&sets, "set", "ID=VALUE, repeatable")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) != 0 {
		return usagef("unexpected arguments %v", pos)
	}
	if *typ == "" {
		return usagef("--type is required")
	}
	typeName, err := c.typeRef(*typ)
	if err != nil {
		return err
	}
	fields, err := fieldsOf(sets)
	if err != nil {
		return err
	}
	req := &kernelv1.CreateItemRequest{
		Parent: "namespaces/" + c.ns, RequestId: *requestID, Type: typeName, Fields: fields,
	}
	if *in != "" {
		req.Container = c.itemRef(*in)
	}
	if *narrow != "" {
		req.Narrow = &kernelv1.Readers{Principals: principals(*narrow)}
	}
	res, err := c.c.CreateItem(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

func (c *cli) itemGet(ctx context.Context, args []string) error {
	pos, err := parse(flag.NewFlagSet("item get", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	ref, err := one(pos, "ITEM")
	if err != nil {
		return err
	}
	res, err := c.c.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: c.itemRef(ref)}))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

func (c *cli) itemUpdate(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("item update", flag.ContinueOnError)
	readers := fs.String("readers", "", "narrow to these readers, comma-separated")
	inherit := fs.Bool("inherit", false, "drop the Item's own readers; inherit its container's")
	moveTo := fs.String("move-to", "", "move into this container ITEM")
	moveToTop := fs.Bool("move-to-top", false, "move to the top level")
	confirmWiden := fs.Bool("confirm-widen", false, "confirm a move that lets more people see the Item")
	etag := fs.String("etag", "", "write only if the Item is still at this etag")
	requestID := fs.String("request-id", newRequestID(), "idempotency key")
	var sets, adds, removes listFlag
	fs.Var(&sets, "set", "ID=VALUE, repeatable")
	fs.Var(&adds, "add", "ID=VALUE: add VALUE to a multi-value field; repeatable")
	fs.Var(&removes, "remove", "ID=VALUE: remove VALUE from a multi-value field; repeatable")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ref, err := one(pos, "ITEM")
	if err != nil {
		return err
	}
	if *readers != "" && *inherit {
		return usagef("--readers and --inherit are two different changes; pick one")
	}
	if *moveTo != "" && *moveToTop {
		return usagef("--move-to and --move-to-top: pick one")
	}
	fields, err := fieldsOf(sets)
	if err != nil {
		return err
	}
	if fields, err = withSetOps(fields, adds, removes); err != nil {
		return err
	}
	req := &kernelv1.UpdateItemRequest{Name: c.itemRef(ref), RequestId: *requestID, Etag: *etag, Fields: fields}
	switch {
	case *readers != "":
		req.Policy = &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{
			Narrow: &kernelv1.Readers{Principals: principals(*readers)},
		}}
	case *inherit:
		req.Policy = &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Inherit{Inherit: true}}
	}
	switch {
	case *moveTo != "":
		req.Move = &kernelv1.Move{Container: c.itemRef(*moveTo), ConfirmWiden: *confirmWiden}
	case *moveToTop:
		req.Move = &kernelv1.Move{ConfirmWiden: *confirmWiden}
	}
	res, err := c.c.UpdateItem(ctx, connect.NewRequest(req))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

func (c *cli) itemTransition(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("item transition", flag.ContinueOnError)
	to := fs.String("to", "", "the state to move to")
	wait := fs.Duration("wait", 0, "how long to wait for a held transition's check (0 to 5s)")
	etag := fs.String("etag", "", "move only if the Item is still at this etag")
	requestID := fs.String("request-id", newRequestID(), "idempotency key")
	var sets, adds, removes listFlag
	fs.Var(&sets, "set", "ID=VALUE, repeatable")
	fs.Var(&adds, "add", "ID=VALUE: add VALUE to a multi-value field; repeatable")
	fs.Var(&removes, "remove", "ID=VALUE: remove VALUE from a multi-value field; repeatable")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	ref, err := one(pos, "ITEM")
	if err != nil {
		return err
	}
	if *to == "" {
		return usagef("--to is required")
	}
	fields, err := fieldsOf(sets)
	if err != nil {
		return err
	}
	if fields, err = withSetOps(fields, adds, removes); err != nil {
		return err
	}
	res, err := c.c.TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: c.itemRef(ref), RequestId: *requestID, Etag: *etag, ToState: *to, Fields: fields,
		Wait: durationpb.New(*wait),
	}))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

func (c *cli) itemSearch(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("item search", flag.ContinueOnError)
	minSeq := fs.Int64("min-seq", 0, "answer only once the write with this commit_seq is visible")
	fl, pg := filterFlags(fs), pagingFlags(fs)
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	filter, err := fl.filter(c.ns)
	if err != nil {
		return err
	}
	size, err := pg.size()
	if err != nil {
		return err
	}
	res, err := c.c.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/" + c.ns, Query: strings.Join(pos, " "), MinSeq: *minSeq,
		Filter: filter, PageSize: size, PageToken: *pg.token, OrderBy: fl.orderBy,
	}))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

func (c *cli) typeGet(ctx context.Context, args []string) error {
	pos, err := parse(flag.NewFlagSet("type get", flag.ContinueOnError), args)
	if err != nil {
		return err
	}
	ref, err := one(pos, "TYPE")
	if err != nil {
		return err
	}
	name, err := c.typeRef(ref)
	if err != nil {
		return err
	}
	res, err := c.c.GetType(ctx, connect.NewRequest(&kernelv1.GetTypeRequest{Name: name}))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}

func (c *cli) typeList(ctx context.Context, args []string) error {
	if _, err := parse(flag.NewFlagSet("type list", flag.ContinueOnError), args); err != nil {
		return err
	}
	res, err := c.c.ListTypes(ctx, connect.NewRequest(&kernelv1.ListTypesRequest{Parent: "namespaces/" + c.ns}))
	if err != nil {
		return err
	}
	return c.print(res.Msg)
}
