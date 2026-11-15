// Tests for Step 0, part 2: an Item's history (ListEvents), projected for the
// caller.
//
//   - Anyone who can see the Item sees its whole history, oldest first by
//     item_seq, paged; an Item they cannot see is ErrNotFound.
//   - Each event shows its shape, its attribution (never the agent session)
//     and the values it set, read from item_event_values. A cleared field is
//     there with a nil value; an erased value is named in Erased, not missing.
//   - Reader lists are shown to anyone who sees the Item. Containers the
//     caller cannot see now are Restricted, with no ID.
//   - An agent's history read records the Item's narrowing scope.
package itemservice_test

import (
	"errors"
	"maps"
	"slices"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func history(t *testing.T, k *itemservice.Service, p item.Principal, id uuid.UUID) []itemservice.Event {
	t.Helper()
	res, err := k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: p, ItemID: id, Limit: 500})
	if err != nil {
		t.Fatalf("ListEvents as %s: %v", p.Actor, err)
	}
	if res.NextPageToken != "" {
		t.Fatalf("history of %s has more than one page", id)
	}
	return res.Events
}

// One Item through create, patch (with a clear), transition: each event in
// order, with its shape, values and attribution.
func TestHistory_EventsInOrderWithValues(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	ana, bob := in(person("ana"), ns), in(person("bob"), ns)
	box := createIn(t, k, ana, nil, nil)
	id := createIn(t, k, ana, map[string]any{"f_title": "printer", "f_priority": 2}, &box)
	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: bob, ItemID: id,
		Fields: map[string]any{"f_assignee": "user-bob", "f_priority": nil},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: bob, ItemID: id, ToState: "in_progress",
	}); err != nil {
		t.Fatalf("transition: %v", err)
	}

	evs := history(t, k, bob, id)
	if len(evs) != 3 {
		t.Fatalf("history has %d events, want 3: %+v", len(evs), evs)
	}
	for i, e := range evs {
		if e.ItemID != id || e.ItemSeq != int64(i+1) || e.CommitSeq == 0 || e.CreatedAt.IsZero() {
			t.Errorf("event %d: %+v, want item_seq %d of %s with a commit_seq and a time", i, e, i+1, id)
		}
	}

	create := evs[0]
	if create.Op != "create" || create.Actor != ana.Actor || create.TypeID != "ticket" || create.TypeVersion != 1 ||
		create.To != "open" {
		t.Errorf("create = %+v", create)
	}
	if !maps.Equal(create.Values, map[string]any{"f_title": "printer", "f_priority": float64(2)}) {
		t.Errorf("create values = %v", create.Values)
	}
	if create.Container == nil || create.Container.Restricted || create.Container.ID != box {
		t.Errorf("create container = %+v, want %s", create.Container, box)
	}

	upd := evs[1]
	if upd.Op != "update" || upd.Actor != bob.Actor {
		t.Errorf("update = %+v", upd)
	}
	// A cleared field is there, with a nil value.
	if v, ok := upd.Values["f_priority"]; !ok || v != nil || upd.Values["f_assignee"] != "user-bob" || len(upd.Values) != 2 {
		t.Errorf("update values = %v, want f_assignee set and f_priority cleared (nil)", upd.Values)
	}
	if len(upd.Erased) != 0 {
		t.Errorf("update erased = %v, want none", upd.Erased)
	}

	tr := evs[2]
	if tr.Op != "transition" || tr.From != "open" || tr.To != "in_progress" || len(tr.Values) != 0 {
		t.Errorf("transition = %+v", tr)
	}
}

// An erased value shows as erased, not missing: a whole values row deleted,
// or one field removed from it.
func TestHistory_ErasedValuesAreNamed(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	ana := in(person("ana"), ns)
	id := createIn(t, k, ana, map[string]any{"f_title": "secret", "f_reporter": "Jane Doe"}, nil)
	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, Fields: map[string]any{"f_title": "also secret"},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	// Erasure (docs/design.md, "Events and personal data"): one field from
	// event 1, the whole row of event 2.
	if _, err := pool.Exec(testCtx(t),
		`update item_event_values set field_values = field_values - 'f_reporter' where item_id = $1 and item_seq = 1`, id); err != nil {
		t.Fatalf("erase one field: %v", err)
	}
	if _, err := pool.Exec(testCtx(t), `delete from item_event_values where item_id = $1 and item_seq = 2`, id); err != nil {
		t.Fatalf("erase a row: %v", err)
	}

	evs := history(t, k, ana, id)
	if len(evs) != 2 {
		t.Fatalf("history has %d events, want 2", len(evs))
	}
	if !maps.Equal(evs[0].Values, map[string]any{"f_title": "secret"}) || !slices.Equal(evs[0].Erased, []string{"f_reporter"}) {
		t.Errorf("event 1: values %v, erased %v; want f_title kept, f_reporter erased", evs[0].Values, evs[0].Erased)
	}
	if len(evs[1].Values) != 0 || !slices.Equal(evs[1].Erased, []string{"f_title"}) {
		t.Errorf("event 2: values %v, erased %v; want f_title erased", evs[1].Values, evs[1].Erased)
	}
}

