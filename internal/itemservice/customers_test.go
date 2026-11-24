// Tests for Jira part 2, step 4a: customers, in the item service, on the
// repository's own Types (desk@2, incident@2, reply@1, note@1, the sla
// package) and two test Types for links and rollups (kiosk, kticket).
// Grouped by the Phase A items:
//
//  1. The principal: customer:<id>, fixed permissions, never a role, never
//     in the namespace's general audience. A customer creates only what a
//     container admits (own, shared), sets only customer_fields, never
//     updates, moves, narrows or links, and takes only the transitions
//     whose allowed names creator, as the requester.
//  2. "Their own": a customer sees an Item when its Type version owns
//     something (a desk: an open portal), when it is their own request in
//     a container that owns its Type, or when it is of a Type their request
//     shares; depth 1, from the current container, and the existing scope
//     test on top (a narrowed scope they do not read still hides).
//  3. Public and internal by Type: a reply is shared, a note is not.
//  4. Leaks: every outbound path, as the table in the Phase A answers.
//  5. Agents acting for a customer: the customer's view and the fixed set
//     of powers; and the idempotency key of an agent session includes the
//     user it acts for.
//  6. Staff create for a customer (requester).
//
// The SLA reactions are run by hand through the internal door, as the
// workflow would; the end-to-end test runs them for real.
package itemservice_test

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// custWorld: lea runs the desk (sla_admin, desk_agent), sam works on it
// (desk_agent), eve is staff with no role. kim and lee are customers.
// Two portals (desk@2) share the calendar and the policy; an old desk@1
// admits nobody; the PLAT space holds a story.
type custWorld struct {
	k             *itemservice.Service
	sla           slaWorld // the step 3b helpers (goals, reactions), over this world
	ns            string
	lea, sam, eve item.Principal
	kim, lee      item.Principal
	desk, desk2   uuid.UUID // desk@2
	oldDesk       uuid.UUID // desk@1
	space, story  uuid.UUID
	cal, policy   uuid.UUID
}

func customer(name, ns string) item.Principal {
	return item.Principal{Actor: "customer:" + name, Namespace: ns}
}

func newCustWorld(t *testing.T) custWorld {
	t.Helper()
	k := itemservice.New(pool)
	for _, f := range []string{"bug.v3.yaml", "comment.v2.yaml", "sla/calendar.v1.yaml", "sla/sla_policy.v1.yaml",
		"sla/sla_goal.v1.yaml", "desk.v1.yaml", "incident.v1.yaml", "reply.v1.yaml", "note.v1.yaml",
		"incident.v2.yaml", "desk.v2.yaml", "space.v1.yaml", "story.v3.yaml"} {
		registerFromFile(t, k, f)
	}
	ns := newNamespace(t, k)
	w := custWorld{k: k, ns: ns,
		lea: in(person("lea"), ns), sam: in(person("sam"), ns), eve: in(person("eve"), ns),
		kim: customer("kim", ns), lee: customer("lee", ns)}
	grantRoleIn(t, ns, w.lea, "sla_admin")
	grantRoleIn(t, ns, w.lea, "desk_agent")
	grantRoleIn(t, ns, w.sam, "desk_agent")
	w.cal = w.mk(t, w.lea, "calendar", 1, nil, map[string]any{"f_name": "always", "f_week": alwaysOpen, "f_zone": "UTC"})
	w.policy = w.mk(t, w.lea, "sla_policy", 1, nil, policyFields())
	for _, d := range []*uuid.UUID{&w.desk, &w.desk2} {
		*d = w.mk(t, w.lea, "desk", 2, nil, map[string]any{"f_key": "IT", "f_name": "IT " + word()})
		w.link(t, w.lea, *d, "calendar", w.cal)
		w.link(t, w.lea, *d, "sla_policy", w.policy)
	}
	w.oldDesk = w.mk(t, w.lea, "desk", 1, nil, map[string]any{"f_key": "OLD", "f_name": "old " + word()})
	w.space = w.mk(t, w.lea, "space", 1, nil, map[string]any{"f_key": "PLAT", "f_name": "PLAT " + word()})
	w.story = w.mk(t, w.sam, "story", 3, &w.space, map[string]any{"f_summary": "vpn story " + word()})
	// anyType names the two test Types of the links and rollups test: a
	// filter on a Type that is not registered is refused.
	registerSrc(t, k, kticketSrc)
	registerSrc(t, k, kioskSrc)
	w.sla = slaWorld{k: k, late: itemservice.NewWithOptions(pool, itemservice.Options{ClockSkew: time.Hour}), ns: ns,
		admin: w.lea, ana: w.kim, sam: w.sam, eve: w.eve, cal: w.cal, policy: w.policy, desk: w.desk}
	return w
}

func (w custWorld) mk(t *testing.T, p item.Principal, typ string, version int, in *uuid.UUID, fields map[string]any) uuid.UUID {
	t.Helper()
	id, err := w.tryCreate(t, p, typ, version, in, fields)
	if err != nil {
		t.Fatalf("%s creates %s@%d: %v", p.Actor, typ, version, err)
	}
	return id
}

func (w custWorld) tryCreate(t *testing.T, p item.Principal, typ string, version int, in *uuid.UUID, fields map[string]any) (uuid.UUID, error) {
	t.Helper()
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: version, ContainerID: in, Fields: fields,
	})
	return res.ItemID, err
}

func (w custWorld) link(t *testing.T, p item.Principal, src uuid.UUID, name string, dst uuid.UUID) {
	t.Helper()
	if _, err := w.k.Link(testCtx(t), itemservice.LinkRequest{
		IdempotencyKey: newKey(), Principal: p, SourceID: src, LinkType: name, TargetID: dst,
	}); err != nil {
		t.Fatalf("link %s: %v", name, err)
	}
}

func (w custWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string, fields map[string]any) error {
	t.Helper()
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to, Fields: fields,
	})
	return err
}

// incident is the customer's incident in the desk, with nothing run yet.
func (w custWorld) incident(t *testing.T, p item.Principal, desk uuid.UUID) uuid.UUID {
	t.Helper()
	return w.mk(t, p, "incident", 2, &desk, map[string]any{"f_title": "VPN down " + word(), "f_priority": "P3"})
}

// started is kim's P-priority incident@2 in the first portal, with both
// goals started and shown on it (the step 3b helpers, on incident@2).
func (w custWorld) started(t *testing.T, prio string) (inc, first, res uuid.UUID) {
	t.Helper()
	inc = w.mk(t, w.kim, "incident", 2, &w.desk, map[string]any{"f_title": "VPN down " + word(), "f_priority": prio})
	f := w.sla.reactCreate(t, entered(inc, 1, "sla_start_first_response"))
	r := w.sla.reactCreate(t, entered(inc, 1, "sla_start_resolution"))
	if f.Outcome != itemservice.ReactionApplied || r.Outcome != itemservice.ReactionApplied {
		t.Fatalf("the start reactions = %+v, %+v; want both applied", f, r)
	}
	w.sla.goalSets(t, f.Target, 1)
	w.sla.goalSets(t, r.Target, 1)
	return inc, f.Target, r.Target
}

func (w custWorld) reply(t *testing.T, p item.Principal, in uuid.UUID, body string) uuid.UUID {
	t.Helper()
	return w.mk(t, p, "reply", 1, &in, map[string]any{"f_body": body})
}

