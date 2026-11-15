// Tests for Step 0, part 1: CreateItem, GetItem, UpdateItem, TransitionItem
// and SearchItems, and every named refusal they can return. The refusals of
// part 2 (NEEDS_APPROVAL for agents) come with agent sessions.
package api_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/durationpb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// --- CreateItem and GetItem ---

// A write returns the Item after the write, with item_seq and commit_seq.
// Names carry the namespace; an int field is a string on the way out.
func TestCreateItem_ReturnsTheItem(t *testing.T) {
	res := create(t, "ana", ticketV1, map[string]any{"f_title": "Printer on fire", "f_priority": 2})

	it := res.Item
	id, ok := strings.CutPrefix(it.GetName(), "namespaces/acme/items/")
	if _, err := uuid.Parse(id); !ok || err != nil {
		t.Errorf("name = %q, want namespaces/acme/items/<uuid>", it.GetName())
	}
	if it.Type != ticketV1 || it.State != "open" || it.Container != "" || it.Hold != nil {
		t.Errorf("item = type %q, state %q, container %q, hold %v; want %s, open, none, none",
			it.Type, it.State, it.Container, it.Hold, ticketV1)
	}
	f := it.Fields.AsMap()
	if f["f_title"] != "Printer on fire" || f["f_priority"] != "2" || len(f) != 2 {
		t.Errorf("fields = %v, want f_title and f_priority \"2\"", f)
	}
	if it.Etag == "" || res.ItemSeq != 1 || res.CommitSeq <= 0 {
		t.Errorf("etag %q, item_seq %d, commit_seq %d; want an etag, 1, > 0", it.Etag, res.ItemSeq, res.CommitSeq)
	}

	got := get(t, "ana", it.Name)
	if !proto.Equal(got, it) {
		t.Errorf("GetItem = %v\nwant the Item CreateItem returned: %v", got, it)
	}
}

// The same request_id with the same request returns the stored result and
// writes nothing; with a different request it is ALREADY_EXISTS.
func TestCreateItem_RequestIDIsTheIdempotencyKey(t *testing.T) {
	ctx := testCtx(t)
	req := &kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
		Fields: fields(t, map[string]any{"f_title": "once"}),
	}
	first, err := as("ana").CreateItem(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("first CreateItem: %v", err)
	}
	again, err := as("ana").CreateItem(ctx, connect.NewRequest(req))
	if err != nil {
		t.Fatalf("same request again: %v", err)
	}
	if again.Msg.Item.Name != first.Msg.Item.Name || again.Msg.CommitSeq != first.Msg.CommitSeq ||
		again.Msg.ItemSeq != first.Msg.ItemSeq {
		t.Errorf("replay = %s seq %d/%d, want %s seq %d/%d", again.Msg.Item.Name, again.Msg.ItemSeq,
			again.Msg.CommitSeq, first.Msg.Item.Name, first.Msg.ItemSeq, first.Msg.CommitSeq)
	}
	if n := countEvents(t, first.Msg.Item.Name); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}

	other := proto.Clone(req).(*kernelv1.CreateItemRequest)
	other.Fields = fields(t, map[string]any{"f_title": "twice"})
	_, err = as("ana").CreateItem(ctx, connect.NewRequest(other))
	apiError(t, "same request_id, other request", err, connect.CodeAlreadyExists, "IDEMPOTENCY_KEY_REUSED")
}

// Every write requires a request_id. A request refused before any work uses
// no key and writes nothing.
func TestWrites_RequestIDIsRequired(t *testing.T) {
	ctx := testCtx(t)
	name := create(t, "ana", ticketV1, map[string]any{"f_assignee": "user-bob"}).Item.Name

	_, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", Type: ticketV1,
	}))
	invalid(t, "CreateItem", err, "request_id")
	_, err = as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, Fields: fields(t, map[string]any{"f_title": "x"}),
	}))
	invalid(t, "UpdateItem", err, "request_id")
	_, err = as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, ToState: "in_progress",
	}))
	invalid(t, "TransitionItem", err, "request_id")

	if n := countEvents(t, name); n != 1 {
		t.Errorf("events = %d, want 1", n)
	}
}

