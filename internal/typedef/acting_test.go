// Tests for proof-of-concept Step 2, part 3: the Type side of acting
// timers and field-less actions.
//
//   - A state may declare acting timers {id, after, every, max, hook}: max
//     (the total number of runs) is required and at least 1; every is
//     required when max > 1; the hook is an action hook of the same Type
//     with on: timer, and each such hook is named by exactly one acting
//     timer.
//   - A hook on: reaction names an after reaction (an undo) of the same
//     Type, and is an action hook.
//   - An action hook may write nothing only when its action writes nothing
//     (email.notify): hook_ran then records that it ran.
//   - creator (created_on_behalf_of, else created_by) is a hook attribute.
package typedef_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// ticketSrc: a state with an acting timer, a mail hook on entering a state
// and an on: reaction hook (the jar/pledge pair of reactions_test.go
// supplies a reaction).
const remindSrc = `
id: pledge
version: 1
initial_state: open
fields:
  - {id: f_amount, type: int}
  - {id: f_owner,  type: principal}
states:
  - {id: open,    category: todo}
  - id: filed
    category: active
    acting:
      - {id: nudge, after: 48h, every: 24h, max: 2, hook: nudge_owner}
  - {id: paid,    category: done}
  - {id: dropped, category: canceled}
transitions:
  - {from: open,  to: filed}
  - {from: filed, to: paid}
  - {from: filed, to: dropped, escape: true}
  - {from: paid,  to: dropped}
links:
  - {name: jar, inverse: pledges, targets: [jar], max: one, writable_by: kernel,
     resolve: {on_enter: filed, field: f_holder, equals: creator, state: active}}
reactions:
  - {id: take, kind: held, on: enter, state: paid, from: [filed], link: jar, add: {f_level: f_amount}}
  - {id: give_back, kind: after, on: enter, state: dropped, from: [paid], undoes: take}
hooks:
  - {id: nudge_owner, on: timer, kind: action, action: email.notify, reads: [f_owner]}
  - {id: tell_paid,   on: enter, state: paid, kind: action, action: email.notify, reads: [creator]}
  - {id: tell_back,   on: reaction, reaction: give_back, kind: action, action: email.notify, reads: [creator]}
`

const nudgeLine = "      - {id: nudge, after: 48h, every: 24h, max: 2, hook: nudge_owner}"

