// Tests for Step 0, part 1: the pli CLI for the part 1 operations. pli is a
// thin client of the public API: it reads the dev-token file, picks a token
// with --as, and prints each response as JSON (protobuf's JSON mapping with
// the proto field names, so int64s are strings and enums are their names).
// An API error goes to stderr as "error: <CODE> <REASON>: <message>" with exit
// status 1; a usage error exits 2.
//
//	pli [--server URL] [--tokens FILE] --as NAME <command>
//	  item create --type TYPE [--in ITEM] [--narrow P,P] [--set ID=VALUE]... [--request-id ID]
//	  item get ITEM
//	  item update ITEM [--set ID=VALUE]... [--readers P,P | --inherit]
//	                   [--move-to ITEM | --move-to-top] [--confirm-widen] [--etag E] [--request-id ID]
//	  item transition ITEM --to STATE [--set ID=VALUE]... [--wait D] [--etag E] [--request-id ID]
//	  item search [WORDS...] [--min-seq N] [FILTERS] [PAGING]
//	  item list [ITEM] [FILTERS] [PAGING]          (no ITEM: the top level)
//	  item history ITEM [PAGING]
//	  agent start [--for USER] --scopes S,S --powers P,P
//	  agent approve SESSION SCOPE
//
//	FILTERS: [--type TYPE]... [--state S]... [--category C]... [--where F=V | F>V | F>=V | F<V | F<=V]...
//	PAGING:  [--page-size N] [--page-token T]
//
// (Step 0, part 2.) In a filter, TYPE is a Type's name or ID without a
// version and C a category's name (active, done). A scope is an ITEM or
// "namespace". agent start saves the session's ID for the --as name in pli's
// config directory; later commands as that name send it; --session ID
// overrides it.
//
//	type get TYPE
//	type list
//
// ITEM is an Item's name or its bare ID (in the caller's namespace). TYPE is
// a Type version's name or "ticket@3". A --set value is read as JSON when it
// parses (3, true, null, "3"), else as a string.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/api"
	"github.com/francoisnyc/work-kernel/internal/devtoken"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/testdb"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

var (
	srv        *httptest.Server
	tokensPath string
)

const tokensYAML = `tokens:
  - {name: ana, token: dev-ana, principal: "user:ana", namespace: acme}
  - {name: bob, token: dev-bob, principal: "user:bob", namespace: acme}
  - {name: bot, token: dev-bot, principal: "agent:triage-bot", namespace: acme}
grants:
  - {agent: "agent:triage-bot", user: "user:ana", namespace: acme, scopes: [namespaces/acme],
     powers: [read, create, edit_fields, "transition:*", move]}
  - {agent: "agent:triage-bot", user: "user:bob", namespace: acme, scopes: [namespaces/acme], powers: [read]}
`

func TestMain(m *testing.M) { os.Exit(runTests(m)) }

func runTests(m *testing.M) int {
	ctx := context.Background()
	pool, stop, err := testdb.Start(ctx)
	if err != nil {
		log.Printf("start database: %v", err)
		return 1
	}
	defer stop()
	k := itemservice.New(pool)
	if err := k.EnsureNamespace(ctx, "acme"); err != nil {
		log.Printf("ensure namespace: %v", err)
		return 1
	}
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		log.Printf("parse types: %v", err)
		return 1
	}
	for _, ty := range types {
		if err := k.RegisterType(ctx, ty); err != nil {
			log.Printf("register: %v", err)
			return 1
		}
	}
	if _, err := pool.Exec(ctx,
		`insert into principal_roles (namespace_id, principal, role) values ('acme', 'user:ana', 'approver')`); err != nil {
		log.Printf("grant role: %v", err)
		return 1
	}
	toks, err := devtoken.Parse([]byte(tokensYAML))
	if err != nil {
		log.Printf("tokens: %v", err)
		return 1
	}
	grants, err := devtoken.ParseGrants([]byte(tokensYAML))
	if err != nil {
		log.Printf("grants: %v", err)
		return 1
	}
	dir, err := os.MkdirTemp("", "pli")
	if err != nil {
		log.Print(err)
		return 1
	}
	defer os.RemoveAll(dir)
	// pli keeps agent sessions in its config directory (Step 0, part 2);
	// on Linux os.UserConfigDir is $XDG_CONFIG_HOME.
	os.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	tokensPath = filepath.Join(dir, "dev-tokens.yaml")
	if err := os.WriteFile(tokensPath, []byte(tokensYAML), 0o600); err != nil {
		log.Print(err)
		return 1
	}

	path, handler := api.NewHandler(k, api.Options{Tokens: toks, Grants: grants})
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv = httptest.NewServer(mux)
	defer srv.Close()
	return m.Run()
}

// result is one pli run.
type result struct {
	code   int
	stdout string
	stderr string
}

