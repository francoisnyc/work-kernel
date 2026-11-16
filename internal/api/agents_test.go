// Tests for Step 0, part 2: agent sessions over the wire (capability mode).
//
//   - StartAgentSession is called with an agent's token. It names the user
//     the agent acts for, the scopes (container names, or namespaces/{ns})
//     and the powers. Both must fit the user's grant (dev config), else
//     PERMISSION_DENIED / NOT_ALLOWED.
//   - Every later call carries the agent's token and the session ID in the
//     Pli-Agent-Session header. The header with a user's token, or an
//     agent's token without it, is INVALID_ARGUMENT naming the header. An
//     unknown session, another agent's, or another namespace's is
//     UNAUTHENTICATED / SESSION_INVALID: a bad credential.
//   - Outside its scopes everything is NOT_FOUND; a read-only session's
//     writes are NOT_ALLOWED.
//   - NEEDS_APPROVAL names the scope in metadata ("scope": a container's name
//     or namespaces/{ns}); the session's user answers with ApproveWiden.
package api_test

import (
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/api"
	"github.com/francoisnyc/work-kernel/internal/devtoken"
)

// startSession starts a session as the named agent, acting for user, and
// returns its ID.
func startSession(t *testing.T, agent, user string, scopes []string, powers ...string) string {
	t.Helper()
	ns := agentNamespace(agent)
	res, err := asAgent(agent, "").StartAgentSession(testCtx(t), connect.NewRequest(&kernelv1.StartAgentSessionRequest{
		Parent: "namespaces/" + ns, ActingFor: user, Scopes: scopes, Powers: powers,
	}))
	if err != nil {
		t.Fatalf("StartAgentSession as %s for %s: %v", agent, user, err)
	}
	if res.Msg.Session.GetId() == "" {
		t.Fatalf("StartAgentSession returned no session ID: %v", res.Msg)
	}
	return res.Msg.Session.Id
}

func agentNamespace(agent string) string {
	i := slices.IndexFunc(agentTokens, func(tk devtoken.Token) bool { return tk.Name == agent })
	return agentTokens[i].Namespace
}

// headerError checks an INVALID_ARGUMENT that names the session header.
func headerError(t *testing.T, what string, err error) {
	t.Helper()
	ce := apiError(t, what, err, connect.CodeInvalidArgument, "INVALID_REQUEST")
	if info := errorInfo(t, ce); info == nil || info.Metadata["header"] != api.AgentSessionHeader {
		t.Errorf("%s: metadata %v, want header = %s", what, info.GetMetadata(), api.AgentSessionHeader)
	}
}

// The response describes the session as asked.
func TestAgentSession_Start(t *testing.T) {
	box := create(t, "ana", ticketV1, nil).Item.Name
	res, err := asAgent("bot", "").StartAgentSession(testCtx(t), connect.NewRequest(&kernelv1.StartAgentSessionRequest{
		Parent: "namespaces/acme", ActingFor: "user:ana", Scopes: []string{box}, Powers: []string{"read", "transition:approved"},
	}))
	if err != nil {
		t.Fatalf("StartAgentSession: %v", err)
	}
	s := res.Msg.Session
	if s.Id == "" || s.Name != "namespaces/acme/agentSessions/"+s.Id || s.Agent != "agent:triage-bot" ||
		s.ActingFor != "user:ana" || !slices.Equal(s.Scopes, []string{box}) ||
		!slices.Equal(s.Powers, []string{"read", "transition:approved"}) {
		t.Errorf("session = %v", s)
	}
}