// Malformed names are INVALID_ARGUMENT naming the field; a well-formed name
// of something that is not there is NOT_FOUND.
func TestCreateItem_Names(t *testing.T) {
	ctx := testCtx(t)
	call := func(parent, typ, container string) error {
		_, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: parent, RequestId: newID(), Type: typ, Container: container,
		}))
		return err
	}
	invalid(t, "parent without namespaces/", call("acme", ticketV1, ""), "parent")
	invalid(t, "no type", call("namespaces/acme", "", ""), "type")
	invalid(t, "type without a version", call("namespaces/acme", "namespaces/acme/types/ticket", ""), "type")
	invalid(t, "version not a number", call("namespaces/acme", "namespaces/acme/types/ticket/versions/one", ""), "type")
	invalid(t, "type not registered", call("namespaces/acme", "namespaces/acme/types/ghost/versions/1", ""), "type")
	invalid(t, "container not an item name", call("namespaces/acme", ticketV1, "channels/1"), "container")
	invalid(t, "container id not a uuid", call("namespaces/acme", ticketV1, "namespaces/acme/items/42"), "container")
	notFound(t, "missing container", call("namespaces/acme", ticketV1, "namespaces/acme/items/"+uuid.NewString()))
}

func TestGetItem_MalformedNameIsInvalid(t *testing.T) {
	for _, name := range []string{"", "items/" + uuid.NewString(), "namespaces/acme/items/42",
		"namespaces/acme/items/" + uuid.NewString() + "/extra"} {
		_, err := as("ana").GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: name}))
		invalid(t, fmt.Sprintf("GetItem(%q)", name), err, "name")
	}
}

// An Item in a container, narrowed to its creator: the container is a field
// of the Item, not part of its name, and the narrowed Item is NOT_FOUND for
// anyone else, exactly like an Item that does not exist.
func TestCreateItem_InAContainer_Narrowed(t *testing.T) {
	ctx := testCtx(t)
	ch := create(t, "ana", channel, map[string]any{"f_name": "ops"}).Item
	res, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: ch.Name,
		Narrow: &kernelv1.Readers{Principals: []string{"user:ana"}},
	}))
	if err != nil {
		t.Fatalf("CreateItem: %v", err)
	}
	it := res.Msg.Item
	if it.Container != ch.Name || !strings.HasPrefix(it.Name, "namespaces/acme/items/") {
		t.Errorf("item %s in %q, want a flat name in container %s", it.Name, it.Container, ch.Name)
	}

	_, err = as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
		Narrow: &kernelv1.Readers{Principals: []string{"user:bob"}},
	}))
	invalid(t, "narrowed without the caller", err, "narrow.principals")

	_, hidden := as("bob").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: it.Name}))
	_, missing := as("bob").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{
		Name: "namespaces/acme/items/" + uuid.NewString(),
	}))
	assertSameNotFound(t, hidden, missing)
}

// assertSameNotFound checks that two NOT_FOUND answers cannot be told apart:
// same code, message and details.
func assertSameNotFound(t *testing.T, a, b error) {
	t.Helper()
	ca := apiError(t, "first", a, connect.CodeNotFound, "NOT_FOUND")
	cb := apiError(t, "second", b, connect.CodeNotFound, "NOT_FOUND")
	if ca.Message() != cb.Message() {
		t.Errorf("messages differ: %q vs %q", ca.Message(), cb.Message())
	}
	ia, ib := errorInfo(t, ca), errorInfo(t, cb)
	if !proto.Equal(ia, ib) || len(ca.Details()) != len(cb.Details()) {
		t.Errorf("details differ: %v vs %v", ca.Details(), cb.Details())
	}
}

// --- UpdateItem ---

// A patch changes only the fields it names; null clears one. An int may be
// sent as a number or a numeric string.
func TestUpdateItem_Patch(t *testing.T) {
	name := create(t, "ana", ticketV1, map[string]any{"f_title": "old", "f_reporter": "user-ann"}).Item.Name
	res, err := as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(),
		Fields: fields(t, map[string]any{"f_title": nil, "f_priority": "5", "f_urgent": true}),
	}))
	if err != nil {
		t.Fatalf("UpdateItem: %v", err)
	}
	f := res.Msg.Item.Fields.AsMap()
	want := map[string]any{"f_reporter": "user-ann", "f_priority": "5", "f_urgent": true}
	if fmt.Sprint(f) != fmt.Sprint(want) {
		t.Errorf("fields = %v, want %v", f, want)
	}
	if res.Msg.ItemSeq != 2 || res.Msg.Item.Etag == "" {
		t.Errorf("item_seq %d, etag %q; want 2 and an etag", res.Msg.ItemSeq, res.Msg.Item.Etag)
	}
}