// json decodes stdout.
func (r result) json(t *testing.T) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &out); err != nil {
		t.Fatalf("stdout is not JSON (%v): %q", err, r.stdout)
	}
	return out
}

func pli(t *testing.T, who string, args ...string) result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	all := append([]string{"--server", srv.URL, "--tokens", tokensPath, "--as", who}, args...)
	code := run(ctx, all, &stdout, &stderr)
	return result{code, stdout.String(), stderr.String()}
}

// ok runs pli and expects success.
func ok(t *testing.T, who string, args ...string) map[string]any {
	t.Helper()
	r := pli(t, who, args...)
	if r.code != 0 {
		t.Fatalf("pli %v as %s: exit %d, stderr %q", args, who, r.code, r.stderr)
	}
	return r.json(t)
}

// fails runs pli and expects an API error with this code and reason.
func fails(t *testing.T, who, codeReason string, args ...string) result {
	t.Helper()
	r := pli(t, who, args...)
	if r.code != 1 || !strings.HasPrefix(r.stderr, "error: "+codeReason) {
		t.Fatalf("pli %v as %s: exit %d, stderr %q; want exit 1, \"error: %s ...\"", args, who, r.code, r.stderr, codeReason)
	}
	return r
}

func itemOf(t *testing.T, out map[string]any) map[string]any {
	t.Helper()
	it, okay := out["item"].(map[string]any)
	if !okay {
		t.Fatalf("no item in %v", out)
	}
	return it
}

func word() string { return "w" + uuid.NewString()[:8] }

// One ticket, start to end, as one user: create, get (by bare ID), update,
// transition, search.
func TestPli_ItemLifecycle(t *testing.T) {
	w := word()
	created := itemOf(t, ok(t, "ana", "item", "create", "--type", "ticket@1",
		"--set", "f_title="+w, "--set", "f_priority=2"))
	name := created["name"].(string)
	if !strings.HasPrefix(name, "namespaces/acme/items/") || created["fields"].(map[string]any)["f_priority"] != "2" {
		t.Fatalf("created %v", created)
	}

	id := name[strings.LastIndex(name, "/")+1:]
	got := itemOf(t, ok(t, "ana", "item", "get", id))
	if got["name"] != name {
		t.Errorf("get by bare ID = %v, want %s", got["name"], name)
	}

	updated := ok(t, "ana", "item", "update", name, "--set", "f_assignee=user-bob", "--etag", created["etag"].(string))
	if updated["item_seq"] != "2" {
		t.Errorf("update item_seq = %v, want \"2\"", updated["item_seq"])
	}

	moved := ok(t, "ana", "item", "transition", name, "--to", "in_progress")
	if moved["outcome"] != "TRANSITION_OUTCOME_APPLIED" || itemOf(t, moved)["state"] != "in_progress" {
		t.Errorf("transition = %v, want APPLIED, in_progress", moved)
	}

	found := ok(t, "ana", "item", "search", w, "--min-seq", moved["commit_seq"].(string))
	items, _ := found["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["name"] != name {
		t.Errorf("search %s = %v, want %s", w, found, name)
	}
}

// --set reads JSON when it parses, else a string; null clears.
func TestPli_SetValues(t *testing.T) {
	name := itemOf(t, ok(t, "ana", "item", "create", "--type", "ticket@1",
		"--set", "f_title=hello world", "--set", "f_urgent=true", "--set", "f_reporter=\"42\""))["name"].(string)
	f := itemOf(t, ok(t, "ana", "item", "get", name))["fields"].(map[string]any)
	if f["f_title"] != "hello world" || f["f_urgent"] != true || f["f_reporter"] != "42" {
		t.Errorf("fields = %v", f)
	}
	f = itemOf(t, ok(t, "ana", "item", "update", name, "--set", "f_urgent=null"))["fields"].(map[string]any)
	if _, set := f["f_urgent"]; set {
		t.Errorf("f_urgent = %v after null, want it cleared", f["f_urgent"])
	}
}

// Readers, inherit and moves; another user's view follows.
func TestPli_ReadersAndMoves(t *testing.T) {
	ch := itemOf(t, ok(t, "ana", "item", "create", "--type", "channel@1", "--set", "f_name=ops"))["name"].(string)
	name := itemOf(t, ok(t, "ana", "item", "create", "--type", "ticket@1", "--in", ch))["name"].(string)

	ok(t, "ana", "item", "update", name, "--readers", "user:ana")
	fails(t, "bob", "NOT_FOUND NOT_FOUND", "item", "get", name)
	ok(t, "ana", "item", "update", name, "--inherit")
	ok(t, "bob", "item", "get", name)

	top := itemOf(t, ok(t, "ana", "item", "update", name, "--move-to-top"))
	if c, _ := top["container"].(string); c != "" {
		t.Errorf("container after --move-to-top = %q, want none", c)
	}
	back := itemOf(t, ok(t, "ana", "item", "update", name, "--move-to", ch))
	if back["container"] != ch {
		t.Errorf("container = %v, want %s", back["container"], ch)
	}

	narrowed := itemOf(t, ok(t, "ana", "item", "create", "--type", "ticket@1", "--narrow", "user:ana"))["name"].(string)
	fails(t, "bob", "NOT_FOUND NOT_FOUND", "item", "get", narrowed)
}

