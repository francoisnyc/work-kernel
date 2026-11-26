// Tests for the reads an inbox screen needs, over the wire (docs/roadmap.md,
// track 1, item 5, step 4, PR 1). The contract grows by exactly:
//
//   - Item.state_time (15): when the Item entered its state; on every read
//     and every write's answer, for staff, an agent and a customer.
//   - Item.moves (16): the moves the caller may make, filled only in an
//     answer to SearchItems or ListItems with filter.waiting_on.
//   - ItemFilter.in (7): the Items below a container; SearchItems and
//     CountItems only; a hidden container is a missing one.
//   - rpc CountItems: a search's words and filter, counted, capped at 50.
//   - rpc WhoAmI: the caller's principal, namespace and roles; an agent's
//     user and session. Nothing else.
//
// No new error reason. The rules are tested in internal/itemservice. (The
// file's name starts with "waiting": see waiting_claim_test.go.)
package api_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
)

// rolesOf reads a principal's roles in acme, sorted: what WhoAmI must say.
func rolesOf(t *testing.T, principal string) []string {
	t.Helper()
	rows, err := pool.Query(testCtx(t),
		`select role from principal_roles where namespace_id = 'acme' and principal = $1 order by role collate "C"`, principal)
	if err != nil {
		t.Fatalf("read roles: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var r string
		if err := rows.Scan(&r); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	return out
}

// msg is a response's message, or an empty one when the call failed: a
// test then reports the error instead of stopping the package on a nil.
// (A generic function: T is any message type, as in Java's <T>.)
func msg[T any](res *connect.Response[T]) *T {
	if res == nil {
		return new(T)
	}
	return res.Msg
}

func sameTime(a, b *timestamppb.Timestamp) bool {
	return a != nil && b != nil && a.AsTime().Equal(b.AsTime())
}

func TestStateTime_OverTheWire(t *testing.T) {
	apiClaimSetup(t)
	ctx := testCtx(t)
	w := word()
	created := create(t, "sam-acme", apiClaimV1, map[string]any{"f_title": "state time " + w}).Item
	if !sameTime(created.StateTime, created.CreateTime) {
		t.Errorf("a create's answer: state_time %v, create_time %v; want the same instant", created.StateTime, created.CreateTime)
	}
	moved := transition(t, "bob", created.Name, "review").Item
	if moved.StateTime == nil || !moved.StateTime.AsTime().After(created.CreateTime.AsTime()) {
		t.Errorf("a transition's answer: state_time %v, want later than the create, %v", moved.StateTime, created.CreateTime)
	}
	want := moved.StateTime

	bot := asAgent("bot", startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "read"))
	for _, c := range []struct {
		who    string
		client kernelv1connect.ItemServiceClient
	}{{"bob", as("bob")}, {"ana", as("ana")}, {"the bot for ana", bot}} {
		got, err := c.client.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: created.Name}))
		if err != nil || !sameTime(msg(got).GetItem().GetStateTime(), want) {
			t.Errorf("GetItem as %s: state_time %v, error %v; want %v", c.who, msg(got).GetItem().GetStateTime(), err, want)
		}
		found, err := c.client.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{Parent: "namespaces/acme", Query: w}))
		if err != nil || len(msg(found).Items) != 1 || !sameTime(msg(found).Items[0].StateTime, want) {
			t.Errorf("SearchItems as %s: %d Items, error %v; want the one Item with state_time %v", c.who, len(msg(found).GetItems()), err, want)
		}
	}
	listed, err := as("bob").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{Types: []string{"namespaces/acme/types/apicl"}, States: []string{"review"}}, PageSize: 500,
	}))
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	i := slices.IndexFunc(msg(listed).Items, func(it *kernelv1.Item) bool { return it.Name == created.Name })
	if i < 0 {
		t.Errorf("ListItems of the claimed cases does not hold the case")
	} else if got := msg(listed).Items[i].StateTime; !sameTime(got, want) {
		t.Errorf("ListItems: state_time %v, want %v", got, want)
	}

	// A customer sees it on her own incident.
	desk := portal(t)
	inc := kimsIncident(t, desk)
	got, err := asCustomer("kim", "").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: inc}))
	if err != nil || !sameTime(msg(got).GetItem().GetStateTime(), msg(got).GetItem().GetCreateTime()) {
		t.Errorf("kim's GetItem of her new incident: state_time %v, create_time %v, error %v; want the same instant",
			msg(got).GetItem().GetStateTime(), msg(got).GetItem().GetCreateTime(), err)
	}
}