// Containers the caller cannot see now are restricted; reader lists are
// shown to anyone who sees the Item.
func TestHistory_RestrictedContainersAndReaders(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	ana, bob := in(person("ana"), ns), in(person("bob"), ns)
	from := createIn(t, k, ana, nil, nil)
	to := createIn(t, k, ana, nil, nil)
	id := createIn(t, k, ana, nil, &from)
	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, Move: &itemservice.Move{To: &to},
	}); err != nil {
		t.Fatalf("move: %v", err)
	}
	setPolicy(t, k, ana, id, itemservice.PolicyPatch{Readers: readers(ana, bob)})
	setPolicy(t, k, ana, from, itemservice.PolicyPatch{Readers: readers(ana)}) // bob loses the old container

	for _, c := range []struct {
		who            item.Principal
		fromRestricted bool
	}{{ana, false}, {bob, true}} {
		evs := history(t, k, c.who, id)
		if len(evs) != 3 {
			t.Fatalf("%s: %d events, want 3", c.who.Actor, len(evs))
		}
		wantFrom := &itemservice.ItemRef{ID: from}
		if c.fromRestricted {
			wantFrom = &itemservice.ItemRef{Restricted: true}
		}
		if got := evs[0].Container; got == nil || *got != *wantFrom {
			t.Errorf("%s: create container = %+v, want %+v", c.who.Actor, got, wantFrom)
		}
		mv := evs[1]
		if !mv.Moved || mv.ContainerFrom == nil || *mv.ContainerFrom != *wantFrom ||
			mv.ContainerTo == nil || *mv.ContainerTo != (itemservice.ItemRef{ID: to}) {
			t.Errorf("%s: move = from %+v to %+v (moved %v), want from %+v to %s",
				c.who.Actor, mv.ContainerFrom, mv.ContainerTo, mv.Moved, wantFrom, to)
		}
		narrow := evs[2]
		if want := slices.Sorted(slices.Values(readers(ana, bob))); !slices.Equal(narrow.Readers, want) {
			t.Errorf("%s: readers = %v, want %v", c.who.Actor, narrow.Readers, want)
		}
	}

	// The top level is no container: a create there names none, and a move
	// from it has no ContainerFrom.
	loose := createIn(t, k, ana, nil, nil)
	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: loose, Move: &itemservice.Move{To: &to},
	}); err != nil {
		t.Fatalf("move from the top: %v", err)
	}
	evs := history(t, k, bob, loose)
	if evs[0].Container != nil {
		t.Errorf("a top-level create names a container: %+v", evs[0].Container)
	}
	if mv := evs[1]; !mv.Moved || mv.ContainerFrom != nil || mv.ContainerTo == nil || mv.ContainerTo.ID != to {
		t.Errorf("move from the top into %s: from %+v to %+v", to, mv.ContainerFrom, mv.ContainerTo)
	}
}

// Agent attribution shows the agent and its user, never the session.
func TestHistory_AgentAttribution(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	tom := in(person("tom"), ns)
	bot := scoped(t, k, tom, []uuid.UUID{uuid.Nil}, item.PowerCreate)
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: bot, TypeID: "ticket", TypeVersion: 1,
		Attribution: item.Attribution{CorrelationID: "corr-1"},
	})
	if err != nil {
		t.Fatalf("agent creates: %v", err)
	}
	e := history(t, k, tom, res.ItemID)[0]
	if e.Actor != bot.Actor || e.OnBehalfOf != tom.Actor || e.CorrelationID != "corr-1" || e.Hop != 0 {
		t.Errorf("attribution = %+v, want %s for %s, correlation corr-1", e, bot.Actor, tom.Actor)
	}
}