// Malformed starts are INVALID_ARGUMENT naming the field; a user cannot
// start a session; a session beyond the grant is NOT_ALLOWED; a scope the
// user cannot see is NOT_FOUND.
func TestAgentSession_StartRefusals(t *testing.T) {
	ctx := testCtx(t)
	start := func(agent string, r *kernelv1.StartAgentSessionRequest) error {
		_, err := asAgent(agent, "").StartAgentSession(ctx, connect.NewRequest(r))
		return err
	}
	acme := []string{"namespaces/acme"}
	req := func(user string, scopes []string, powers ...string) *kernelv1.StartAgentSessionRequest {
		return &kernelv1.StartAgentSessionRequest{Parent: "namespaces/acme", ActingFor: user, Scopes: scopes, Powers: powers}
	}

	invalid(t, "no scopes", start("bot", req("user:ana", nil, "read")), "scopes")
	invalid(t, "malformed scope", start("bot", req("user:ana", []string{"acme"}, "read")), "scopes")
	invalid(t, "no powers", start("bot", req("user:ana", acme)), "powers")
	invalid(t, "unknown power", start("bot", req("user:ana", acme, "read", "launch_missiles")), "powers")
	invalid(t, "acting for an agent", start("bot", req("agent:helper", acme, "read")), "acting_for")

	_, err := as("ana").StartAgentSession(ctx, connect.NewRequest(req("user:ana", acme, "read")))
	malformed(t, "a user starting a session", err)

	notAllowed := func(what string, err error) {
		t.Helper()
		apiError(t, what, err, connect.CodePermissionDenied, "NOT_ALLOWED")
	}
	notAllowed("a power bob did not grant", start("bot", req("user:bob", acme, "read", "edit_fields")))
	notAllowed("no grant for this user", start("helper", req("user:bob", acme, "read")))
	notAllowed("no grant to act for itself", start("bot", req("", acme, "read")))

	hidden := createNarrowed(t, "bob", "user:bob")
	notFound(t, "a scope ana cannot see", start("bot", req("user:ana", []string{hidden}, "read")))
	notFound(t, "a scope in globex", start("bot", req("user:ana", []string{"namespaces/globex"}, "read")))
	_, err = asAgent("bot", "").StartAgentSession(ctx, connect.NewRequest(&kernelv1.StartAgentSessionRequest{
		Parent: "namespaces/globex", ActingFor: "user:ana", Scopes: acme, Powers: []string{"read"},
	}))
	notFound(t, "a session in another namespace", err)
}

// createNarrowed creates a ticket readable by the given principals only.
func createNarrowed(t *testing.T, who string, readers ...string) string {
	t.Helper()
	res, err := as(who).CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
		Narrow: &kernelv1.Readers{Principals: readers},
	}))
	if err != nil {
		t.Fatalf("create narrowed as %s: %v", who, err)
	}
	return res.Msg.Item.Name
}

// The header goes with an agent's token and only with it.
func TestAgentSession_Header(t *testing.T) {
	ctx := testCtx(t)
	it := create(t, "ana", ticketV1, nil).Item.Name
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read")

	_, err := asAgent("bot", "").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: it}))
	headerError(t, "an agent without the header", err)
	_, err = withToken("tok-ana", sessionHeader(session)).GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: it}))
	headerError(t, "a user with the header", err)

	sessionInvalid := func(what string, err error) {
		t.Helper()
		apiError(t, what, err, connect.CodeUnauthenticated, "SESSION_INVALID")
	}
	_, err = asAgent("bot", "ses_made-up").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: it}))
	sessionInvalid("an unknown session", err)
	_, err = asAgent("helper", session).GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: it}))
	sessionInvalid("another agent's session", err)
	_, err = asAgent("bot-globex", session).GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{
		Name: "namespaces/globex/items/" + itemID(t, it).String(),
	}))
	sessionInvalid("the same agent's session from another namespace", err)

	// With the header, it works.
	if _, err := asAgent("bot", session).GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: it})); err != nil {
		t.Errorf("GetItem with the session: %v", err)
	}
}

// Every operation with an agent token and no header is refused before it
// runs, StartAgentSession aside.
func TestAgentSession_EveryOperationNeedsTheHeader(t *testing.T) {
	it := create(t, "ana", ticketV1, nil).Item.Name
	for name, call := range calls(asAgent("bot", ""), it) {
		if name == "StartAgentSession" {
			continue
		}
		t.Run(name, func(t *testing.T) { headerError(t, name, call(testCtx(t))) })
	}
}

// Inside its scopes the session reads and writes; outside, nothing exists.
func TestAgentSession_Scopes(t *testing.T) {
	ctx := testCtx(t)
	w := word()
	box := create(t, "ana", ticketV1, map[string]any{"f_title": w + " box"}).Item.Name
	inside := createIn(t, "ana", box, map[string]any{"f_title": w + " inside"})
	outside := create(t, "ana", ticketV1, map[string]any{"f_title": w + " outside"}).Item.Name
	session := startSession(t, "bot", "user:ana", []string{box}, "read", "edit_fields", "create")
	bot := asAgent("bot", session)

	for _, name := range []string{box, inside} {
		if _, err := bot.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: name})); err != nil {
			t.Errorf("GetItem %s inside: %v", name, err)
		}
	}
	_, err := bot.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: outside}))
	notFound(t, "GetItem outside", err)
	_, err = bot.ListItemEvents(ctx, connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: outside}))
	notFound(t, "ListItemEvents outside", err)
	_, err = bot.ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{Parent: "namespaces/acme"}))
	notFound(t, "ListItems of the top level", err)
	_, err = bot.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: outside, RequestId: newID(), Fields: fields(t, map[string]any{"f_title": "bot"}),
	}))
	notFound(t, "UpdateItem outside", err)
	_, err = bot.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
	}))
	notFound(t, "CreateItem at the top level", err)

	found, err := bot.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{Parent: "namespaces/acme", Query: w}))
	if err != nil {
		t.Fatalf("SearchItems: %v", err)
	}
	var names []string
	for _, it := range found.Msg.Items {
		names = append(names, it.Name)
	}
	slices.Sort(names)
	want := []string{box, inside}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Errorf("SearchItems = %v, want only %v", names, want)
	}

	if _, err := bot.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: inside, RequestId: newID(), Fields: fields(t, map[string]any{"f_title": "bot was here"}),
	})); err != nil {
		t.Errorf("UpdateItem inside: %v", err)
	}
	made, err := bot.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: box,
	}))
	if err != nil {
		t.Fatalf("CreateItem inside: %v", err)
	}
	if it := made.Msg.Item; it.Creator != "agent:triage-bot" || it.CreatorOnBehalfOf != "user:ana" {
		t.Errorf("the bot's Item: creator %q on behalf of %q, want agent:triage-bot for user:ana", it.Creator, it.CreatorOnBehalfOf)
	}
}