// A held transition reports PENDING and the hold; a write to a frozen field
// prints HELD and the hold's name. Rule refusals print their code.
func TestPli_HeldAndRefusals(t *testing.T) {
	name := itemOf(t, ok(t, "ana", "item", "create", "--type", "ticket@3",
		"--set", "f_customer_id=cust-1", "--set", "f_amount=100", "--set", "f_assignee=user-bob"))["name"].(string)
	ok(t, "ana", "item", "transition", name, "--to", "open")
	ok(t, "ana", "item", "transition", name, "--to", "in_progress")

	fails(t, "bob", "PERMISSION_DENIED NOT_ALLOWED", "item", "transition", name, "--to", "approved")
	held := ok(t, "ana", "item", "transition", name, "--to", "approved", "--wait", "0s")
	hold, _ := held["hold"].(string)
	if held["outcome"] != "TRANSITION_OUTCOME_PENDING" || !strings.HasPrefix(hold, name+"/holds/") {
		t.Fatalf("approve = %v, want PENDING with a hold", held)
	}
	r := fails(t, "ana", "ABORTED HELD", "item", "update", name, "--set", "f_amount=5")
	if !strings.Contains(r.stderr, hold) {
		t.Errorf("stderr %q does not name the hold %s", r.stderr, hold)
	}

	open := itemOf(t, ok(t, "ana", "item", "create", "--type", "ticket@1"))["name"].(string)
	fails(t, "ana", "FAILED_PRECONDITION GUARD_FAILED", "item", "transition", open, "--to", "in_progress")
	fails(t, "ana", "INVALID_ARGUMENT INVALID_REQUEST", "item", "update", open, "--set", "f_priority=high")
	fails(t, "ana", "ABORTED STALE", "item", "update", open, "--set", "f_priority=1", "--etag", "c7")
}

// --request-id makes a retry safe: the same create twice is one Item.
func TestPli_RequestIDReplays(t *testing.T) {
	id := "req-" + uuid.NewString()
	a := ok(t, "ana", "item", "create", "--type", "ticket@1", "--set", "f_title=once", "--request-id", id)
	b := ok(t, "ana", "item", "create", "--type", "ticket@1", "--set", "f_title=once", "--request-id", id)
	if itemOf(t, a)["name"] != itemOf(t, b)["name"] || a["commit_seq"] != b["commit_seq"] {
		t.Errorf("replay = %v / %v, want the same Item and commit_seq", a, b)
	}
	fails(t, "ana", "ALREADY_EXISTS IDEMPOTENCY_KEY_REUSED",
		"item", "create", "--type", "ticket@1", "--set", "f_title=twice", "--request-id", id)
}

func TestPli_Types(t *testing.T) {
	list := ok(t, "bob", "type", "list")
	var names []string
	for _, ty := range list["types"].([]any) {
		names = append(names, ty.(map[string]any)["name"].(string))
	}
	if !slices.Contains(names, "namespaces/acme/types/ticket/versions/3") || len(names) != 38 {
		t.Errorf("type list = %v, want the 38 Types of types/", names)
	}
	ty := ok(t, "bob", "type", "get", "ticket@3")["type"].(map[string]any)
	if ty["initial_state"] != "triage" {
		t.Errorf("type get ticket@3 = %v", ty)
	}
}

// Usage errors exit 2 and say what is wrong; nothing is sent.
func TestPli_UsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string
	}{
		{"unknown user", []string{"--as", "nobody", "type", "list"}, "nobody"},
		{"no command", []string{"--as", "ana"}, "usage"},
		{"unknown command", []string{"--as", "ana", "item", "frobnicate"}, "frobnicate"},
		{"transition without --to", []string{"--as", "ana", "item", "transition", uuid.NewString()}, "--to"},
		{"--set without =", []string{"--as", "ana", "item", "create", "--type", "ticket@1", "--set", "f_title"}, "--set"},
		{"bad type shorthand", []string{"--as", "ana", "item", "create", "--type", "ticket"}, "--type"},
		{"readers and inherit", []string{"--as", "ana", "item", "update", uuid.NewString(), "--readers", "user:ana", "--inherit"}, "--inherit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			args := append([]string{"--server", srv.URL, "--tokens", tokensPath}, c.args...)
			code := run(context.Background(), args, &stdout, &stderr)
			if code != 2 || !strings.Contains(stderr.String(), c.want) {
				t.Errorf("exit %d, stderr %q; want exit 2 mentioning %q", code, stderr.String(), c.want)
			}
		})
	}
}