func TestMoves_OverTheWire(t *testing.T) {
	apiClaimSetup(t)
	ctx := testCtx(t)
	w := word()
	name := create(t, "sam-acme", apiClaimV1, map[string]any{"f_title": "moves " + w}).Item.Name
	waiting := func(who, on string) *kernelv1.Item {
		t.Helper()
		res, err := as(who).SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Query: w, Filter: &kernelv1.ItemFilter{WaitingOn: on},
		}))
		if err != nil {
			t.Fatalf("%s searches waiting on %s: %v", who, on, err)
		}
		if len(msg(res).Items) == 0 {
			return nil
		}
		return msg(res).Items[0]
	}
	// In the queue: the team claims it. sam asked for the queue and holds
	// no role: no move is his.
	if it := waiting("bob", "me"); it == nil || !slices.Equal(it.Moves, []string{"review"}) {
		t.Errorf("bob, in the team, a case in the queue: %v; want moves [review]", it.GetMoves())
	}
	if it := waiting("sam-acme", "role:apicl_team"); it == nil || len(it.Moves) != 0 {
		t.Errorf("sam reads the team's queue: %v; want the case with no moves", it.GetMoves())
	}
	transition(t, "bob", name, "review")
	// Claimed by bob: his to release or finish; out of ana's view.
	if it := waiting("bob", "me"); it == nil || !slices.Equal(it.Moves, []string{"queue", "done"}) {
		t.Errorf("bob, the claimant: %v; want moves [queue done]", it.GetMoves())
	}
	if it := waiting("ana", "me"); it != nil {
		t.Errorf("ana, bob's colleague, finds the case bob claimed in her view, with moves %v", it.Moves)
	}
	// ListItems with waiting_on fills it too.
	listed, err := as("bob").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{Types: []string{"namespaces/acme/types/apicl"}, WaitingOn: "me"}, PageSize: 500,
	}))
	if err != nil {
		t.Fatalf("ListItems waiting on me: %v", err)
	}
	if i := slices.IndexFunc(msg(listed).Items, func(it *kernelv1.Item) bool { return it.Name == name }); i < 0 ||
		!slices.Equal(msg(listed).Items[i].Moves, []string{"queue", "done"}) {
		t.Errorf("ListItems waiting on me: the case is at %d; want it there with moves [queue done]", i)
	}

	// Everywhere else the field is empty: GetItem, a search without
	// waiting_on, a write's answer.
	got, err := as("bob").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: name}))
	if err != nil || len(msg(got).GetItem().GetMoves()) != 0 {
		t.Errorf("GetItem: moves %v, error %v; want none", msg(got).GetItem().GetMoves(), err)
	}
	found, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{Parent: "namespaces/acme", Query: w}))
	if err != nil || len(msg(found).Items) != 1 || len(msg(found).Items[0].Moves) != 0 {
		t.Errorf("SearchItems without waiting_on: %d Items, error %v; want the case with no moves", len(msg(found).GetItems()), err)
	}
	if back := transition(t, "bob", name, "queue").Item; len(back.Moves) != 0 {
		t.Errorf("a transition's answer: moves %v, want none", back.Moves)
	}
}