// createIn creates a ticket in a container as who and returns its name.
func createIn(t *testing.T, who, container string, f map[string]any) string {
	t.Helper()
	res, err := as(who).CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: container, Fields: fields(t, f),
	}))
	if err != nil {
		t.Fatalf("CreateItem in %s as %s: %v", container, who, err)
	}
	return res.Msg.Item.Name
}

// A session with only read cannot write: NOT_ALLOWED, nothing written.
func TestAgentSession_ReadOnly(t *testing.T) {
	ctx := testCtx(t)
	it := create(t, "bob", ticketV1, nil).Item.Name
	bot := asAgent("bot", startSession(t, "bot", "user:bob", []string{"namespaces/acme"}, "read"))
	if _, err := bot.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: it})); err != nil {
		t.Fatalf("GetItem: %v", err)
	}
	_, err := bot.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: it, RequestId: newID(), Fields: fields(t, map[string]any{"f_title": "nope"}),
	}))
	apiError(t, "UpdateItem read-only", err, connect.CodePermissionDenied, "NOT_ALLOWED")
	_, err = bot.TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: it, RequestId: newID(), ToState: "canceled",
	}))
	apiError(t, "TransitionItem read-only", err, connect.CodePermissionDenied, "NOT_ALLOWED")
	if n := countEvents(t, it); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}

// After a narrowing read, a wider write is NEEDS_APPROVAL naming the target
// scope in metadata, with the fixed message; the session's user approves
// that scope and the retry works.
func TestAgentSession_NeedsApprovalAndApproveWiden(t *testing.T) {
	ctx := testCtx(t)
	private := createNarrowed(t, "ana", "user:ana", "agent:triage-bot")
	open := create(t, "ana", ticketV1, nil).Item.Name
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read", "create", "edit_fields")
	bot := asAgent("bot", session)
	if _, err := bot.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: private})); err != nil {
		t.Fatalf("the bot reads the private Item: %v", err)
	}

	needsApproval := func(what string, err error, scope string) {
		t.Helper()
		ce := apiError(t, what, err, connect.CodePermissionDenied, "NEEDS_APPROVAL")
		if info := errorInfo(t, ce); info.GetMetadata()["scope"] != scope {
			t.Errorf("%s: metadata %v, want scope = %s", what, info.GetMetadata(), scope)
		}
		if strings.Contains(ce.Message(), itemID(t, open).String()) || strings.Contains(ce.Message(), "namespaces/") {
			t.Errorf("%s: message %q names the scope; it belongs in metadata", what, ce.Message())
		}
	}
	createInOpen := func() error {
		_, err := bot.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: open,
		}))
		return err
	}
	needsApproval("create into a wider container", createInOpen(), open)
	_, err := bot.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
	}))
	needsApproval("create at the top level", err, "namespaces/acme")

	approve := func(who, parent, scope string) error {
		_, err := as(who).ApproveWiden(ctx, connect.NewRequest(&kernelv1.ApproveWidenRequest{RequestId: newID(),
			Parent: parent, Session: session, Scope: scope, ExpectedAudience: "namespace",
		}))
		return err
	}
	// Someone in another namespace, or naming another namespace, finds no
	// such session.
	notFound(t, "approval from globex", approve("bea", "namespaces/globex", open))
	notFound(t, "approval naming acme from globex", approve("bea", "namespaces/acme", open))
	notFound(t, "approval of an unknown session", func() error {
		_, err := as("ana").ApproveWiden(ctx, connect.NewRequest(&kernelv1.ApproveWidenRequest{RequestId: newID(),
			Parent: "namespaces/acme", Session: "ses_made-up", Scope: open, ExpectedAudience: "namespace",
		}))
		return err
	}())
	_, err = asAgent("bot", session).ApproveWiden(ctx, connect.NewRequest(&kernelv1.ApproveWidenRequest{RequestId: newID(),
		Parent: "namespaces/acme", Session: session, Scope: open, ExpectedAudience: "namespace",
	}))
	malformed(t, "an agent approving", err)
	invalid(t, "a malformed scope", approve("ana", "namespaces/acme", "open"), "scope")

	if err := approve("ana", "namespaces/acme", open); err != nil {
		t.Fatalf("ana approves: %v", err)
	}
	if err := createInOpen(); err != nil {
		t.Errorf("create after approval: %v", err)
	}
	if err := approve("ana", "namespaces/acme", "namespaces/acme"); err != nil {
		t.Fatalf("ana approves the root: %v", err)
	}
	if _, err := bot.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
	})); err != nil {
		t.Errorf("create at the top level after approval: %v", err)
	}
}