// etag is an if-match on every write. A stale or made-up etag is ABORTED /
// STALE and writes nothing; the current one passes.
func TestWrites_Etag(t *testing.T) {
	ctx := testCtx(t)
	created := create(t, "ana", ticketV1, map[string]any{"f_assignee": "user-bob"}).Item
	update := func(etag string) error {
		_, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
			Name: created.Name, RequestId: newID(), Etag: etag, Fields: fields(t, map[string]any{"f_priority": 1}),
		}))
		return err
	}
	if err := update(created.Etag); err != nil {
		t.Fatalf("update with the current etag: %v", err)
	}
	for _, etag := range []string{created.Etag, "0", "-1", "abc"} {
		apiError(t, "UpdateItem with etag "+etag, update(etag), connect.CodeAborted, "STALE")
	}
	_, err := as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: created.Name, RequestId: newID(), Etag: created.Etag, ToState: "in_progress",
	}))
	apiError(t, "TransitionItem with a stale etag", err, connect.CodeAborted, "STALE")
	if n := countEvents(t, created.Name); n != 2 {
		t.Errorf("events = %d, want 2 (create, the one update)", n)
	}
}

// A readers change narrows the Item (others get NOT_FOUND); inherit undoes
// it. Hiding an Item from yourself is malformed.
func TestUpdateItem_Readers(t *testing.T) {
	ctx := testCtx(t)
	name := create(t, "ana", ticketV1, nil).Item.Name
	policy := func(p *kernelv1.PolicyChange) error {
		_, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
			Name: name, RequestId: newID(), Policy: p,
		}))
		return err
	}
	narrow := func(ps ...string) *kernelv1.PolicyChange {
		return &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{Narrow: &kernelv1.Readers{Principals: ps}}}
	}

	if err := policy(narrow("user:ana")); err != nil {
		t.Fatalf("narrow to ana: %v", err)
	}
	_, err := as("bob").GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: name}))
	notFound(t, "bob after narrowing", err)

	if err := policy(&kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Inherit{Inherit: true}}); err != nil {
		t.Fatalf("inherit: %v", err)
	}
	get(t, "bob", name)

	// A bad reader list names the readers' path in the request.
	invalid(t, "narrow without the caller", policy(narrow("user:bob")), "policy.narrow.principals")
	invalid(t, "untyped reader", policy(narrow("user:ana", "bob")), "policy.narrow.principals")
	invalid(t, "empty policy change", policy(&kernelv1.PolicyChange{}), "policy")
	invalid(t, "inherit false", policy(&kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Inherit{}}), "policy")
}

// A move changes the container field; the name stays.
func TestUpdateItem_Move(t *testing.T) {
	ctx := testCtx(t)
	c1 := create(t, "ana", channel, nil).Item.Name
	c2 := create(t, "ana", channel, nil).Item.Name
	res, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: c1,
	}))
	if err != nil {
		t.Fatalf("create in c1: %v", err)
	}
	name := res.Msg.Item.Name
	move := func(m *kernelv1.Move) (*kernelv1.Item, error) {
		res, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
			Name: name, RequestId: newID(), Move: m,
		}))
		if err != nil {
			return nil, err
		}
		return res.Msg.Item, nil
	}

	it, err := move(&kernelv1.Move{Container: c2})
	if err != nil || it.Container != c2 || it.Name != name {
		t.Fatalf("move to c2 = %v, %v; want the same name in c2", it, err)
	}
	it, err = move(&kernelv1.Move{})
	if err != nil || it.Container != "" {
		t.Fatalf("move to the top = %v, %v; want no container", it, err)
	}
	_, err = move(&kernelv1.Move{Container: name})
	malformed(t, "move into itself", err)
	_, err = move(&kernelv1.Move{Container: "namespaces/acme/items/" + uuid.NewString()})
	notFound(t, "move into a missing container", err)
}