func (w custWorld) note(t *testing.T, p item.Principal, in uuid.UUID, body string) uuid.UUID {
	t.Helper()
	return w.mk(t, p, "note", 1, &in, map[string]any{"f_body": body})
}

// sees reports whether Get answers for p; any error but ErrNotFound fails.
func (w custWorld) sees(t *testing.T, p item.Principal, id uuid.UUID) bool {
	t.Helper()
	_, err := w.k.Get(testCtx(t), p, id)
	if err != nil && !errors.Is(err, itemservice.ErrNotFound) {
		t.Fatalf("Get(%s, %s): %v", p.Actor, id, err)
	}
	return err == nil
}

// anyType is a filter that matches any Item of the Types in play.
var anyType = itemservice.Filter{TypeIDs: []string{
	"desk", "incident", "reply", "note", "comment", "sla_goal", "calendar", "sla_policy", "space", "story", "bug",
	"kiosk", "kticket",
}}

// searchAll pages through a search to the end and returns the IDs found.
func (w custWorld) searchAll(t *testing.T, p item.Principal, words string, f itemservice.Filter, pageSize int) []uuid.UUID {
	t.Helper()
	var out []uuid.UUID
	token := ""
	for range 1000 {
		res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Query: words, Filter: f, Limit: pageSize, PageToken: token})
		if err != nil {
			t.Fatalf("Search(%s, %q): %v", p.Actor, words, err)
		}
		for _, it := range res.Items {
			out = append(out, it.ID)
		}
		if token = res.NextPageToken; token == "" {
			return out
		}
		if len(res.Items) == 0 {
			t.Fatalf("Search(%s): an empty page with a next token", p.Actor)
		}
	}
	t.Fatal("Search never ended")
	return nil
}

func (w custWorld) listAll(t *testing.T, p item.Principal, in *uuid.UUID, pageSize int) []uuid.UUID {
	t.Helper()
	var out []uuid.UUID
	token := ""
	for range 1000 {
		res, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: p, ContainerID: in, Limit: pageSize, PageToken: token})
		if err != nil {
			t.Fatalf("List(%s): %v", p.Actor, err)
		}
		for _, it := range res.Items {
			out = append(out, it.ID)
		}
		if token = res.NextPageToken; token == "" {
			return out
		}
		if len(res.Items) == 0 {
			t.Fatalf("List(%s): an empty page with a next token", p.Actor)
		}
	}
	t.Fatal("List never ended")
	return nil
}

// ============================================================ 1. the principal

func TestCustomer_IsAKindOfPrincipal(t *testing.T) {
	if kind, ok := item.KindOf("customer:kim"); !ok || kind != item.KindCustomer {
		t.Errorf("KindOf(customer:kim) = %q, %v; want customer", kind, ok)
	}
	if item.IsKernelActor("customer:kim") {
		t.Error("a customer is a kernel actor")
	}
	if _, ok := item.KindOf("customer:"); ok {
		t.Error("customer: with no ID is a principal")
	}
}

// A customer creates only what a container admits, where they can see it.
func TestCustomer_CreatesOnlyWhatAContainerAdmits(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	it, err := w.k.Get(testCtx(t), w.sam, inc)
	if err != nil || it.CreatedBy != "customer:kim" || it.CreatedOnBehalfOf != "" {
		t.Fatalf("kim's incident, as sam reads it: %+v, %v; want created by customer:kim", it, err)
	}
	if r, err := w.tryCreate(t, w.kim, "reply", 1, &inc, map[string]any{"f_body": "any news?"}); err != nil || r == uuid.Nil {
		t.Errorf("kim replies on her incident: %v", err)
	}

	lees := w.incident(t, w.lee, w.desk)
	ownReply := w.reply(t, w.kim, inc, "nested")
	forbidden := []struct {
		name    string
		typ     string
		version int
		in      *uuid.UUID
		fields  map[string]any
	}{
		{"an incident at the top level", "incident", 2, nil, map[string]any{"f_title": "x", "f_priority": "P3"}},
		{"a note in her incident", "note", 1, &inc, map[string]any{"f_body": "x"}},
		{"a goal in her incident", "sla_goal", 1, &inc, nil},
		{"a comment in her incident (comment is not shared)", "comment", 2, &inc, map[string]any{"f_body": "x", "f_public": true}},
		{"a reply in a reply (depth 1)", "reply", 1, &ownReply, map[string]any{"f_body": "x"}},
		{"a story in the desk", "story", 3, &w.desk, map[string]any{"f_summary": "x"}},
		{"a desk at the top level", "desk", 2, nil, map[string]any{"f_key": "K", "f_name": "mine"}},
	}
	for _, c := range forbidden {
		if _, err := w.tryCreate(t, w.kim, c.typ, c.version, c.in, c.fields); !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("kim creates %s: %v; want ErrForbidden", c.name, err)
		}
	}
	// A container she cannot see does not exist for her: no hint that it does.
	for name, in := range map[string]uuid.UUID{"lee's incident": lees, "the old desk (admits nobody)": w.oldDesk,
		"the PLAT space": w.space, "nothing at all": uuid.New()} {
		_, err := w.tryCreate(t, w.kim, "reply", 1, &in, map[string]any{"f_body": "x"})
		if !errors.Is(err, itemservice.ErrNotFound) {
			t.Errorf("kim creates in %s: %v; want ErrNotFound", name, err)
		}
	}
	// Never narrowed at creation: a customer creates no scope.
	_, err = w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.kim, TypeID: "incident", TypeVersion: 2,
		ContainerID: &w.desk, Fields: map[string]any{"f_title": "x", "f_priority": "P3"}, Narrow: &itemservice.Narrow{Readers: []string{"customer:kim"}}})
	if !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("kim creates a narrowed incident: %v; want ErrForbidden", err)
	}
}

// At creation a customer sets only the Type's customer_fields.
func TestCustomer_SetsOnlyCustomerFields(t *testing.T) {
	w := newCustWorld(t)
	for _, f := range []string{"f_assignee", "f_due_resolution"} {
		value := any("user:sam")
		if f == "f_due_resolution" {
			value = "2030-01-01T00:00:00Z"
		}
		_, err := w.tryCreate(t, w.kim, "incident", 2, &w.desk, map[string]any{"f_title": "x", "f_priority": "P1", f: value})
		var nw *itemservice.FieldNotWritableError
		if !errors.As(err, &nw) || !slices.Equal(nw.Fields, []string{f}) {
			t.Errorf("kim opens an incident with %s: %v; want FieldNotWritableError naming it", f, err)
		}
	}
	// The same fields are anyone's for staff, as before.
	w.mk(t, w.sam, "incident", 2, &w.desk, map[string]any{"f_title": "x", "f_priority": "P1", "f_assignee": "user:sam"})
}