func TestFilterIn_OverTheWire(t *testing.T) {
	ctx := testCtx(t)
	w := word()
	box := create(t, "ana", ticketV1, map[string]any{"f_title": "box " + w}).Item.Name
	inside := createIn(t, "ana", box, map[string]any{"f_title": "inside " + w})
	inner := createIn(t, "ana", inside, map[string]any{"f_title": "deeper " + w})
	create(t, "ana", ticketV1, map[string]any{"f_title": "outside " + w})
	vault := createNarrowed(t, "ana", "user:ana")

	res, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: box},
	}))
	if err != nil || !sameNames(names(msg(res).GetItems()), []string{inside, inner}) {
		t.Errorf("below the box: %v, error %v; want the two Items inside it, at both depths, and not the box", names(msg(res).GetItems()), err)
	}
	// With words: the filter narrows the words' matches.
	res, err = as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: &kernelv1.ItemFilter{In: inside},
	}))
	if err != nil || !sameNames(names(msg(res).GetItems()), []string{inner}) {
		t.Errorf("the word, below the inner box: %v, error %v; want the deepest Item only", names(msg(res).GetItems()), err)
	}

	// A container bob cannot see and one that does not exist: NOT_FOUND,
	// the same answer byte for byte.
	missing := "namespaces/acme/items/00000000-0000-7000-8000-000000000000"
	_, hiddenErr := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: vault},
	}))
	_, missingErr := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: missing},
	}))
	h := apiError(t, "below a container bob cannot see", hiddenErr, connect.CodeNotFound, "NOT_FOUND")
	m := apiError(t, "below a container that does not exist", missingErr, connect.CodeNotFound, "NOT_FOUND")
	if h.Message() != m.Message() || !proto.Equal(errorInfo(t, h), errorInfo(t, m)) {
		t.Errorf("hidden: %q %v; missing: %q %v; want the same answer", h.Message(), errorInfo(t, h), m.Message(), errorInfo(t, m))
	}
	// Another namespace's Item, and a name that is no Item name.
	_, err = as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: "namespaces/globex/items/00000000-0000-7000-8000-000000000000"},
	}))
	apiError(t, "below another namespace's Item", err, connect.CodeNotFound, "NOT_FOUND")
	_, err = as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: "the box"},
	}))
	invalid(t, "filter.in that is no Item name", err, "filter.in")
	// ListItems has its parent: the filter is refused there.
	_, err = as("bob").ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: box},
	}))
	invalid(t, "ListItems with filter.in", err, "filter.in")

	// An agent searches below a container only inside its session's scopes.
	bot := asAgent("bot", startSession(t, "bot", "user:ana", []string{inside}, "read"))
	got, err := bot.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: inside},
	}))
	if err != nil || !sameNames(names(msg(got).GetItems()), []string{inner}) {
		t.Errorf("the bot, below the container it is scoped to: %v, error %v; want the deepest Item", names(msg(got).GetItems()), err)
	}
	_, err = bot.SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: box},
	}))
	apiError(t, "the bot, below a container outside its scopes", err, connect.CodeNotFound, "NOT_FOUND")
}