// Hidden, missing and other-namespace Items are ErrNotFound alike.
func TestHistory_NotFound(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	ana, bob := in(person("ana"), ns), in(person("bob"), ns)
	private, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: ana, TypeID: "ticket", TypeVersion: 1,
		Narrow: &itemservice.Narrow{Readers: readers(ana)},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for name, c := range map[string]struct {
		p  item.Principal
		id uuid.UUID
	}{
		"hidden":          {bob, private.ItemID},
		"missing":         {bob, uuid.New()},
		"other namespace": {in(ana, newNamespace(t, k)), private.ItemID},
	} {
		_, err := k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: c.p, ItemID: c.id})
		assertNotFound(t, name, err)
	}
}

// History pages oldest first; the token is bound like any other.
func TestHistory_Paging(t *testing.T) {
	k := itemservice.New(pool)
	ns := newNamespace(t, k)
	ana := in(person("ana"), ns)
	id := createIn(t, k, ana, nil, nil)
	for range 6 {
		if err := agentUpdate(t, k, ana, id); err != nil {
			t.Fatalf("update: %v", err)
		}
	}
	var seqs []int64
	all, pages := pageAll(t, func(token string) ([]uuid.UUID, string, error) {
		res, err := k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: ana, ItemID: id, Limit: 3, PageToken: token})
		for _, e := range res.Events {
			seqs = append(seqs, e.ItemSeq)
		}
		return make([]uuid.UUID, len(res.Events)), res.NextPageToken, err
	})
	if len(all) != 7 || pages != 3 || !slices.Equal(seqs, []int64{1, 2, 3, 4, 5, 6, 7}) {
		t.Errorf("history pages: %d pages, seqs %v; want 3 pages of 1..7", pages, seqs)
	}

	first, err := k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: ana, ItemID: id, Limit: 3})
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	other := createIn(t, k, ana, nil, nil)
	_, err = k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: ana, ItemID: other, Limit: 3, PageToken: first.NextPageToken})
	assertInvalidAt(t, "another Item's token", err, "page_token")
}

// An agent reading a narrowed Item's history records that scope.
func TestHistory_AgentReadsAreRecorded(t *testing.T) {
	w := newAgentWorld(t)
	// The Step A2 world is in the default namespace; ListEvents needs it named.
	agent := in(w.agent, "default")
	if _, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: agent, ItemID: w.inSpace}); err != nil {
		t.Fatalf("ListEvents as the agent: %v", err)
	}
	if got := recordedReads(t, w.agent.AgentSession); !slices.Contains(got, scopeOf(t, w.inSpace)) {
		t.Errorf("recorded reads %v, want the narrowed space's scope", got)
	}
}

// --- rejections carry their details as data ---

// Rule refusals carry what the caller needs as metadata, not only as text:
// the states of the move, the missing fields.
func TestRejected_CarryMetadata(t *testing.T) {
	k := itemservice.New(pool)
	ana := person("ana")
	id := createAs(t, k, ana, "x", nil, nil).ItemID

	transition := func(to string) *itemservice.RejectedError {
		t.Helper()
		_, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
			IdempotencyKey: newKey(), Principal: ana, ItemID: id, ToState: to,
		})
		var rej *itemservice.RejectedError
		if !errors.As(err, &rej) {
			t.Fatalf("transition to %s: error = %v, want a RejectedError", to, err)
		}
		return rej
	}
	guard := transition("in_progress") // f_assignee is not set
	if guard.Code != itemservice.CodeGuardFailed ||
		guard.Metadata["from_state"] != "open" || guard.Metadata["to_state"] != "in_progress" {
		t.Errorf("guard refusal = %+v, want guard_failed with from_state open, to_state in_progress", guard)
	}
	none := transition("resolved")
	if none.Code != itemservice.CodeNoTransition ||
		none.Metadata["from_state"] != "open" || none.Metadata["to_state"] != "resolved" {
		t.Errorf("no-transition refusal = %+v", none)
	}

	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, Fields: map[string]any{"f_assignee": "user-bob"},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if _, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: id, ToState: "in_progress",
	}); err != nil {
		t.Fatalf("to in_progress: %v", err)
	}
	missing := transition("resolved") // needs f_resolution
	if missing.Code != itemservice.CodeMissingFields || missing.Metadata["fields"] != "f_resolution" {
		t.Errorf("missing-fields refusal = %+v, want fields = f_resolution", missing)
	}
}