// Fixed permissions: no update, no move, no policy, no link.
func TestCustomer_NeverUpdatesMovesNarrowsOrLinks(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	bug := w.mk(t, w.sam, "bug", 3, nil, map[string]any{"f_summary": "x"})
	other := w.incident(t, w.kim, w.desk)
	ctx := testCtx(t)
	attempts := map[string]error{}
	_, attempts["a field she set herself"] = w.k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.kim, ItemID: inc,
		Fields: map[string]any{"f_title": "renamed"}})
	_, attempts["a move to the other portal"] = w.k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.kim, ItemID: inc,
		Move: &itemservice.Move{To: &w.desk2}})
	_, attempts["a narrowing to herself"] = w.k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.kim, ItemID: inc,
		Policy: &itemservice.PolicyPatch{Readers: []string{"customer:kim"}}})
	_, attempts["a link between her own incidents"] = w.k.Link(ctx, itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.kim,
		SourceID: inc, LinkType: "caused_by", TargetID: other})
	_, attempts["a link to a bug"] = w.k.Link(ctx, itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.kim,
		SourceID: inc, LinkType: "caused_by", TargetID: bug})
	for what, err := range attempts {
		if !errors.Is(err, itemservice.ErrForbidden) && !(what == "a link to a bug" && errors.Is(err, itemservice.ErrNotFound)) {
			t.Errorf("kim: %s: %v; want ErrForbidden", what, err)
		}
	}
	if n := itemSeqOf(t, inc); n != 1 {
		t.Errorf("kim's incident is at item_seq %d after her refused writes, want 1", n)
	}
}

// A customer takes only the transitions whose allowed names creator, as
// the requester; staff take those two with desk_agent, never through creator.
func TestCustomer_TransitionsOnlyAsTheRequester(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	// allowed is empty on new -> in_progress: anyone who sees it, but not a customer.
	if err := w.move(t, w.kim, inc, "in_progress", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("kim starts work on her own incident: %v; want ErrForbidden", err)
	}
	// Somebody else's: she cannot see it.
	lees := w.incident(t, w.lee, w.desk)
	if err := w.move(t, w.kim, lees, "canceled", nil); !errors.Is(err, itemservice.ErrNotFound) {
		t.Errorf("kim cancels lee's incident: %v; want ErrNotFound", err)
	}
	if err := w.move(t, w.kim, inc, "canceled", nil); err != nil {
		t.Errorf("kim cancels her new incident: %v", err)
	}

	// Reopening: resolved -> in_progress.
	inc2 := w.incident(t, w.kim, w.desk)
	for _, step := range []struct {
		to     string
		fields map[string]any
	}{{"in_progress", nil}, {"resolved", map[string]any{"f_resolution": "fixed"}}} {
		if err := w.move(t, w.sam, inc2, step.to, step.fields); err != nil {
			t.Fatalf("sam moves to %s: %v", step.to, err)
		}
	}
	if err := w.move(t, w.kim, inc2, "in_progress", nil); err != nil {
		t.Errorf("kim reopens her resolved incident: %v", err)
	}

	// Staff: eve holds no role and is not the requester.
	inc3 := w.incident(t, w.kim, w.desk)
	if err := w.move(t, w.eve, inc3, "canceled", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("eve cancels kim's incident: %v; want ErrForbidden (needs desk_agent)", err)
	}
	if err := w.move(t, w.sam, inc3, "canceled", nil); err != nil {
		t.Errorf("sam (desk_agent) cancels kim's incident: %v", err)
	}
	// An empty allowed still lets any staff member in.
	inc4 := w.incident(t, w.kim, w.desk)
	if err := w.move(t, w.eve, inc4, "in_progress", nil); err != nil {
		t.Errorf("eve starts work: %v", err)
	}
}

// Never a role: EnsureRoles refuses one, and a row put there by hand is
// never read for a customer.
func TestCustomer_NeverHoldsARole(t *testing.T) {
	w := newCustWorld(t)
	_, err := w.k.EnsureRoles(testCtx(t), []string{w.ns}, []itemservice.Role{{Namespace: w.ns, Principal: "customer:kim", Role: "desk_agent"}})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("EnsureRoles(customer:kim, desk_agent): %v; want ErrInvalid", err)
	}
	grantRoleIn(t, w.ns, w.kim, "desk_agent")
	inc := w.incident(t, w.kim, w.desk)
	if err := w.move(t, w.sam, inc, "in_progress", nil); err != nil {
		t.Fatal(err)
	}
	// in_progress -> canceled names no creator: a customer never takes
	// it, whatever principal_roles says.
	if err := w.move(t, w.kim, inc, "canceled", nil); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("kim with a desk_agent row cancels an incident in progress: %v; want ErrForbidden", err)
	}
}

// Never in the general audience: an open scope alone shows a customer nothing.
func TestCustomer_NotInTheGeneralAudience(t *testing.T) {
	w := newCustWorld(t)
	for name, id := range map[string]uuid.UUID{"the PLAT space": w.space, "a story in it": w.story,
		"the calendar": w.cal, "the SLA policy": w.policy, "the old desk": w.oldDesk} {
		if w.sees(t, w.kim, id) {
			t.Errorf("kim sees %s", name)
		}
		if !w.sees(t, w.eve, id) {
			t.Errorf("eve (staff, no role) does not see %s", name)
		}
	}
}

// ============================================================ 2. their own

func TestCustomer_SeesTheirOwn_AndWhatTheirOwnShares(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	answer := w.reply(t, w.sam, inc, "we are on it")
	hers := w.reply(t, w.kim, inc, "thanks")
	note := w.note(t, w.sam, inc, "customer is on the old VPN client")
	comment := w.mk(t, w.sam, "comment", 2, &inc, map[string]any{"f_body": "public?", "f_public": true})
	_, first, res := w.started(t, "P3") // another of kim's, in the first portal
	lees := w.incident(t, w.lee, w.desk)
	leesReply := w.reply(t, w.sam, lees, "hello lee")
	deep := w.mk(t, w.sam, "reply", 1, &answer, map[string]any{"f_body": "a reply in a reply"})

	visible := map[string]uuid.UUID{"both portals": w.desk, "the other portal": w.desk2, "her incident": inc,
		"sam's reply": answer, "her reply": hers}
	hidden := map[string]uuid.UUID{"sam's note": note, "a comment (not shared)": comment, "the first response goal": first,
		"the resolution goal": res, "lee's incident": lees, "a reply in lee's incident": leesReply,
		"a reply two levels down": deep, "the old desk": w.oldDesk}
	for name, id := range visible {
		if !w.sees(t, w.kim, id) {
			t.Errorf("kim does not see %s", name)
		}
	}
	for name, id := range hidden {
		if w.sees(t, w.kim, id) {
			t.Errorf("kim sees %s", name)
		}
	}
	// Staff see anyType, as today.
	for name, id := range hidden {
		if !w.sees(t, w.sam, id) {
			t.Errorf("sam does not see %s", name)
		}
	}
	for name, id := range visible {
		if !w.sees(t, w.eve, id) {
			t.Errorf("eve does not see %s", name)
		}
	}
	// lee sees hers and not kim's.
	if !w.sees(t, w.lee, lees) || w.sees(t, w.lee, inc) || w.sees(t, w.lee, answer) {
		t.Error("lee's view: want her incident, not kim's nor its replies")
	}
}