// ApproveWiden requires a request_id like every write; a replay answers the
// same; the key with another request is ALREADY_EXISTS.
func TestAgentSession_ApproveWidenTakesARequestID(t *testing.T) {
	ctx := testCtx(t)
	open := create(t, "ana", ticketV1, nil).Item.Name
	other := create(t, "ana", ticketV1, nil).Item.Name
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read")
	approve := func(id, scope string) error {
		_, err := as("ana").ApproveWiden(ctx, connect.NewRequest(&kernelv1.ApproveWidenRequest{
			Parent: "namespaces/acme", Session: session, Scope: scope, RequestId: id, ExpectedAudience: "namespace",
		}))
		return err
	}
	invalid(t, "no request_id", approve("", open), "request_id")
	id := newID()
	if err := approve(id, open); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := approve(id, open); err != nil {
		t.Errorf("replay: %v", err)
	}
	apiError(t, "same request_id, other scope", approve(id, other), connect.CodeAlreadyExists, "IDEMPOTENCY_KEY_REUSED")
}

// NEEDS_APPROVAL names the audience the approval would open the content to:
// "namespace" for an open scope, "restricted" for a narrowed one. A
// validate_only ApproveWiden says the same and records nothing; a real one
// answers it too.
func TestAgentSession_NeedsApprovalNamesTheAudience(t *testing.T) {
	ctx := testCtx(t)
	private := createNarrowed(t, "ana", "user:ana", "agent:triage-bot")
	open := create(t, "ana", ticketV1, nil).Item.Name
	room := createNarrowed(t, "ana", "user:ana", "agent:triage-bot")
	session := startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read", "create")
	bot := asAgent("bot", session)
	if _, err := bot.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: private})); err != nil {
		t.Fatalf("the bot reads the private Item: %v", err)
	}
	createIn := func(container string) error {
		_, err := bot.CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: container,
		}))
		return err
	}
	audienceOf := func(what string, err error, scope, audience string) {
		t.Helper()
		ce := apiError(t, what, err, connect.CodePermissionDenied, "NEEDS_APPROVAL")
		if md := errorInfo(t, ce).GetMetadata(); md["scope"] != scope || md["audience"] != audience {
			t.Errorf("%s: metadata %v, want scope %s, audience %s", what, md, scope, audience)
		}
	}
	audienceOf("into an open container", createIn(open), open, "namespace")
	audienceOf("at the top level", createIn(""), "namespaces/acme", "namespace")
	audienceOf("into a narrowed container", createIn(room), room, "restricted")

	approve := func(scope string, validateOnly bool) (*kernelv1.ApproveWidenResponse, error) {
		res, err := as("ana").ApproveWiden(ctx, connect.NewRequest(&kernelv1.ApproveWidenRequest{
			Parent: "namespaces/acme", Session: session, Scope: scope, RequestId: newID(), ValidateOnly: validateOnly,
			ExpectedAudience: "namespace", // its one real approval is of an open Item
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg, nil
	}
	if res, err := approve(open, true); err != nil || res.Audience != "namespace" {
		t.Errorf("validate_only, open: %v, %v; want audience namespace", res, err)
	}
	if res, err := approve(room, true); err != nil || res.Audience != "restricted" {
		t.Errorf("validate_only, narrowed: %v, %v; want audience restricted", res, err)
	}
	audienceOf("after validate_only, nothing is approved", createIn(open), open, "namespace")
	if res, err := approve(open, false); err != nil || res.Audience != "namespace" {
		t.Errorf("approve, open: %v, %v; want audience namespace", res, err)
	}
	if err := createIn(open); err != nil {
		t.Errorf("create after approval: %v", err)
	}
}