// A move that lets more people see the Item needs confirm_widen.
func TestUpdateItem_MoveThatWidensNeedsConfirmation(t *testing.T) {
	ctx := testCtx(t)
	res, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: channel,
		Narrow: &kernelv1.Readers{Principals: []string{"user:ana"}},
	}))
	if err != nil {
		t.Fatalf("create private channel: %v", err)
	}
	private := res.Msg.Item.Name
	res, err = as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1, Container: private,
	}))
	if err != nil {
		t.Fatalf("create in private channel: %v", err)
	}
	name := res.Msg.Item.Name
	toTop := func(confirm bool) error {
		_, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
			Name: name, RequestId: newID(), Move: &kernelv1.Move{ConfirmWiden: confirm},
		}))
		return err
	}
	// Not malformed: valid, but the client must ask the user and resend.
	apiError(t, "widening move without confirm_widen", toTop(false), connect.CodeFailedPrecondition, "WIDEN_NOT_CONFIRMED")
	if err := toTop(true); err != nil {
		t.Fatalf("widening move with confirm_widen: %v", err)
	}
	get(t, "bob", name)
}

// An update names at least one change; a readers change and a move are two
// writes, not one.
func TestUpdateItem_ChangesPerCall(t *testing.T) {
	ctx := testCtx(t)
	name := create(t, "ana", ticketV1, nil).Item.Name
	_, err := as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(),
	}))
	invalid(t, "no change", err, "fields")
	_, err = as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Move: &kernelv1.Move{},
		Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Inherit{Inherit: true}},
	}))
	invalid(t, "policy and move together", err, "move")
}

// --- TransitionItem ---

func TestTransitionItem_Applied(t *testing.T) {
	name := create(t, "ana", ticketV1, map[string]any{"f_assignee": "user-bob"}).Item.Name
	res := transition(t, "ana", name, "in_progress")
	if res.Outcome != kernelv1.TransitionOutcome_TRANSITION_OUTCOME_APPLIED || res.Item.State != "in_progress" ||
		res.ItemSeq != 2 || res.Hold != "" || res.Reason != "" {
		t.Errorf("TransitionItem = %v, want APPLIED, in_progress, item_seq 2, no hold or reason", res)
	}
}

// The Type's rules refuse with FAILED_PRECONDITION and the rule's code.
func TestTransitionItem_RuleRefusals(t *testing.T) {
	ctx := testCtx(t)
	call := func(name, to string, patch map[string]any) error {
		_, err := as("ana").TransitionItem(ctx, connect.NewRequest(&kernelv1.TransitionItemRequest{
			Name: name, RequestId: newID(), ToState: to, Fields: fields(t, patch),
		}))
		return err
	}
	open := create(t, "ana", ticketV1, nil).Item.Name
	apiError(t, "guard", call(open, "in_progress", nil), connect.CodeFailedPrecondition, "GUARD_FAILED")
	apiError(t, "undeclared move", call(open, "resolved", nil), connect.CodeFailedPrecondition, "NO_TRANSITION")

	working := create(t, "ana", ticketV1, map[string]any{"f_assignee": "user-bob"}).Item.Name
	transition(t, "ana", working, "in_progress")
	apiError(t, "required field", call(working, "resolved", nil), connect.CodeFailedPrecondition, "MISSING_FIELDS")
	invalid(t, "bad patch", call(working, "resolved", map[string]any{"f_resolution": "magic"}), "fields.f_resolution")

	invalid(t, "no to_state", call(working, "", nil), "to_state")
	if n := countEvents(t, working); n != 2 {
		t.Errorf("events = %d, want 2", n)
	}
}

// wait is 0 to 5 s.
func TestTransitionItem_WaitIsBounded(t *testing.T) {
	name := create(t, "ana", ticketV1, map[string]any{"f_assignee": "user-bob"}).Item.Name
	for _, d := range []time.Duration{-time.Second, 6 * time.Second} {
		_, err := as("ana").TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{
			Name: name, RequestId: newID(), ToState: "in_progress", Wait: durationpb.New(d),
		}))
		invalid(t, fmt.Sprintf("wait %v", d), err, "wait")
	}
}