// Computed from the current container: an incident moved out of a portal
// is no longer the customer's to see.
func TestCustomer_FromTheCurrentContainer(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	reply := w.reply(t, w.sam, inc, "moved")
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: inc,
		Move: &itemservice.Move{To: &w.desk2}}); err != nil {
		t.Fatalf("sam moves it to the other portal: %v", err)
	}
	if !w.sees(t, w.kim, inc) || !w.sees(t, w.kim, reply) {
		t.Error("kim lost her incident moved between two portals")
	}
	for name, to := range map[string]uuid.UUID{"the old desk": w.oldDesk, "the PLAT space": w.space} {
		if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: inc,
			Move: &itemservice.Move{To: &to}}); err != nil {
			t.Fatalf("sam moves it to %s: %v", name, err)
		}
		if w.sees(t, w.kim, inc) || w.sees(t, w.kim, reply) {
			t.Errorf("kim still sees her incident (or its reply) in %s", name)
		}
	}
}

// A narrowed scope they do not read still hides; a customer may be a
// reader; a reader is never enough outside what the rule admits.
func TestCustomer_ComposesWithNarrowing(t *testing.T) {
	w := newCustWorld(t)
	narrow := func(id uuid.UUID, readers ...string) {
		t.Helper()
		if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: id,
			Policy: &itemservice.PolicyPatch{Readers: readers}}); err != nil {
			t.Fatalf("narrow to %v: %v", readers, err)
		}
	}
	inc := w.incident(t, w.kim, w.desk)
	reply := w.reply(t, w.sam, inc, "x")
	narrow(inc, w.lea.Actor, w.sam.Actor)
	if w.sees(t, w.kim, inc) || w.sees(t, w.kim, reply) {
		t.Error("kim sees her incident narrowed to staff")
	}
	narrow(inc, w.lea.Actor, w.sam.Actor, "customer:kim")
	if !w.sees(t, w.kim, inc) || !w.sees(t, w.kim, reply) {
		t.Error("kim does not see her incident narrowed to staff and her")
	}

	// A narrowed portal admits only its customer readers.
	inc2 := w.incident(t, w.kim, w.desk2)
	leeInc := w.incident(t, w.lee, w.desk2)
	narrow(w.desk2, w.lea.Actor, w.sam.Actor, "customer:kim")
	if !w.sees(t, w.kim, w.desk2) || !w.sees(t, w.kim, inc2) {
		t.Error("kim, a reader of the narrowed portal, lost it or her incident in it")
	}
	if w.sees(t, w.lee, w.desk2) || w.sees(t, w.lee, leeInc) {
		t.Error("lee sees a portal narrowed away from her, or her incident in it")
	}
	if w.sees(t, w.eve, w.desk2) {
		t.Error("eve sees a portal narrowed to others")
	}

	// A reader of a story is still a customer: the rule admits no story.
	story := w.mk(t, w.lea, "story", 3, &w.space, map[string]any{"f_summary": "x"})
	narrow(story, w.lea.Actor, "customer:kim")
	if w.sees(t, w.kim, story) {
		t.Error("kim sees a story because she is among its readers")
	}
}

// The portal: read through its customer_fields, its links omitted (not
// restricted), its children filtered.
func TestCustomer_TheDesk_ThroughItsCustomerFields(t *testing.T) {
	w := newCustWorld(t)
	it, err := w.k.Get(testCtx(t), w.kim, w.desk)
	if err != nil {
		t.Fatalf("kim reads the portal: %v", err)
	}
	if got := slices.Sorted(maps.Keys(it.Fields)); !slices.Equal(got, []string{"f_key", "f_name"}) {
		t.Errorf("the portal's fields for kim: %v, want f_key and f_name", got)
	}
	if len(it.Links) != 0 || it.LinksTruncated {
		t.Errorf("kim sees the portal's links %+v; want none (calendar and sla_policy are not listed)", it.Links)
	}
	if len(it.Rollups) != 0 {
		t.Errorf("kim sees rollups %v on the portal", it.Rollups)
	}
	staff, _ := w.k.Get(testCtx(t), w.sam, w.desk)
	if len(staff.Links) != 2 {
		t.Errorf("sam sees %d links on the portal, want 2 (as today)", len(staff.Links))
	}
}

// ============================================================ 3. public and internal

// The reply is public by its Type, the note internal by its Type; the
// reactions follow the Type, with no field to read.
func TestCustomer_ReplyReopensAndResumes_ANoteDoesNothing(t *testing.T) {
	w := newCustWorld(t)
	inc, first, res := w.started(t, "P3") // kim's
	// Someone else's reply answers.
	answer := w.reply(t, w.sam, inc, "on it")
	out := w.sla.reactMove(t, itemservice.ReactionRunRequest{SourceID: inc, Reaction: "sla_answered", CauseItem: answer, CauseSeq: 1, Hop: 1})
	if out.Outcome != itemservice.ReactionApplied || w.sla.state(t, first) != "completed" {
		t.Fatalf("sam's reply = %+v, first response %s; want it answered", out, w.sla.state(t, first))
	}
	if err := w.move(t, w.sam, inc, "in_progress", nil); err != nil {
		t.Fatal(err)
	}
	if err := w.move(t, w.sam, inc, "waiting_for_customer", nil); err != nil {
		t.Fatal(err)
	}
	pause := w.sla.reactMove(t, entered(inc, itemSeqOf(t, inc), "sla_pause"))
	if pause.Outcome != itemservice.ReactionApplied || w.sla.state(t, res) != "paused" {
		t.Fatalf("pause = %+v; resolution %s", pause, w.sla.state(t, res))
	}
	// sam's note neither answers nor reopens.
	note := w.note(t, w.sam, inc, "internal")
	for _, r := range []string{"customer_replied", "sla_answered"} {
		o, err := w.k.Internal().ReactMove(testCtx(t), itemservice.ReactionRunRequest{SourceID: inc, Reaction: r, CauseItem: note, CauseSeq: 1, Hop: 1})
		if err == nil && o.Outcome == itemservice.ReactionApplied {
			t.Errorf("a note ran %s: %+v", r, o)
		}
	}
	// kim's reply reopens.
	hers := w.reply(t, w.kim, inc, "still broken")
	o := w.sla.reactMove(t, itemservice.ReactionRunRequest{SourceID: inc, Reaction: "customer_replied", CauseItem: hers, CauseSeq: 1, Hop: 1})
	if o.Outcome != itemservice.ReactionApplied || w.sla.state(t, inc) != "in_progress" {
		t.Fatalf("kim's reply = %+v, incident %s; want it back in progress", o, w.sla.state(t, inc))
	}
	// The resume runs for the event that brought it back: the reaction's
	// transition, not its outcome record after it.
	var back int64
	if err := pool.QueryRow(testCtx(t), `select max(item_seq) from item_events where item_id = $1 and op = 'transition'`, inc).Scan(&back); err != nil {
		t.Fatal(err)
	}
	resume := w.sla.reactMove(t, entered(inc, back, "sla_resume"))
	if resume.Outcome != itemservice.ReactionApplied || w.sla.state(t, res) != "running" {
		t.Errorf("resume = %+v, resolution %s; want running again", resume, w.sla.state(t, res))
	}
	// kim's own reply does not answer her.
	if o := w.sla.reactMove(t, itemservice.ReactionRunRequest{SourceID: inc, Reaction: "sla_answered", CauseItem: hers, CauseSeq: 1, Hop: 1}); o.Outcome == itemservice.ReactionApplied {
		t.Errorf("kim's reply answered her own incident: %+v", o)
	}
}

// ============================================================ 4. leaks