func TestCountItems(t *testing.T) {
	apiWaitSetup(t)
	ctx := testCtx(t)
	w := word()
	box := create(t, "ana", ticketV1, map[string]any{"f_title": "count box"}).Item.Name
	// 52 requests for the team with one word, and three tickets with the
	// same word in a box.
	for range 52 {
		create(t, "sam-acme", apiWaitV1, map[string]any{"f_title": "counted " + w})
	}
	for range 3 {
		createIn(t, "ana", box, map[string]any{"f_title": "boxed " + w})
	}
	createNarrowed(t, "ana", "user:ana") // not bob's to see, and it has no word
	count := func(who string, req *kernelv1.CountItemsRequest) *kernelv1.CountItemsResponse {
		t.Helper()
		req.Parent = "namespaces/acme"
		res, err := as(who).CountItems(ctx, connect.NewRequest(req))
		if err != nil {
			t.Fatalf("CountItems %v as %s: %v", req, who, err)
		}
		return msg(res)
	}
	for _, c := range []struct {
		what   string
		who    string
		req    *kernelv1.CountItemsRequest
		count  int32
		capped bool
	}{
		{"the word: 55 match", "bob", &kernelv1.CountItemsRequest{Query: w}, 50, true},
		{"the word, below the box", "bob", &kernelv1.CountItemsRequest{Query: w, Filter: &kernelv1.ItemFilter{In: box}}, 3, false},
		{"below the box, no words", "bob", &kernelv1.CountItemsRequest{Filter: &kernelv1.ItemFilter{In: box}}, 3, false},
		{"the word, waiting on bob (in the team)", "bob", &kernelv1.CountItemsRequest{Query: w, Filter: &kernelv1.ItemFilter{WaitingOn: "me"}}, 50, true},
		{"the word, waiting on sam (who asked)", "sam-acme", &kernelv1.CountItemsRequest{Query: w, Filter: &kernelv1.ItemFilter{WaitingOn: "me"}}, 0, false},
		{"the word, in the team's queue, asked by sam", "sam-acme", &kernelv1.CountItemsRequest{Query: w, Filter: &kernelv1.ItemFilter{WaitingOn: "role:apiwq_team"}}, 50, true},
		{"a word nothing holds", "bob", &kernelv1.CountItemsRequest{Query: word()}, 0, false},
	} {
		got := count(c.who, c.req)
		if got.Count != c.count || got.Capped != c.capped {
			t.Errorf("CountItems %s = %d (capped %v), want %d (capped %v)", c.what, got.Count, got.Capped, c.count, c.capped)
		}
	}
	// Below the cap a count is what the search returns.
	found, err := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: w, Filter: &kernelv1.ItemFilter{In: box}, PageSize: 500,
	}))
	if err != nil || len(msg(found).Items) != 3 {
		t.Errorf("the search the count stands for returns %d Items (error %v), want 3", len(msg(found).GetItems()), err)
	}

	// Refused where SearchItems is, with the same answer.
	for what, req := range map[string]*kernelv1.CountItemsRequest{
		"no words and no filter": {Parent: "namespaces/acme"},
		"waiting on someone":     {Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{WaitingOn: "user:ana"}},
		"another namespace":      {Parent: "namespaces/globex", Query: w},
		"a hidden container":     {Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: "namespaces/acme/items/00000000-0000-7000-8000-000000000000"}},
	} {
		_, countErr := as("bob").CountItems(ctx, connect.NewRequest(req))
		_, searchErr := as("bob").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: req.Parent, Query: req.Query, Filter: req.Filter,
		}))
		if countErr == nil || searchErr == nil || countErr.Error() != searchErr.Error() {
			t.Errorf("CountItems with %s: %v; SearchItems: %v; want the same refusal", what, countErr, searchErr)
		}
	}
	// A customer has no role's queue, counted or not. Her own view (step
	// 5) is counted in waiting_sent_back_test.go.
	_, err = asCustomer("kim", "").CountItems(ctx, connect.NewRequest(&kernelv1.CountItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{WaitingOn: "role:apiwq_team"},
	}))
	invalid(t, "a customer's CountItems waiting on role:apiwq_team", err, "filter.waiting_on")
	// And counts her own requests like anyone their searches.
	desk := portal(t)
	kimsIncident(t, desk)
	kims, err := asCustomer("kim", "").CountItems(ctx, connect.NewRequest(&kernelv1.CountItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: desk},
	}))
	if err != nil || msg(kims).Count != 1 || msg(kims).Capped {
		t.Errorf("kim counts below her portal: %v, error %v; want 1", msg(kims), err)
	}
	lees, err := asCustomer("lee", "").CountItems(ctx, connect.NewRequest(&kernelv1.CountItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{In: desk},
	}))
	if err != nil || msg(lees).Count != 0 {
		t.Errorf("lee counts below the portal: %v, error %v; want 0: kim's incident is not his", msg(lees), err)
	}
}