// v3Ticket returns a ticket v3 in in_progress, ready to approve, made by ana.
func v3Ticket(t *testing.T) string {
	t.Helper()
	name := create(t, "ana", ticketV3, map[string]any{
		"f_title": "Onboarding", "f_customer_id": "cust-1", "f_amount": 100, "f_assignee": "user-bob",
	}).Item.Name
	transition(t, "ana", name, "open")
	transition(t, "ana", name, "in_progress")
	return name
}

// bob may see the ticket but is not an approver.
func TestTransitionItem_NotAllowed(t *testing.T) {
	name := v3Ticket(t)
	_, err := as("bob").TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), ToState: "approved",
	}))
	apiError(t, "approve as bob", err, connect.CodePermissionDenied, "NOT_ALLOWED")
}

// erpCheck is ticket v3's check hook, resolving holds as the worker would.
var erpCheck = item.Principal{Actor: "hook:ticket:erp_check"}

func itemID(t *testing.T, name string) uuid.UUID {
	t.Helper()
	return uuid.MustParse(name[strings.LastIndex(name, "/")+1:])
}

// resolve settles the Item's open hold through the kernel's internal door,
// as ticket v3's check hook does. The request is event 4 of v3Ticket's Item.
func resolve(t *testing.T, itemName, holdName string, v itemservice.Verdict, reason string) {
	t.Helper()
	id, holdID := itemID(t, itemName), itemID(t, holdName)
	_, err := k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
		IdempotencyKey: "hold:" + holdID.String(), Principal: erpCheck, ItemID: id, HoldID: holdID,
		Verdict: v, Reason: reason,
		Attribution: item.Attribution{
			OriginatingHook: "hook:ticket@v3:erp_check", CausationID: fmt.Sprintf("%s:4", id), Hop: 1,
		},
	})
	if err != nil {
		t.Fatalf("ResolveHold %s: %v", v, err)
	}
}

// A checked transition is held: PENDING with the hold's name, shown on the
// Item. While held, a write to a field the check reads is ABORTED / HELD,
// naming the hold. A denial is a successful call: DENIED with the reason.
func TestTransitionItem_Held(t *testing.T) {
	ctx := testCtx(t)
	name := v3Ticket(t)
	approve := &kernelv1.TransitionItemRequest{Name: name, RequestId: newID(), ToState: "approved"}

	res, err := as("ana").TransitionItem(ctx, connect.NewRequest(approve))
	if err != nil {
		t.Fatalf("approve: %v", err)
	}
	m := res.Msg
	if m.Outcome != kernelv1.TransitionOutcome_TRANSITION_OUTCOME_PENDING ||
		!strings.HasPrefix(m.Hold, name+"/holds/") || m.Item.State != "in_progress" {
		t.Fatalf("approve = %v, want PENDING with a hold under %s, still in_progress", m, name)
	}
	h := m.Item.Hold
	if h == nil || h.Name != m.Hold || h.Check != "erp_check" || h.FromState != "in_progress" ||
		h.RequestedState != "approved" {
		t.Errorf("Item.hold = %v, want the open hold %s (erp_check, in_progress -> approved)", h, m.Hold)
	}
	if got := get(t, "bob", name).Hold; !proto.Equal(got, h) {
		t.Errorf("GetItem hold = %v, want %v", got, h)
	}

	_, err = as("ana").UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Fields: fields(t, map[string]any{"f_amount": 5}),
	}))
	ce := apiError(t, "update a frozen field", err, connect.CodeAborted, "HELD")
	if info := errorInfo(t, ce); info == nil || info.Metadata["hold"] != m.Hold {
		t.Errorf("HELD metadata = %v, want hold = %s", info.GetMetadata(), m.Hold)
	}

	resolve(t, name, m.Hold, itemservice.VerdictDeny, "customer_on_hold")
	again, err := as("ana").TransitionItem(ctx, connect.NewRequest(approve))
	if err != nil {
		t.Fatalf("approve again with the same request_id: %v", err)
	}
	if again.Msg.Outcome != kernelv1.TransitionOutcome_TRANSITION_OUTCOME_DENIED ||
		again.Msg.Reason != "CUSTOMER_ON_HOLD" || again.Msg.Hold != m.Hold {
		t.Errorf("retry = %v, want DENIED for CUSTOMER_ON_HOLD, hold %s", again.Msg, m.Hold)
	}
	if it := again.Msg.Item; it.State != "in_progress" || it.Hold != nil {
		t.Errorf("Item after the denial: state %q, hold %v; want in_progress, no open hold", it.State, it.Hold)
	}
}

