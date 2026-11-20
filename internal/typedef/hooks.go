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
func (t *Type) checkHooks() error {
	seen := map[string]bool{}
	checked := map[string]string{} // "from -> to" -> the check hook on it
	for _, h := range t.Hooks {
		// Every error names the hook and where it attaches.
		where := fmt.Sprintf("hook %q", h.ID)
		switch h.On {
		case OnEnter:
			where += fmt.Sprintf(" (on enter %s)", h.State)
		case OnTransition:
			where += fmt.Sprintf(" (on %s -> %s)", h.From, h.To)
		case OnReaction:
			where += fmt.Sprintf(" (on reaction %s)", h.Reaction)
		}
		fail := func(format string, args ...any) error {
			return fmt.Errorf("typedef %s v%d: %s: %s", t.ID, t.Version, where, fmt.Sprintf(format, args...))
		}

		if _, _, ok := item.HookParts(item.KindHook + ":" + t.ID + ":" + h.ID); !ok {
			return fail("not a valid hook ID: it must be non-empty, without ':' or '@'")
		}
		if seen[h.ID] {
			return fail("duplicate hook ID")
		}
		seen[h.ID] = true

		switch h.On {
		case OnEnter:
			if _, ok := t.State(h.State); !ok {
				return fail("unknown state %q", h.State)
			}
			if h.Kind != HookEnrich && h.Kind != HookAction {
				return fail("a hook on entering a state must be kind enrich or action, not %q: a check belongs on a transition", h.Kind)
			}
		case OnTransition:
			tr, ok := t.Transition(h.From, h.To)
			if !ok {
				return fail("there is no transition %s -> %s", h.From, h.To)
			}
			if h.Kind != HookCheck {
				return fail("a hook on a transition must be kind check, not %q: enrich and action belong on entering a state", h.Kind)
			}
			if tr.Escape {
				return fail("transition %s -> %s is an escape: it cancels a hold, so it may not open one", h.From, h.To)
			}
			name := h.From + " -> " + h.To
			if other, dup := checked[name]; dup {
				return fail("transition %s already has the check %q", name, other)
			}
			checked[name] = h.ID
			// The correction travels with the move out of the from-state:
			// it may not write what that state freezes (else every
			// resolution would be refused FIELD_FROZEN).
			if from, ok := t.State(h.From); ok {
				for _, id := range h.Writes {
					if slices.Contains(from.Frozen, id) {
						return fail("it writes %s, which state %s freezes: its correction leaves %s with the move",
							id, h.From, h.From)
					}
				}
			}
			// A check's correction is the transition's patch, applied by
			// ResolveHold: like any patch of the transition, it may not
			// write a field the transition's `allowed` names.
			for _, id := range h.Writes {
				if slices.Contains(tr.Allowed, AllowedFieldPrefix+id) {
					return fail("it writes %s, which the transition's allowed names (field:%s): "+
						"a transition's patch may not choose who may make it", id, id)
				}
			}
			// The hold applies the requester's transition: `allowed` is
			// checked against the requester. A hook named there could
			// approve on its own authority.
			for _, entry := range tr.Allowed {
				if who, ok := strings.CutPrefix(entry, AllowedPrincipalPrefix); ok && item.IsKind(who, item.KindHook) {
					return fail("the checked transition's allowed entry %q names a hook; "+
						"a hook never makes a checked move on its own authority", entry)
				}
			}
		case OnTimer:
			// Which state's timer runs it is the acting timer's to say
			// (checkActing): each such hook is named by exactly one.
			if h.State != "" || h.From != "" || h.To != "" {
				return fail("a hook on: timer names no state or transition: the acting timer that runs it does")
			}
			if h.Kind != HookAction {
				return fail("a hook on: timer must be kind action, not %q", h.Kind)
			}
		case OnReaction:
			if h.State != "" || h.From != "" || h.To != "" {
				return fail("a hook on: reaction names no state or transition: its reaction does")
			}
			if h.Reaction == "" {
				return fail("a hook on: reaction needs `reaction`, the undo whose applied event runs it")
			}
			r, ok := t.Reaction(h.Reaction)
			if !ok {
				return fail("unknown reaction %q", h.Reaction)
			}
			if r.Kind != ReactionAfter || r.Undoes == "" {
				return fail("reaction %q is not an undo (an after reaction): only an undo records that it was applied", h.Reaction)
			}
			if h.Kind != HookAction {
				return fail("a hook on: reaction must be kind action, not %q", h.Kind)
			}
		default:
			return fail("unknown attach point %q: want enter, transition, timer or reaction", h.On)
		}
		if h.On != OnReaction && h.Reaction != "" {
			return fail("only a hook on: reaction names a reaction")
		}

		if h.Action == "" {
			return fail("no action")
		}
		// A hook reads fields or the creator attributes; it writes fields only.
		for _, id := range h.Reads {
			if _, ok := t.Field(id); !ok && !IsAttribute(id) {
				return fail("field %q is not declared", id)
			}
		}
		for _, id := range h.Writes {
			if IsAttribute(id) {
				return fail("%q is set by the kernel at create: no hook writes it", id)
			}
			if _, ok := t.Field(id); !ok {
				return fail("field %q is not declared", id)
			}
		}
		if h.Kind == HookCheck && len(h.Reads) == 0 {
			return fail("a check reads at least one field: its reads are what the hold freezes")
		}
		if h.Kind == HookEnrich && len(h.Writes) == 0 {
			return fail("an enrich hook writes at least one field")
		}
		if _, writes, ok := hooks.Arity(h.Action); h.Kind == HookAction && len(h.Writes) == 0 && !(ok && writes == 0) {
			return fail("an action hook writes at least one field: what it made records that it ran")
		}
		if len(h.Fallback) > 0 {
			if h.Kind != HookEnrich {
				return fail("only an enrich hook has a fallback: there is no default for a side effect")
			}
			for id := range h.Fallback {
				if !slices.Contains(h.Writes, id) {
					return fail("fallback for %q, a field the hook does not write", id)
				}
			}
			if err := t.CheckFields(h.Fallback, false); err != nil {
				return fail("fallback: %v", err)
			}
		}

		reads, writes, ok := hooks.Arity(h.Action)
		if !ok {
			return fail("unknown action %q", h.Action)
		}
		if reads != len(h.Reads) || writes != len(h.Writes) {
			return fail("action %s takes %d reads and %d writes; the hook declares %d and %d",
				h.Action, reads, writes, len(h.Reads), len(h.Writes))
		}
	}
	return t.checkActing()
}

