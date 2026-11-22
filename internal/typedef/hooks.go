package typedef

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
)

// HookKind says what a hook does.
type HookKind string

const (
	// HookEnrich runs on entering a state: it reads some fields, calls out,
	// and writes others. It never holds the Item.
	HookEnrich HookKind = "enrich"
	// HookCheck runs on a transition: asking for the transition opens a
	// hold, and the check allows, denies, corrects or errors.
	HookCheck HookKind = "check"
	// HookAction runs on entering a state, like enrich, but changes the
	// outside world (an invoice in the ERP). It gets an idempotency key,
	// has no fallback, writes back what it made, and once its state is
	// entered it runs to completion, even if the Item has moved on.
	HookAction HookKind = "action"
)

// HookOn says where a hook attaches.
type HookOn string

const (
	OnEnter      HookOn = "enter"      // entering State
	OnTransition HookOn = "transition" // the transition From -> To
	// OnTimer: run by an acting timer of a state (Step 2, part 3); the
	// timer names the hook. Action hooks only.
	OnTimer HookOn = "timer"
	// OnReaction: run when the Item records that its reaction Reaction (an
	// undo) committed (the reaction_applied event). Action hooks only.
	OnReaction HookOn = "reaction"
	// OnCreatedInside: a reaction (never a hook) triggered by the creation
	// of an Item inside an Item of the declaring Type (Jira part 2, step 2).
	OnCreatedInside HookOn = "created_inside"
)

// Hook binds an action (code, named here, never contained) to a point in
// the lifecycle. Its principal is hook:<type>:<id>.
type Hook struct {
	ID    string `yaml:"id" json:"id"`
	On    HookOn `yaml:"on" json:"on"`
	State string `yaml:"state" json:"state,omitempty"` // on: enter
	From  string `yaml:"from" json:"from,omitempty"`   // on: transition
	To    string `yaml:"to" json:"to,omitempty"`       // on: transition

	Kind   HookKind `yaml:"kind" json:"kind"`
	Action string   `yaml:"action" json:"action"`
	// Reads are the fields whose values the action gets, in order; a
	// check's reads are frozen while its hold is open. Writes are the fields
	// the action's values go to, in order, and the only fields the hook may
	// write.
	Reads  []string `yaml:"reads" json:"reads,omitempty"`
	Writes []string `yaml:"writes" json:"writes,omitempty"`
	// Fallback is what an enrich hook writes when its budget runs out.
	Fallback map[string]any `yaml:"fallback" json:"fallback,omitempty"`
	// Reaction (on: reaction): the after reaction whose applied event runs
	// the hook.
	Reaction string `yaml:"reaction" json:"reaction,omitempty"`
}

// Hook returns the hook with the given ID.
func (t *Type) Hook(id string) (Hook, bool) {
	for _, h := range t.Hooks {
		if h.ID == id {
			return h, true
		}
	}
	return Hook{}, false
}

// checkHooks validates the hooks against the states, transitions and fields
// already checked, and against the actions the hooks package registers.
func (t *Type) checkHooks(r reporter) {
	seen := map[string]bool{}
	checked := map[string]string{} // "from -> to" -> the check hook on it
	for i := range t.Hooks {
		t.checkHook(r, i, seen, checked)
	}
	t.checkActing(r)
}