// Fields: only customer_fields, on every read; no etag for a customer.
func TestCustomer_Fields_OnlyTheListOnEveryRead(t *testing.T) {
	w := newCustWorld(t)
	inc, _, _ := w.started(t, "P3")
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: inc,
		Fields: map[string]any{"f_assignee": "user:sam", "f_impact": "minor"}}); err != nil {
		t.Fatal(err)
	}
	staff := fieldsOf(t, w.k, w.sam, inc)
	for _, f := range []string{"f_assignee", "f_due_first_response", "f_due_resolution"} {
		if staff[f] == nil {
			t.Fatalf("sam does not see %s: %v", f, staff)
		}
	}
	check := func(path string, it item.Item) {
		t.Helper()
		for f := range it.Fields {
			if f != "f_title" && f != "f_priority" && f != "f_impact" && f != "f_resolution" {
				t.Errorf("%s: kim sees %s", path, f)
			}
		}
		if it.Fields["f_title"] == nil || it.Fields["f_priority"] != "P3" || it.Fields["f_impact"] != "minor" {
			t.Errorf("%s: kim's fields %v, want her title, P3 and the impact", path, it.Fields)
		}
		if it.ContentSeq != 0 {
			t.Errorf("%s: kim gets content_seq %d; a customer gets no etag", path, it.ContentSeq)
		}
		if it.CreatedBy != "customer:kim" {
			t.Errorf("%s: creator %q", path, it.CreatedBy)
		}
	}
	got, err := w.k.Get(testCtx(t), w.kim, inc)
	if err != nil {
		t.Fatal(err)
	}
	check("Get", got)
	sr, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.kim, Filter: itemservice.Filter{TypeIDs: []string{"incident"}}})
	if err != nil || len(sr.Items) != 1 {
		t.Fatalf("Search: %v, %d items", err, len(sr.Items))
	}
	check("Search", sr.Items[0])
	lr, err := w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.kim, ContainerID: &w.desk})
	if err != nil || len(lr.Items) != 1 {
		t.Fatalf("List: %v, %d items", err, len(lr.Items))
	}
	check("List", lr.Items[0])
	// A staff creator is shown by name.
	phoned := w.mk(t, w.sam, "reply", 1, &inc, map[string]any{"f_body": "called you"})
	r, _ := w.k.Get(testCtx(t), w.kim, phoned)
	if r.CreatedBy != w.sam.Actor {
		t.Errorf("a staff reply's creator for kim: %q, want sam's", r.CreatedBy)
	}
}

// Search, List and counts: only theirs, paged right, filters and orders on
// hidden fields refused as if the field did not exist, words over visible
// fields only.
func TestCustomer_SearchAndList_OnlyTheirs_PagedRight(t *testing.T) {
	w := newCustWorld(t)
	var mine []uuid.UUID
	for range 3 {
		mine = append(mine, w.incident(t, w.kim, w.desk))
	}
	mine = append(mine, w.incident(t, w.kim, w.desk2))
	for range 2 {
		w.incident(t, w.lee, w.desk)
	}
	w.mk(t, w.sam, "incident", 2, &w.desk, map[string]any{"f_title": "VPN staff", "f_priority": "P1", "f_assignee": "user:sam"})
	replies := []uuid.UUID{w.reply(t, w.sam, mine[0], "vpn fixed?"), w.reply(t, w.kim, mine[0], "no")}
	w.note(t, w.sam, mine[0], "vpn note, internal")
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: mine[1],
		Fields: map[string]any{"f_assignee": "user:sam"}}); err != nil {
		t.Fatal(err)
	}

	for _, size := range []int{1, 2, 3, 50} {
		got := w.searchAll(t, w.kim, "", itemservice.Filter{TypeIDs: []string{"incident"}}, size)
		if !sameIDs(got, mine) {
			t.Errorf("kim's incidents, %d a page: %d found, want her %d", size, len(got), len(mine))
		}
	}
	all := w.searchAll(t, w.kim, "", anyType, 2)
	want := append(append([]uuid.UUID{w.desk, w.desk2}, mine...), replies...)
	if !sameIDs(all, want) {
		t.Errorf("anyType kim can find: %d Items, want %d (2 portals, 4 incidents, 2 replies)", len(all), len(want))
	}
	// Words: over the fields she sees, on her own rows.
	if got := w.searchAll(t, w.kim, "vpn", anyType, 50); !sameIDs(got, append(slices.Clone(mine), replies[0])) {
		t.Errorf("kim searches vpn: %d found, want her 4 incidents and sam's reply (not the note, not the staff incident)", len(got))
	}
	if got := w.searchAll(t, w.kim, "sam", anyType, 50); len(got) != 0 {
		t.Errorf("kim searches sam: %d found; the assignee is hidden", len(got))
	}
	// List: the top level is the portals; a portal holds her own.
	if got := w.listAll(t, w.kim, nil, 1); !sameIDs(got, []uuid.UUID{w.desk, w.desk2}) {
		t.Errorf("kim lists the top level: %v, want the two portals", got)
	}
	if got := w.listAll(t, w.kim, &w.desk, 2); !sameIDs(got, mine[:3]) {
		t.Errorf("kim lists the portal: %d, want her 3 there", len(got))
	}
	if got := w.listAll(t, w.kim, &mine[0], 1); !sameIDs(got, replies) {
		t.Errorf("kim lists her incident: %d, want the 2 replies", len(got))
	}

	// Hidden fields are refused as unknown ones are, in filters and order.
	_, errHidden := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.kim,
		Filter: itemservice.Filter{TypeIDs: []string{"incident"}, Fields: []itemservice.FieldFilter{{Field: "f_assignee", Eq: "me"}}}})
	_, errNone := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.kim,
		Filter: itemservice.Filter{TypeIDs: []string{"incident"}, Fields: []itemservice.FieldFilter{{Field: "f_nope", Eq: "me"}}}})
	var a, b *itemservice.InvalidError
	if !errors.As(errHidden, &a) || !errors.As(errNone, &b) || a.Path != b.Path {
		t.Errorf("filter on a hidden field: %v; on an unknown one: %v; want the same refusal", errHidden, errNone)
	}
	_, errOrder := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.kim,
		Filter: itemservice.Filter{TypeIDs: []string{"incident"}}, OrderBy: &itemservice.OrderBy{Field: "f_due_resolution"}})
	if !errors.As(errOrder, &a) || a.Path != "order_by" {
		t.Errorf("kim orders by a deadline: %v; want InvalidError on order_by", errOrder)
	}
	// Staff: as today.
	if got := w.searchAll(t, w.sam, "", itemservice.Filter{TypeIDs: []string{"incident"}}, 50); len(got) != 7 {
		t.Errorf("sam finds %d incidents, want 7", len(got))
	}
	// A token is the caller's: lee cannot use kim's.
	page, _ := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.kim, Filter: itemservice.Filter{TypeIDs: []string{"incident"}}, Limit: 1})
	if _, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.lee, Filter: itemservice.Filter{TypeIDs: []string{"incident"}},
		Limit: 1, PageToken: page.NextPageToken}); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("lee pages with kim's token: %v; want ErrInvalid", err)
	}
}