// checkActing validates the states' acting timers against the hooks: each
// names an action hook on: timer, and each such hook is named by exactly one
// acting timer. It fills the parsed durations.
func (t *Type) checkActing() error {
	named := map[string]string{} // hook -> "state/timer" naming it
	for i := range t.States {
		s := &t.States[i]
		ids := map[string]bool{}
		for j := range s.Acting {
			a := &s.Acting[j]
			fail := func(format string, args ...any) error {
				return fmt.Errorf("typedef %s v%d: state %s: acting timer %q: %s",
					t.ID, t.Version, s.ID, a.ID, fmt.Sprintf(format, args...))
			}
			// The ID is part of the runs' idempotency keys (':' separated).
			if a.ID == "" || strings.ContainsAny(a.ID, ":@") {
				return fail("not a valid acting timer ID: it must be non-empty, without ':' or '@'")
			}
			if ids[a.ID] {
				return fail("duplicate acting timer ID in the state")
			}
			ids[a.ID] = true
			if a.Max < 1 {
				return fail("needs `max`, the total number of runs, at least 1")
			}
			if a.After == "" {
				return fail("needs `after`, such as 48h")
			}
			d, err := time.ParseDuration(a.After)
			if err != nil {
				return fail("`after`: %v", err)
			}
			if d <= 0 {
				return fail("`after` must be more than zero")
			}
			a.AfterDuration = d
			a.EveryDuration = 0
			if a.Max > 1 && a.Every == "" {
				return fail("runs %d times, so it needs `every`, the interval between runs", a.Max)
			}
			if a.Every != "" {
				e, err := time.ParseDuration(a.Every)
				if err != nil {
					return fail("`every`: %v", err)
				}
				if e <= 0 {
					return fail("`every` must be more than zero")
				}
				a.EveryDuration = e
			}
			h, ok := t.Hook(a.Hook)
			if !ok {
				return fail("unknown hook %q", a.Hook)
			}
			if h.On != OnTimer {
				return fail("hook %q is not on: timer", a.Hook)
			}
			here := s.ID + "/" + a.ID
			if other, dup := named[a.Hook]; dup {
				return fail("hook %q is already run by acting timer %s: each timer hook belongs to one timer", a.Hook, other)
			}
			named[a.Hook] = here
		}
	}
	for _, h := range t.Hooks {
		if h.On == OnTimer && named[h.ID] == "" {
			return fmt.Errorf("typedef %s v%d: hook %q (on: timer): no acting timer runs it", t.ID, t.Version, h.ID)
		}
	}
	return nil
}
