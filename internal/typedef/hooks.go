package typedef

import (
	"fmt"
	"slices"
	"strings"

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
			name := h.From + " -> " + h.To
			if other, dup := checked[name]; dup {
				return fail("transition %s already has the check %q", name, other)
			}
			checked[name] = h.ID
			// The hold applies the requester's transition: `allowed` is
			// checked against the requester. A hook named there could
			// approve on its own authority.
			for _, entry := range tr.Allowed {
				if who, ok := strings.CutPrefix(entry, AllowedPrincipalPrefix); ok && item.IsKind(who, item.KindHook) {
					return fail("the checked transition's allowed entry %q names a hook; "+
						"a hook never makes a checked move on its own authority", entry)
				}
			}
		default:
			return fail("unknown attach point %q: want enter or transition", h.On)
		}

		if h.Action == "" {
			return fail("no action")
		}
		for _, list := range [][]string{h.Reads, h.Writes} {
			for _, id := range list {
				if _, ok := t.Field(id); !ok {
					return fail("field %q is not declared", id)
				}
			}
		}
		if h.Kind == HookCheck && len(h.Reads) == 0 {
			return fail("a check reads at least one field: its reads are what the hold freezes")
		}
		if h.Kind == HookEnrich && len(h.Writes) == 0 {
			return fail("an enrich hook writes at least one field")
		}
		if h.Kind == HookAction && len(h.Writes) == 0 {
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
	return nil
}