func TestActingTimers_Load(t *testing.T) {
	ty := mustParse(t, remindSrc)
	s, _ := ty.State("filed")
	if len(s.Acting) != 1 {
		t.Fatalf("filed acting = %+v, want one", s.Acting)
	}
	a := s.Acting[0]
	if a.ID != "nudge" || a.Hook != "nudge_owner" || a.Max != 2 ||
		a.AfterDuration != 48*time.Hour || a.EveryDuration != 24*time.Hour {
		t.Errorf("acting timer = %+v", a)
	}
	if h, ok := ty.Hook("tell_back"); !ok || h.On != typedef.OnReaction || h.Reaction != "give_back" {
		t.Errorf("tell_back = %+v", h)
	}
	// Stored as JSON, read back: the timer and its parsed durations survive.
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatal(err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if s, _ := back.State("filed"); len(s.Acting) != 1 || s.Acting[0].EveryDuration != 24*time.Hour {
		t.Errorf("after the round trip: %+v", s.Acting)
	}
}

// One run needs no interval.
func TestActingTimers_OneRunNeedsNoEvery(t *testing.T) {
	mustParse(t, swap(t, remindSrc, nudgeLine, "      - {id: nudge, after: 48h, max: 1, hook: nudge_owner}"))
}

func TestActingTimers_Refused(t *testing.T) {
	acting := func(line string) string { return swap(t, remindSrc, nudgeLine, line) }
	cases := []struct{ name, src, mention string }{
		{"no max", acting("      - {id: nudge, after: 48h, every: 24h, hook: nudge_owner}"), "nudge"},
		{"max 0", acting("      - {id: nudge, after: 48h, every: 24h, max: 0, hook: nudge_owner}"), "nudge"},
		{"repeats without every", acting("      - {id: nudge, after: 48h, max: 2, hook: nudge_owner}"), "nudge"},
		{"no after", acting("      - {id: nudge, every: 24h, max: 2, hook: nudge_owner}"), "nudge"},
		{"a zero after", acting("      - {id: nudge, after: 0s, every: 24h, max: 2, hook: nudge_owner}"), "nudge"},
		{"a bad duration", acting("      - {id: nudge, after: 2d, every: 24h, max: 2, hook: nudge_owner}"), "nudge"},
		{"a negative every", acting("      - {id: nudge, after: 48h, every: -1h, max: 2, hook: nudge_owner}"), "nudge"},
		{"an unknown hook", acting("      - {id: nudge, after: 48h, every: 24h, max: 2, hook: nudge_nobody}"), "nudge_nobody"},
		{"a hook not on: timer", acting("      - {id: nudge, after: 48h, every: 24h, max: 2, hook: tell_paid}"), "tell_paid"},
		{"no id", acting("      - {after: 48h, every: 24h, max: 2, hook: nudge_owner}"), "filed"},
		{"two timers, one hook", acting(nudgeLine + "\n      - {id: nudge2, after: 72h, max: 1, hook: nudge_owner}"), "nudge_owner"},
		{"two timers, one id", swap(t, acting(nudgeLine+"\n      - {id: nudge, after: 72h, max: 1, hook: nudge_owner}"),
			"  - {id: nudge_owner,", "  - {id: nudge_other, on: timer, kind: action, action: email.notify, reads: [f_owner]}\n  - {id: nudge_owner,"), "nudge"},
		// A hook on: timer that no acting timer names would never run.
		{"a timer hook no timer names", acting(""), "nudge_owner"},
		{"a timer hook that is not an action", swap(t, remindSrc,
			"{id: nudge_owner, on: timer, kind: action, action: email.notify, reads: [f_owner]}",
			"{id: nudge_owner, on: timer, kind: enrich, action: crm.get_plan, reads: [f_owner], writes: [f_amount]}"), "nudge_owner"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
}

func TestReactionHooks_Refused(t *testing.T) {
	line := "{id: tell_back,   on: reaction, reaction: give_back, kind: action, action: email.notify, reads: [creator]}"
	cases := []struct{ name, src, mention string }{
		{"an unknown reaction", swap(t, remindSrc, line,
			"{id: tell_back,   on: reaction, reaction: give_more, kind: action, action: email.notify, reads: [creator]}"), "give_more"},
		{"a held reaction (only an undo is applied this way)", swap(t, remindSrc, line,
			"{id: tell_back,   on: reaction, reaction: take, kind: action, action: email.notify, reads: [creator]}"), "take"},
		{"no reaction", swap(t, remindSrc, line,
			"{id: tell_back,   on: reaction, kind: action, action: email.notify, reads: [creator]}"), "tell_back"},
		{"not an action", swap(t, remindSrc, line,
			"{id: tell_back,   on: reaction, reaction: give_back, kind: enrich, action: crm.get_plan, reads: [creator], writes: [f_amount]}"), "tell_back"},
		{"a reaction on an enter hook", swap(t, remindSrc,
			"{id: tell_paid,   on: enter, state: paid, kind: action, action: email.notify, reads: [creator]}",
			"{id: tell_paid,   on: enter, state: paid, reaction: give_back, kind: action, action: email.notify, reads: [creator]}"), "tell_paid"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
}

// An action hook writes nothing only when its action writes nothing: an
// action that makes something (an invoice) must write it back.
func TestFieldlessActions(t *testing.T) {
	mustParse(t, remindSrc) // email.notify, writes: []
	mustRefuse(t, "an invoice action that writes nothing", swap(t, remindSrc,
		"{id: tell_paid,   on: enter, state: paid, kind: action, action: email.notify, reads: [creator]}",
		"{id: tell_paid,   on: enter, state: paid, kind: action, action: erp.create_invoice, reads: [creator, f_amount]}"),
		"tell_paid")
	mustRefuse(t, "email.notify told to write a field", swap(t, remindSrc,
		"{id: tell_paid,   on: enter, state: paid, kind: action, action: email.notify, reads: [creator]}",
		"{id: tell_paid,   on: enter, state: paid, kind: action, action: email.notify, reads: [creator], writes: [f_amount]}"),
		"tell_paid")
}

// creator is a hook attribute, and a reserved name: no field may take it.
func TestCreatorAttribute(t *testing.T) {
	if !typedef.IsAttribute(typedef.AttrCreator) {
		t.Error("creator is not a hook attribute")
	}
	mustRefuse(t, "a field named creator", swap(t, remindSrc, "  - {id: f_amount, type: int}",
		"  - {id: f_amount, type: int}\n  - {id: creator, type: principal}"), "creator")
}

// The repository's vacation request v3.
func TestRepositoryTypes_VacationV3(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	var v3 *typedef.Type
	for _, ty := range types {
		if ty.ID == "vacation_request" && ty.Version == 3 {
			v3 = ty
		}
	}
	if v3 == nil {
		t.Fatal("types/ has no vacation_request v3")
	}
	s, _ := v3.State("submitted")
	if s.Timer == nil || s.Timer.To != "expired" || s.Timer.Duration != 120*time.Hour {
		t.Errorf("submitted timer = %+v, want 120h to expired", s.Timer)
	}
	if len(s.Acting) != 1 || s.Acting[0].ID != "remind" || s.Acting[0].Max != 2 ||
		s.Acting[0].AfterDuration != 48*time.Hour || s.Acting[0].EveryDuration != 24*time.Hour ||
		s.Acting[0].Hook != "remind_approver" {
		t.Errorf("submitted acting = %+v", s.Acting)
	}
	if e, ok := v3.State("expired"); !ok || e.Category != "canceled" {
		t.Errorf("expired = %+v, %v", e, ok)
	}
	if tr, ok := v3.Transition("submitted", "expired"); !ok || strings.Join(tr.Allowed, ",") != "principal:system:timer" {
		t.Errorf("submitted -> expired = %+v, %v", tr, ok)
	}
	for _, id := range []string{"remind_approver", "notify_approved", "notify_rejected", "notify_expired", "refund_email"} {
		if h, ok := v3.Hook(id); !ok || h.Action != "email.notify" || len(h.Writes) != 0 {
			t.Errorf("hook %s = %+v, %v", id, h, ok)
		}
	}
}