func (t *Type) checkHook(r reporter, i int, seen map[string]bool, checked map[string]string) {
	h, p := t.Hooks[i], at("hooks", i)
	// Every message names the hook and where it attaches.
	where := fmt.Sprintf("hook %q", h.ID)
	switch h.On {
	case OnEnter:
		where += fmt.Sprintf(" (on enter %s)", h.State)
	case OnTransition:
		where += fmt.Sprintf(" (on %s -> %s)", h.From, h.To)
	case OnReaction:
		where += fmt.Sprintf(" (on reaction %s)", h.Reaction)
	}
	r = r.within("%s: ", where)

	if _, _, ok := item.HookParts(item.KindHook + ":" + t.ID + ":" + h.ID); !ok {
		r.fail(CodeInvalidName, at(p, "id"), "Give the hook an id without ':' or '@', such as check_credit",
			"not a valid hook ID: it must be non-empty, without ':' or '@'")
		return
	}
	if seen[h.ID] {
		r.fail(CodeDuplicate, at(p, "id"), "Rename one of the two hooks", "duplicate hook ID")
		return
	}
	seen[h.ID] = true

	switch h.On {
	case OnEnter:
		if _, ok := t.State(h.State); !ok {
			r.fail(CodeUnknownState, at(p, "state"), "Name a declared state", "unknown state %q", h.State)
		}
		if h.Kind != HookEnrich && h.Kind != HookAction {
			r.fail(CodeInvalidValue, at(p, "kind"), "Use kind: enrich or action here, or put the check on a transition",
				"a hook on entering a state must be kind enrich or action, not %q: a check belongs on a transition", h.Kind)
		}
	case OnTransition:
		t.checkCheckHook(r, p, h, checked)
	case OnTimer:
		// Which state's timer runs it is the acting timer's to say
		// (checkActing): each such hook is named by exactly one.
		if k := namedKey(h.State, h.From, h.To); k != "" {
			r.fail(CodeKeyNotApplicable, at(p, k), "Remove it: the acting timer that runs the hook names its state",
				"a hook on: timer names no state or transition: the acting timer that runs it does")
		}
		if h.Kind != HookAction {
			r.fail(CodeInvalidValue, at(p, "kind"), "Use kind: action", "a hook on: timer must be kind action, not %q", h.Kind)
		}
	case OnReaction:
		if k := namedKey(h.State, h.From, h.To); k != "" {
			r.fail(CodeKeyNotApplicable, at(p, k), "Remove it: the hook's reaction says when it runs",
				"a hook on: reaction names no state or transition: its reaction does")
		}
		switch rx, ok := t.Reaction(h.Reaction); {
		case h.Reaction == "":
			r.fail(CodeRequired, at(p, "reaction"), "Name the undo (an after reaction) whose applied event runs the hook",
				"a hook on: reaction needs `reaction`, the undo whose applied event runs it")
		case !ok:
			r.fail(CodeUnknownReaction, at(p, "reaction"), "Name a reaction this Type declares",
				"unknown reaction %q", h.Reaction)
		case rx.Kind != ReactionAfter || rx.Undoes == "":
			r.fail(CodeInvalidValue, at(p, "reaction"), "Name an after reaction that undoes a held one",
				"reaction %q is not an undo (an after reaction): only an undo records that it was applied", h.Reaction)
		}
		if h.Kind != HookAction {
			r.fail(CodeInvalidValue, at(p, "kind"), "Use kind: action", "a hook on: reaction must be kind action, not %q", h.Kind)
		}
	default:
		r.fail(CodeInvalidValue, at(p, "on"), "Use on: enter, transition, timer or reaction",
			"unknown attach point %q: want enter, transition, timer or reaction", h.On)
		return
	}
	if h.On != OnReaction && h.Reaction != "" {
		r.fail(CodeKeyNotApplicable, at(p, "reaction"), "Remove reaction, or attach the hook on: reaction",
			"only a hook on: reaction names a reaction")
	}

	// A hook reads fields or the creator attributes; it writes fields only.
	for k, id := range h.Reads {
		// container:<offer>: read from the Item's container when the hook
		// runs (jira23b.go).
		if _, offer := OfferOf(id); offer {
			checkOfferRead(r, at(p, "reads", k), h, id)
			continue
		}
		if _, ok := t.Field(id); !ok && !IsAttribute(id) {
			r.fail(CodeUnknownField, at(p, "reads", k), "Declare the field, or read one that is declared", "field %q is not declared", id)
		}
	}
	for k, id := range h.Writes {
		switch _, offer := OfferOf(id); {
		case offer:
			r.fail(CodeInvalidValue, at(p, "writes", k), "Write a field of this Type", "%q is the container's to offer: no hook writes it", id)
		case IsAttribute(id):
			r.fail(CodeInvalidValue, at(p, "writes", k), "Write a field of this Type", "%q is set by the kernel at create: no hook writes it", id)
		default:
			if _, ok := t.Field(id); !ok {
				r.fail(CodeUnknownField, at(p, "writes", k), "Declare the field, or write one that is declared", "field %q is not declared", id)
			}
		}
	}
	if h.Kind == HookCheck && len(h.Reads) == 0 {
		r.fail(CodeRequired, at(p, "reads"), "Name the fields the check reads",
			"a check reads at least one field: its reads are what the hold freezes")
	}
	if h.Kind == HookEnrich && len(h.Writes) == 0 {
		r.fail(CodeRequired, at(p, "writes"), "Name the fields the hook writes", "an enrich hook writes at least one field")
	}
	// Only a known action says whether it may write nothing.
	if _, writes, ok := hooks.Arity(h.Action); ok && h.Kind == HookAction && len(h.Writes) == 0 && writes != 0 {
		r.fail(CodeRequired, at(p, "writes"), "Name the field that records what the action made",
			"an action hook writes at least one field: what it made records that it ran")
	}
	t.checkFallback(r, p, h)

	if h.Action == "" {
		r.fail(CodeRequired, at(p, "action"), "Name the action the hook runs", "no action")
		return
	}
	reads, writes, ok := hooks.Arity(h.Action)
	switch {
	case !ok:
		r.fail(CodeUnknownAction, at(p, "action"), "Name an action the kernel registers (internal/hooks)", "unknown action %q", h.Action)
	case reads != len(h.Reads):
		r.fail(CodeActionArity, at(p, "reads"), "Give the action as many reads as it takes",
			"action %s takes %d reads and %d writes; the hook declares %d and %d", h.Action, reads, writes, len(h.Reads), len(h.Writes))
	case writes != len(h.Writes):
		r.fail(CodeActionArity, at(p, "writes"), "Give the action as many writes as it takes",
			"action %s takes %d reads and %d writes; the hook declares %d and %d", h.Action, reads, writes, len(h.Reads), len(h.Writes))
	}
}