// History: every event kind an incident gets, and what a customer sees of it.
//
//	create                         shown; values: customer_fields only
//	transition (person, reaction)  shown; values: customer_fields only; a
//	                               cause naming an Item they cannot see dropped
//	update                         shown when it set a customer field, with
//	                               those values only; else omitted (the
//	                               kernel's due instants, the assignee)
//	update: readers, inherit       omitted
//	update: move                   omitted
//	link_added, link_removed       omitted (caused_by is not listed)
//	reaction_applied, _skipped,
//	_failed                        omitted (and so their targets)
//	hook_ran, hook_failed,
//	transition_requested, _denied,
//	_failed                        omitted
func TestCustomer_History_EveryEventKind(t *testing.T) {
	w := newCustWorld(t)
	inc, first, res := w.started(t, "P3")
	upd := func(p itemservice.UpdateRequest) {
		t.Helper()
		p.IdempotencyKey, p.ItemID = newKey(), inc
		if p.Principal.Actor == "" {
			p.Principal = w.sam
		}
		if _, err := w.k.Update(testCtx(t), p); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	upd(itemservice.UpdateRequest{Fields: map[string]any{"f_assignee": "user:sam"}})
	upd(itemservice.UpdateRequest{Fields: map[string]any{"f_priority": "P2", "f_assignee": "user:lea"}})
	if err := w.move(t, w.sam, inc, "in_progress", map[string]any{"f_impact": "minor", "f_assignee": "user:sam"}); err != nil {
		t.Fatal(err)
	}
	answer := w.reply(t, w.sam, inc, "on it")
	w.sla.reactMove(t, itemservice.ReactionRunRequest{SourceID: inc, Reaction: "sla_answered", CauseItem: answer, CauseSeq: 1, Hop: 1})
	w.sla.goalSets(t, first, itemSeqOf(t, first))
	if err := w.move(t, w.sam, inc, "waiting_for_customer", nil); err != nil {
		t.Fatal(err)
	}
	w.sla.reactMove(t, entered(inc, itemSeqOf(t, inc), "sla_pause"))
	w.sla.goalSets(t, res, itemSeqOf(t, res))
	hers := w.reply(t, w.kim, inc, "still broken")
	w.sla.reactMove(t, itemservice.ReactionRunRequest{SourceID: inc, Reaction: "customer_replied", CauseItem: hers, CauseSeq: 1, Hop: 1})
	bug := w.mk(t, w.sam, "bug", 3, nil, map[string]any{"f_summary": "vpn bug"})
	w.link(t, w.sam, inc, "caused_by", bug)
	if _, err := w.k.Unlink(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.sam, SourceID: inc, LinkType: "caused_by", TargetID: bug}); err != nil {
		t.Fatal(err)
	}
	upd(itemservice.UpdateRequest{Policy: &itemservice.PolicyPatch{Readers: []string{w.sam.Actor, w.lea.Actor, "customer:kim"}}})
	upd(itemservice.UpdateRequest{Move: &itemservice.Move{To: &w.desk2}})
	if err := w.move(t, w.sam, inc, "resolved", map[string]any{"f_resolution": "fixed"}); err != nil {
		t.Fatal(err)
	}
	if err := w.move(t, w.kim, inc, "in_progress", nil); err != nil {
		t.Fatalf("kim reopens: %v", err)
	}

	staff := allEvents(t, w.k, w.sam, inc)
	kims := allEvents(t, w.k, w.kim, inc)
	listed := []string{"f_title", "f_priority", "f_impact", "f_resolution"}
	ops := map[string]bool{}
	var want []itemservice.Event
	for _, e := range staff {
		ops[e.Op] = true
		switch {
		case e.Op == "create" || e.Op == "transition":
		case e.Op == "update" && !e.Moved && e.Readers == nil && !e.Inherit && slices.ContainsFunc(slices.Collect(maps.Keys(e.Values)),
			func(f string) bool { return slices.Contains(listed, f) }):
		default:
			continue
		}
		maps.DeleteFunc(e.Values, func(f string, _ any) bool { return !slices.Contains(listed, f) })
		want = append(want, e)
	}
	for _, op := range []string{"create", "transition", "update", lifecycle.OpLinkAdded, lifecycle.OpLinkRemoved,
		lifecycle.OpReactionApplied} {
		if !ops[op] {
			t.Errorf("the incident's history has no %s: the test no longer covers it", op)
		}
	}
	if len(kims) != len(want) {
		t.Errorf("kim sees %d events, want %d", len(kims), len(want))
	}
	for i := range min(len(kims), len(want)) {
		g, x := kims[i], want[i]
		if g.ItemSeq != x.ItemSeq || g.Op != x.Op || g.From != x.From || g.To != x.To || !maps.EqualFunc(g.Values, x.Values,
			func(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }) {
			t.Errorf("kim's event %d: seq %d %s %s->%s %v; want seq %d %s %s->%s %v",
				i, g.ItemSeq, g.Op, g.From, g.To, g.Values, x.ItemSeq, x.Op, x.From, x.To, x.Values)
		}
		if g.Readers != nil || g.Inherit || g.Moved || g.LinkTarget != nil || g.ReactionTarget != nil || g.Container != nil || g.Hook != "" {
			t.Errorf("kim's event %d (%s) carries %+v", i, g.Op, g)
		}
		// A cause is shown only when kim can see the Item it names.
		if g.CausationID != "" {
			id, _, _ := strings.Cut(g.CausationID, ":")
			if u, err := uuid.Parse(id); err != nil || !w.sees(t, w.kim, u) {
				t.Errorf("kim's event %d names cause %s, which she cannot see", i, g.CausationID)
			}
		}
	}
}

func allEvents(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) []itemservice.Event {
	t.Helper()
	var out []itemservice.Event
	token := ""
	for {
		res, err := k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: p, ItemID: id, Limit: 500, PageToken: token})
		if err != nil {
			t.Fatalf("ListEvents(%s): %v", p.Actor, err)
		}
		out = append(out, res.Events...)
		if token = res.NextPageToken; token == "" {
			return out
		}
	}
}

// Links and rollups on a customer's Item: only listed names, only visible
// ends; omitted, never restricted. Rollups only when listed, counting only
// what they see.
const kioskSrc = `
id: kiosk
version: 1
fields:
  - {id: f_name, type: string}
own: [kticket]
customer_fields: [f_name]
`

const kticketSrc = `
id: kticket
version: 1
initial_state: open
fields:
  - {id: f_title,  type: string}
  - {id: f_secret, type: string}
states:
  - {id: open, category: active}
  - {id: done, category: done}
transitions:
  - {from: open, to: done}
links:
  - {name: blocks,  inverse: blocked_by, targets: [kticket]}
  - {name: relates, inverse: related_to, targets: [kticket]}
rollups:
  - {id: progress,        over: blocks,  count_by: category}
  - {id: hidden_progress, over: relates, count_by: category}
customer_fields: [f_title, blocks, progress]
`

func registerSrc(t *testing.T, k *itemservice.Service, src string) {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register %s: %v", ty.ID, err)
	}
}