func TestWhoAmI_OverTheWire(t *testing.T) {
	apiClaimSetup(t)
	ctx := testCtx(t)
	// A user: principal, namespace, roles, and nothing else. proto.Equal
	// fails on any field the answer has beyond these.
	got, err := as("bob").WhoAmI(ctx, connect.NewRequest(&kernelv1.WhoAmIRequest{}))
	if err != nil {
		t.Fatalf("WhoAmI as bob: %v", err)
	}
	bobs := rolesOf(t, "user:bob")
	if !slices.Contains(bobs, "apicl_team") {
		t.Fatalf("bob's roles in the database are %v: the set-up gave him apicl_team", bobs)
	}
	want := &kernelv1.WhoAmIResponse{Principal: "user:bob", Namespace: "namespaces/acme", Roles: bobs}
	if !proto.Equal(msg(got), want) {
		t.Errorf("WhoAmI as bob = %v\nwant %v", msg(got), want)
	}
	// The same person in another namespace: that namespace's roles only.
	globex, err := as("sam-globex").WhoAmI(ctx, connect.NewRequest(&kernelv1.WhoAmIRequest{}))
	if err != nil || msg(globex).Principal != "user:sam" || msg(globex).Namespace != "namespaces/globex" {
		t.Errorf("WhoAmI as sam in globex = %v, error %v", msg(globex), err)
	}
	for _, r := range msg(globex).GetRoles() {
		if slices.Contains(rolesOf(t, "user:sam"), r) {
			t.Errorf("sam in globex is told the role %s, which he holds in acme", r)
		}
	}

	// A customer: herself, no roles.
	kim, err := asCustomer("kim", "").WhoAmI(ctx, connect.NewRequest(&kernelv1.WhoAmIRequest{}))
	if err != nil || !proto.Equal(msg(kim), &kernelv1.WhoAmIResponse{Principal: "customer:kim", Namespace: "namespaces/acme"}) {
		t.Errorf("WhoAmI as kim = %v, error %v; want customer:kim in acme and nothing else", msg(kim), err)
	}

	// An agent acting for ana: its roles, hers, and its session.
	box := create(t, "ana", ticketV1, map[string]any{"f_title": "whoami box"}).Item.Name
	session := startSession(t, "bot", "user:ana", []string{box}, "read", "edit_fields")
	bot, err := asAgent("bot", session).WhoAmI(ctx, connect.NewRequest(&kernelv1.WhoAmIRequest{}))
	if err != nil {
		t.Fatalf("WhoAmI as the bot: %v", err)
	}
	wantBot := &kernelv1.WhoAmIResponse{
		Principal: "agent:triage-bot", Namespace: "namespaces/acme", Roles: rolesOf(t, "agent:triage-bot"),
		OnBehalfOf: "user:ana", OnBehalfOfRoles: rolesOf(t, "user:ana"),
		Session: &kernelv1.AgentSession{
			Name: "namespaces/acme/agentSessions/" + session, Id: session, Agent: "agent:triage-bot", ActingFor: "user:ana",
			Scopes: []string{box}, Powers: []string{"read", "edit_fields"},
		},
	}
	if !proto.Equal(msg(bot), wantBot) {
		t.Errorf("WhoAmI as the bot = %v\nwant %v", msg(bot), wantBot)
	}
	if len(wantBot.OnBehalfOfRoles) == 0 {
		t.Errorf("ana holds no role in the database: the test proves nothing about on_behalf_of_roles")
	}

	// An agent without a session is refused as on every read; with a
	// session that is not its own, or that does not exist, it is nobody.
	_, err = asAgent("bot", "").WhoAmI(ctx, connect.NewRequest(&kernelv1.WhoAmIRequest{}))
	apiError(t, "WhoAmI as the bot with no session", err, connect.CodeInvalidArgument, "INVALID_REQUEST")
	_, err = asAgent("bot", "00000000-0000-7000-8000-000000000000").WhoAmI(ctx, connect.NewRequest(&kernelv1.WhoAmIRequest{}))
	apiError(t, "WhoAmI as the bot with a session that does not exist", err, connect.CodeUnauthenticated, "SESSION_INVALID")
	_, err = asAgent("helper", session).WhoAmI(ctx, connect.NewRequest(&kernelv1.WhoAmIRequest{}))
	apiError(t, "WhoAmI as another agent with the bot's session", err, connect.CodeUnauthenticated, "SESSION_INVALID")
	// No token: nobody.
	_, err = withToken("no-such-token").WhoAmI(ctx, connect.NewRequest(&kernelv1.WhoAmIRequest{}))
	apiError(t, "WhoAmI with an unknown token", err, connect.CodeUnauthenticated, "UNAUTHENTICATED")
}