// namedKey returns the first of state, from and to that is set, as its key.
func namedKey(state, from, to string) string {
	switch {
	case state != "":
		return "state"
	case from != "":
		return "from"
	case to != "":
		return "to"
	}
	return ""
}

// checkCheckHook checks a hook on: transition, a check.
func (t *Type) checkCheckHook(r reporter, p string, h Hook, checked map[string]string) {
	j := t.transitionIndex(h.From, h.To)
	if j < 0 {
		r.fail(CodeUnknownTransition, at(p, "to"), "Declare the transition, or attach the check to one that is declared",
			"there is no transition %s -> %s", h.From, h.To)
		return
	}
	tr := t.Transitions[j]
	if h.Kind != HookCheck {
		r.fail(CodeInvalidValue, at(p, "kind"), "Use kind: check here, or attach the hook on entering a state",
			"a hook on a transition must be kind check, not %q: enrich and action belong on entering a state", h.Kind)
	}
	if tr.Escape {
		r.fail(CodeHeldTransition, p, "Check another transition: an escape may not be held",
			"transition %s -> %s is an escape: it cancels a hold, so it may not open one", h.From, h.To)
	}
	name := h.From + " -> " + h.To
	if other, dup := checked[name]; dup {
		r.fail(CodeOnlyOneAllowed, p, "Merge the two checks into one, or remove one",
			"transition %s already has the check %q", name, other)
		return
	}
	checked[name] = h.ID
	// The correction travels with the move out of the from-state: it may
	// not write what that state freezes (else every resolution would be
	// refused FIELD_FROZEN).
	if from, ok := t.State(h.From); ok {
		for k, id := range h.Writes {
			if slices.Contains(from.Frozen, id) {
				r.fail(CodeWritesFrozenField, at(p, "writes", k), "Write another field, or stop freezing it in the from-state",
					"it writes %s, which state %s freezes: its correction leaves %s with the move", id, h.From, h.From)
			}
		}
	}
	// A check's correction is the transition's patch, applied by
	// ResolveHold: like any patch of the transition, it may not write a
	// field the transition's `allowed` names.
	for k, id := range h.Writes {
		if slices.Contains(tr.Allowed, AllowedFieldPrefix+id) {
			r.fail(CodeConflict, at(p, "writes", k), "Write another field: the check may not choose who may make the move",
				"it writes %s, which the transition's allowed names (field:%s): "+
					"a transition's patch may not choose who may make it", id, id)
		}
	}
	// The hold applies the requester's transition: `allowed` is checked
	// against the requester. A hook named there could approve on its own
	// authority.
	for k, entry := range tr.Allowed {
		if who, ok := strings.CutPrefix(entry, AllowedPrincipalPrefix); ok && item.IsKind(who, item.KindHook) {
			r.fail(CodeConflict, at("transitions", j, "allowed", k), "Remove the hook from allowed: a check decides through its hold",
				"the checked transition's allowed entry %q names a hook; "+
					"a hook never makes a checked move on its own authority", entry)
		}
	}
}

// checkFallback checks an enrich hook's fallback values.
func (t *Type) checkFallback(r reporter, p string, h Hook) {
	if len(h.Fallback) == 0 {
		return
	}
	if h.Kind != HookEnrich {
		r.fail(CodeKeyNotApplicable, at(p, "fallback"), "Remove the fallback: only an enrich hook has one",
			"only an enrich hook has a fallback: there is no default for a side effect")
		return
	}
	ids := make([]string, 0, len(h.Fallback))
	for id := range h.Fallback {
		ids = append(ids, id)
	}
	slices.Sort(ids) // Go randomizes map order; problems come in a stable one
	bad := false
	for _, id := range ids {
		if !slices.Contains(h.Writes, id) {
			r.fail(CodeKeyNotApplicable, at(p, "fallback", id), "Give a fallback only for a field the hook writes",
				"fallback for %q, a field the hook does not write", id)
			bad = true
		}
	}
	if bad {
		return
	}
	if err := t.CheckFields(h.Fallback, false); err != nil {
		r.fail(CodeInvalidValue, at(p, "fallback"), "Give each fallback a value of its field's type", "fallback: %v", err)
	}
}