func TestCustomer_LinksAndRollups_OnlyListedAndVisible(t *testing.T) {
	w := newCustWorld(t)
	registerSrc(t, w.k, kticketSrc)
	registerSrc(t, w.k, kioskSrc)
	kiosk := w.mk(t, w.lea, "kiosk", 1, nil, map[string]any{"f_name": "k"})
	t1 := w.mk(t, w.kim, "kticket", 1, &kiosk, map[string]any{"f_title": "one"})
	t2 := w.mk(t, w.kim, "kticket", 1, &kiosk, map[string]any{"f_title": "two"})
	t3 := w.mk(t, w.lee, "kticket", 1, &kiosk, map[string]any{"f_title": "lee's"})
	w.link(t, w.sam, t1, "blocks", t2)
	w.link(t, w.sam, t1, "blocks", t3)
	w.link(t, w.sam, t1, "relates", t2)
	w.link(t, w.sam, t3, "blocks", t1)

	it, err := w.k.Get(testCtx(t), w.kim, t1)
	if err != nil {
		t.Fatal(err)
	}
	if len(it.Links) != 1 || it.Links[0].LinkType != "blocks" || it.Links[0].Other != t2 || it.Links[0].Restricted {
		t.Errorf("kim's links on t1: %+v; want only blocks -> t2 (lee's omitted, relates unlisted, lee's incoming omitted)", it.Links)
	}
	if p, ok := it.Rollups["progress"]; !ok || p.Counts["active"] != 1 {
		t.Errorf("kim's progress on t1: %+v; want 1 active (lee's ticket not counted)", it.Rollups)
	}
	if _, ok := it.Rollups["hidden_progress"]; ok {
		t.Error("kim gets a rollup that is not listed")
	}
	if _, ok := it.Fields["f_secret"]; ok {
		t.Error("kim sees f_secret")
	}
	in2, _ := w.k.Get(testCtx(t), w.kim, t2)
	if len(in2.Links) != 1 || in2.Links[0].LinkType != "blocked_by" || in2.Links[0].Other != t1 {
		t.Errorf("kim's links on t2: %+v; want blocked_by t1 only", in2.Links)
	}
	staff, _ := w.k.Get(testCtx(t), w.sam, t1)
	if len(staff.Links) != 4 || staff.Rollups["progress"].Counts["active"] != 2 {
		t.Errorf("sam on t1: %d links, rollups %v; want 4 and progress 2, as today", len(staff.Links), staff.Rollups)
	}
}

// Not found is not found: a hidden Item and a missing one, the same error.
func TestCustomer_HiddenAndMissing_TheSameAnswer(t *testing.T) {
	w := newCustWorld(t)
	lees := w.incident(t, w.lee, w.desk)
	note := w.note(t, w.sam, w.incident(t, w.kim, w.desk), "x")
	missing := uuid.New()
	for name, hidden := range map[string]uuid.UUID{"lee's incident": lees, "a note in her own": note, "the old desk": w.oldDesk} {
		_, e1 := w.k.Get(testCtx(t), w.kim, hidden)
		_, e2 := w.k.Get(testCtx(t), w.kim, missing)
		if !errors.Is(e1, itemservice.ErrNotFound) || e1.Error() != e2.Error() {
			t.Errorf("Get %s: %v; missing: %v; want the same ErrNotFound", name, e1, e2)
		}
		_, e1 = w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: w.kim, ItemID: hidden})
		_, e2 = w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: w.kim, ItemID: missing})
		if !errors.Is(e1, itemservice.ErrNotFound) || e1.Error() != e2.Error() {
			t.Errorf("ListEvents %s: %v; missing: %v", name, e1, e2)
		}
		_, e1 = w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.kim, ContainerID: &hidden})
		_, e2 = w.k.List(testCtx(t), itemservice.ListRequest{Principal: w.kim, ContainerID: &missing})
		if !errors.Is(e1, itemservice.ErrNotFound) || e1.Error() != e2.Error() {
			t.Errorf("List %s: %v; missing: %v", name, e1, e2)
		}
	}
}

// Notifications: a customer is never a principal field's value, so never a
// breach contact.
func TestCustomer_NeverABreachContact(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	_, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: inc,
		Fields: map[string]any{"f_assignee": "customer:kim"}})
	var inv *itemservice.InvalidError
	if !errors.As(err, &inv) || inv.Field != "f_assignee" {
		t.Errorf("sam assigns the incident to kim: %v; want InvalidError on f_assignee", err)
	}
}

// ============================================================ 5. agents

var customerPowers = []string{item.PowerRead, item.PowerCreate, item.PowerComment, item.PowerTransitionAll}

func (w custWorld) portalBot(t *testing.T, agent string, c item.Principal) item.Principal {
	t.Helper()
	p := item.Principal{Actor: agent, OnBehalfOf: c.Actor, Namespace: w.ns}
	all := []uuid.UUID{uuid.Nil}
	session, err := w.k.StartAgentSession(testCtx(t), itemservice.StartAgentSessionRequest{
		Principal: p, Scopes: all, Powers: customerPowers, Grant: &itemservice.Grant{Scopes: all, Powers: customerPowers},
	})
	if err != nil {
		t.Fatalf("a portal bot for %s: %v", c.Actor, err)
	}
	p.AgentSession = session
	return p
}

func TestCustomer_AnAgentForACustomer_GetsTheCustomersView(t *testing.T) {
	w := newCustWorld(t)
	bot := w.portalBot(t, "agent:portal-"+word(), w.kim)
	inc := w.incident(t, w.kim, w.desk)
	lees := w.incident(t, w.lee, w.desk)
	note := w.note(t, w.sam, inc, "x")
	if !w.sees(t, bot, inc) || w.sees(t, bot, lees) || w.sees(t, bot, note) || w.sees(t, bot, w.story) {
		t.Error("the bot for kim: want her incident only, not lee's, the note or the story")
	}
	it, _ := w.k.Get(testCtx(t), bot, inc)
	if _, ok := it.Fields["f_assignee"]; ok || it.ContentSeq != 0 {
		t.Errorf("the bot for kim reads %v, content_seq %d; want customer fields and no etag", it.Fields, it.ContentSeq)
	}
	// It writes as kim: a reply, which is the requester's.
	r, err := w.tryCreate(t, bot, "reply", 1, &inc, map[string]any{"f_body": "from the portal"})
	if err != nil {
		t.Fatalf("the bot replies for kim: %v", err)
	}
	if got, _ := w.k.Get(testCtx(t), w.sam, r); got.CreatedBy != bot.Actor || got.CreatedOnBehalfOf != "customer:kim" {
		t.Errorf("the bot's reply: by %s for %s", got.CreatedBy, got.CreatedOnBehalfOf)
	}
	// And no more than kim: no update, no note.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: bot, ItemID: inc,
		Fields: map[string]any{"f_title": "x"}}); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("the bot updates kim's incident: %v; want ErrForbidden", err)
	}
	if _, err := w.tryCreate(t, bot, "note", 1, &inc, map[string]any{"f_body": "x"}); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("the bot writes a note: %v; want ErrForbidden", err)
	}
	if err := w.move(t, bot, inc, "canceled", nil); err != nil {
		t.Errorf("the bot cancels kim's new incident for her: %v", err)
	}
}