// With a wait, the call answers once the check has: APPLIED, the Item moved.
func TestTransitionItem_HeldWithWait(t *testing.T) {
	name := v3Ticket(t)
	id := itemID(t, name)

	// Play the check hook: once the hold opens, allow it.
	done := make(chan error, 1)
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var holdID uuid.UUID
			err := pool.QueryRow(testCtx(t),
				`select id from item_holds where item_id = $1 and status = 'open'`, id).Scan(&holdID)
			if err == nil {
				_, err = k.Internal().ResolveHold(testCtx(t), itemservice.ResolveHoldRequest{
					IdempotencyKey: "hold:" + holdID.String(), Principal: erpCheck, ItemID: id, HoldID: holdID,
					Verdict: itemservice.VerdictAllow,
					Attribution: item.Attribution{
						OriginatingHook: "hook:ticket@v3:erp_check", CausationID: fmt.Sprintf("%s:4", id), Hop: 1,
					},
				})
				done <- err
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		done <- fmt.Errorf("no hold opened")
	}()

	res, err := as("ana").TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), ToState: "approved", Wait: durationpb.New(3 * time.Second),
	}))
	if err != nil {
		t.Fatalf("approve with wait: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("check hook: %v", err)
	}
	if res.Msg.Outcome != kernelv1.TransitionOutcome_TRANSITION_OUTCOME_APPLIED || res.Msg.Item.State != "approved" ||
		res.Msg.Item.Hold != nil || res.Msg.ItemSeq != 5 {
		t.Errorf("approve with wait = %v, want APPLIED, approved, no open hold, item_seq 5", res.Msg)
	}
}

// --- SearchItems ---

// Search finds what the caller can see, projected like GetItem.
func TestSearchItems_OnlyWhatTheCallerSees(t *testing.T) {
	ctx := testCtx(t)
	w := word()
	open := create(t, "ana", ticketV1, map[string]any{"f_title": w, "f_priority": 3})
	res, err := as("ana").CreateItem(ctx, connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV1,
		Fields: fields(t, map[string]any{"f_title": w}), Narrow: &kernelv1.Readers{Principals: []string{"user:ana"}},
	}))
	if err != nil {
		t.Fatalf("create narrowed: %v", err)
	}
	private := res.Msg

	search := func(who string, minSeq int64) []*kernelv1.Item {
		t.Helper()
		res, err := as(who).SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Query: w, MinSeq: minSeq,
		}))
		if err != nil {
			t.Fatalf("SearchItems as %s: %v", who, err)
		}
		return res.Msg.Items
	}
	names := func(items []*kernelv1.Item) []string {
		var out []string
		for _, it := range items {
			out = append(out, it.Name)
		}
		slices.Sort(out)
		return out
	}

	got := search("ana", private.CommitSeq)
	want := []string{open.Item.Name, private.Item.Name}
	slices.Sort(want)
	if !slices.Equal(names(got), want) {
		t.Errorf("ana finds %v, want %v", names(got), want)
	}
	got = search("bob", 0)
	if !slices.Equal(names(got), []string{open.Item.Name}) {
		t.Errorf("bob finds %v, want only %s", names(got), open.Item.Name)
	}
	if !proto.Equal(got[0], open.Item) {
		t.Errorf("search result %v, want the Item as GetItem shows it: %v", got[0], open.Item)
	}
}

// A min_seq the server has not reached is UNAVAILABLE / NOT_CAUGHT_UP after
// the wait; no words is malformed.
func TestSearchItems_Refusals(t *testing.T) {
	ctx := testCtx(t)
	last := create(t, "ana", ticketV1, nil)
	_, err := as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: word(), MinSeq: last.CommitSeq + 1_000_000,
	}))
	apiError(t, "unreached min_seq", err, connect.CodeUnavailable, "NOT_CAUGHT_UP")

	_, err = as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Query: "  ",
	}))
	invalid(t, "no words", err, "query")
	_, err = as("ana").SearchItems(ctx, connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "acme", Query: "x",
	}))
	invalid(t, "bad parent", err, "parent")
}