// checkActing validates the states' acting timers against the hooks: each
// names an action hook on: timer, and each such hook is named by exactly one
// acting timer. It fills the parsed durations.
func (t *Type) checkActing(r reporter) {
	named := map[string]string{} // hook -> "state/timer" naming it
	for i := range t.States {
		ids := map[string]bool{}
		for j := range t.States[i].Acting {
			t.checkActingTimer(r, i, j, ids, named)
		}
	}
	for k, h := range t.Hooks {
		if h.On == OnTimer && named[h.ID] == "" {
			r.fail(CodeMissingCounterpart, at("hooks", k), "Run it from a state's acting timer, or remove it",
				"hook %q (on: timer): no acting timer runs it", h.ID)
		}
	}
}

func (t *Type) checkActingTimer(r reporter, i, j int, ids map[string]bool, named map[string]string) {
	s := &t.States[i]
	a, p := &s.Acting[j], at("states", i, "acting", j)
	r = r.within("state %s: acting timer %q: ", s.ID, a.ID)
	// The ID is part of the runs' idempotency keys (':' separated).
	if a.ID == "" || strings.ContainsAny(a.ID, ":@") {
		r.fail(CodeInvalidName, at(p, "id"), "Give the timer an id without ':' or '@', such as remind",
			"not a valid acting timer ID: it must be non-empty, without ':' or '@'")
		return
	}
	if ids[a.ID] {
		r.fail(CodeDuplicate, at(p, "id"), "Rename one of the two timers", "duplicate acting timer ID in the state")
		return
	}
	ids[a.ID] = true
	if a.At != "" {
		// At an instant: no schedule; sets, a hook, or both.
		t.checkActingAt(s, a, r.legacy())
		if a.Hook == "" {
			return
		}
	} else {
		switch {
		case a.When != "":
			r.fail(CodeKeyNotApplicable, at(p, "when"), "Remove when, or act at an instant (at)", "when and sets are for a timer at an instant (at)")
		case len(a.Sets) > 0:
			r.fail(CodeKeyNotApplicable, at(p, "sets"), "Remove sets, or act at an instant (at)", "when and sets are for a timer at an instant (at)")
		case a.Max < 1:
			r.fail(CodeRequired, at(p, "max"), "Give max, the number of runs, 1 or more", "needs `max`, the total number of runs, at least 1")
		}
		switch d, err := time.ParseDuration(a.After); {
		case a.After == "":
			r.fail(CodeRequired, at(p, "after"), "Give after as a duration, such as 48h", "needs `after`, such as 48h")
		case err != nil:
			r.fail(CodeInvalidValue, at(p, "after"), "Write a duration such as 90s, 4h or 48h", "`after`: %v", err)
		case d <= 0:
			r.fail(CodeInvalidValue, at(p, "after"), "Give a duration of more than zero", "`after` must be more than zero")
		default:
			a.AfterDuration = d
		}
		a.EveryDuration = 0
		if a.Max > 1 && a.Every == "" {
			r.fail(CodeRequired, at(p, "every"), "Give every, the interval between runs, such as 24h",
				"runs %d times, so it needs `every`, the interval between runs", a.Max)
		}
		if a.Every != "" {
			switch e, err := time.ParseDuration(a.Every); {
			case err != nil:
				r.fail(CodeInvalidValue, at(p, "every"), "Write a duration such as 90s, 4h or 48h", "`every`: %v", err)
			case e <= 0:
				r.fail(CodeInvalidValue, at(p, "every"), "Give a duration of more than zero", "`every` must be more than zero")
			default:
				a.EveryDuration = e
			}
		}
	}
	h, ok := t.Hook(a.Hook)
	if !ok {
		r.fail(CodeUnknownHook, at(p, "hook"), "Name a hook of this Type with on: timer", "unknown hook %q", a.Hook)
		return
	}
	if h.On != OnTimer {
		r.fail(CodeInvalidValue, at(p, "hook"), "Name a hook with on: timer", "hook %q is not on: timer", a.Hook)
		return
	}
	if other, dup := named[a.Hook]; dup {
		r.fail(CodeOnlyOneAllowed, at(p, "hook"), "Give each timer its own hook",
			"hook %q is already run by acting timer %s: each timer hook belongs to one timer", a.Hook, other)
		return
	}
	named[a.Hook] = s.ID + "/" + a.ID
}