// The fixed set: read, create, comment, transition:*; nothing else, even
// when the grant says more.
func TestCustomer_AgentSession_OnlyTheFixedPowers(t *testing.T) {
	w := newCustWorld(t)
	all := []uuid.UUID{uuid.Nil}
	for _, extra := range []string{item.PowerEditFields, item.PowerMove, item.PowerOutbound} {
		powers := append(slices.Clone(customerPowers), extra)
		_, err := w.k.StartAgentSession(testCtx(t), itemservice.StartAgentSessionRequest{
			Principal: item.Principal{Actor: "agent:portal-" + word(), OnBehalfOf: w.kim.Actor, Namespace: w.ns},
			Scopes:    all, Powers: powers, Grant: &itemservice.Grant{Scopes: all, Powers: powers},
		})
		if !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("a session for kim with %s: %v; want ErrForbidden", extra, err)
		}
	}
	// A customer never starts a session as an agent, nor is one.
	_, err := w.k.StartAgentSession(testCtx(t), itemservice.StartAgentSessionRequest{
		Principal: w.kim, Scopes: all, Powers: customerPowers, Grant: &itemservice.Grant{Scopes: all, Powers: customerPowers},
	})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("kim starts a session herself: %v; want ErrInvalid", err)
	}
}

// One agent, two people it acts for, one key: two writes. The idempotency
// key of an agent belongs to the agent and the user it acts for.
func TestIdempotency_AnAgentForTwoPeople_TheSameKey_TwoWrites(t *testing.T) {
	w := newCustWorld(t)
	agent := "agent:portal-" + word()
	forKim, forLee := w.portalBot(t, agent, w.kim), w.portalBot(t, agent, w.lee)
	kims, lees := w.incident(t, w.kim, w.desk), w.incident(t, w.lee, w.desk)
	key := newKey()
	create := func(p item.Principal, in uuid.UUID) (itemservice.WriteResult, error) {
		return w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: key, Principal: p, TypeID: "reply", TypeVersion: 1,
			ContainerID: &in, Fields: map[string]any{"f_body": "same words"}})
	}
	a, err := create(forKim, kims)
	if err != nil {
		t.Fatal(err)
	}
	b, err := create(forLee, lees)
	if err != nil || b.ItemID == a.ItemID {
		t.Errorf("the bot for lee with kim's key: %+v, %v; want a new reply of lee's", b, err)
	}
	if again, err := create(forKim, kims); err != nil || again != a {
		t.Errorf("the bot for kim, again: %+v, %v; want kim's first result %+v", again, err, a)
	}
	// The same for staff: an agent acting for ana and for sam.
	userAgent := "agent:triage-" + word()
	forSam := withSession(t, w.k, item.Principal{Actor: userAgent, OnBehalfOf: w.sam.Actor, Namespace: w.ns})
	forLea := withSession(t, w.k, item.Principal{Actor: userAgent, OnBehalfOf: w.lea.Actor, Namespace: w.ns})
	key = newKey()
	// An empty note: note@1 opens no field to agents (closed by default),
	// and the key, not the content, is what this checks.
	note := func(p item.Principal) (itemservice.WriteResult, error) {
		return w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: key, Principal: p, TypeID: "note", TypeVersion: 1,
			ContainerID: &kims})
	}
	if _, err := note(forSam); err != nil {
		t.Fatal(err)
	}
	if _, err := note(forLea); err != nil {
		t.Errorf("the agent for lea with the key it used for sam: %v; want a write of its own", err)
	}
}

// ============================================================ 6. staff for a customer

func TestRequester_StaffOpenAnIncidentForACustomer(t *testing.T) {
	w := newCustWorld(t)
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.sam, TypeID: "incident", TypeVersion: 2,
		ContainerID: &w.desk, Fields: map[string]any{"f_title": "phoned in", "f_priority": "P2", "f_assignee": "user:sam"}, Requester: "customer:kim"})
	if err != nil {
		t.Fatalf("sam opens an incident for kim: %v", err)
	}
	it, _ := w.k.Get(testCtx(t), w.sam, res.ItemID)
	if it.CreatedBy != w.sam.Actor || it.CreatedOnBehalfOf != "customer:kim" {
		t.Errorf("by %s for %s; want sam for customer:kim", it.CreatedBy, it.CreatedOnBehalfOf)
	}
	if !w.sees(t, w.kim, res.ItemID) || w.sees(t, w.lee, res.ItemID) {
		t.Error("want kim to see it and lee not to")
	}
	if got := w.searchAll(t, w.kim, "", itemservice.Filter{TypeIDs: []string{"incident"}}, 50); !sameIDs(got, []uuid.UUID{res.ItemID}) {
		t.Errorf("kim's search finds %v", got)
	}
	// She is its requester: her reply reopens, and she may cancel it.
	if err := w.move(t, w.kim, res.ItemID, "canceled", nil); err != nil {
		t.Errorf("kim cancels the incident opened for her: %v", err)
	}
	// The create event says who and for whom.
	ev := allEvents(t, w.k, w.kim, res.ItemID)
	if len(ev) == 0 || ev[0].Actor != w.sam.Actor || ev[0].OnBehalfOf != "customer:kim" {
		t.Errorf("kim's first event: %+v", ev)
	}
}

func TestRequester_Refused_WithNoHintAboutTheCustomer(t *testing.T) {
	w := newCustWorld(t)
	ask := func(p item.Principal, typ string, version int, in *uuid.UUID, requester string) error {
		_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: p, TypeID: typ, TypeVersion: version,
			ContainerID: in, Fields: map[string]any{"f_title": "x", "f_priority": "P3"}, Requester: requester})
		return err
	}
	isRequester := func(err error) bool {
		var inv *itemservice.InvalidError
		return errors.As(err, &inv) && inv.Path == "requester"
	}
	cases := map[string]error{
		"a user, not a customer":         ask(w.sam, "incident", 2, &w.desk, "user:ana"),
		"an untyped requester":           ask(w.sam, "incident", 2, &w.desk, "kim"),
		"a desk that admits nobody":      ask(w.sam, "incident", 1, &w.oldDesk, "customer:kim"),
		"at the top level":               ask(w.sam, "incident", 2, nil, "customer:kim"),
		"a Type the portal does not own": ask(w.sam, "note", 1, &w.desk, "customer:kim"),
		"asked by a customer":            ask(w.kim, "incident", 2, &w.desk, "customer:lee"),
		"asked by an agent for sam (Q6)": ask(withSession(t, w.k, item.Principal{Actor: "agent:t-" + word(), OnBehalfOf: w.sam.Actor, Namespace: w.ns}), "incident", 2, &w.desk, "customer:kim"),
		"asked by a portal bot for kim":  ask(w.portalBot(t, "agent:p-"+word(), w.kim), "incident", 2, &w.desk, "customer:kim"),
	}
	for name, err := range cases {
		if !isRequester(err) {
			t.Errorf("%s: %v; want InvalidError on requester", name, err)
		}
	}
	// A narrowed portal: a customer it hides and a customer that never
	// existed are refused in the same words.
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: w.desk2,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.lea.Actor, w.sam.Actor, "customer:lee"}}}); err != nil {
		t.Fatal(err)
	}
	hidden := ask(w.sam, "incident", 2, &w.desk2, "customer:kim")
	ghost := ask(w.sam, "incident", 2, &w.desk2, "customer:nobody-"+word())
	if !isRequester(hidden) || !isRequester(ghost) || hidden.Error() != ghost.Error() {
		t.Errorf("kim hidden by the portal: %v; a customer that never was: %v; want the same refusal", hidden, ghost)
	}
	if err := ask(w.sam, "incident", 2, &w.desk2, "customer:lee"); err != nil {
		t.Errorf("for lee, a reader of the portal: %v", err)
	}
}
