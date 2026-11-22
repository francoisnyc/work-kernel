package typedef

import (
	"maps"
	"regexp"
	"slices"
	"time"

	"cel.dev/cel-go/cel"
)

// Jira part 2, step 1 (docs/design.md, "Decisions for Jira part 2"):
// date-time values, a state's timer at a field's instant, and a
// transition's computed field effects.

// rfc3339 is the shape of a datetime as it may be sent: a full date and
// time, at most six fractional digits, and an offset. Go's own parser is
// more lenient (it takes any number of fractional digits), so the shape is
// checked first.
var rfc3339 = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,6})?(Z|[+-]\d{2}:\d{2})$`)

// parseInstant reads a datetime value as sent or as stored.
func parseInstant(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || !rfc3339.MatchString(s) {
		return time.Time{}, false
	}
	// time.Parse refuses a day that does not exist and a leap second.
	at, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, false
	}
	at = at.UTC()
	// The stored form has a four-digit year.
	if at.Year() < 1 || at.Year() > 9999 {
		return time.Time{}, false
	}
	return at, true
}

// NormalizeDateTime reads a datetime value as sent (an RFC 3339 string with
// an offset, at most microseconds) and returns its stored form
// (DateTimeLayout). The second value is false for anything else.
func NormalizeDateTime(v any) (string, bool) {
	at, ok := parseInstant(v)
	if !ok {
		return "", false
	}
	return at.Format(DateTimeLayout), true
}

// FormatDateTime is an instant in the stored form, cut to the microsecond.
// The second value is false for a year the form cannot hold.
func FormatDateTime(at time.Time) (string, bool) {
	at = at.UTC()
	if at.Year() < 1 || at.Year() > 9999 {
		return "", false
	}
	return at.Format(DateTimeLayout), true
}

// ParseDateTime reads a datetime value as a time.
func ParseDateTime(v any) (time.Time, bool) { return parseInstant(v) }

// NormalizeFields returns values with every datetime value in its stored
// form, and every list of a multi-value field as its set. The values must have passed CheckFields. The map is copied only
// when something changes: a map argument is shared with the caller, as a
// Java Map would be.
func (t *Type) NormalizeFields(values map[string]any) map[string]any {
	out, copied := values, false
	set := func(id string, v any) {
		if !copied {
			out, copied = maps.Clone(values), true
		}
		out[id] = v
	}
	for id, v := range values {
		f, ok := t.Field(id)
		if !ok || v == nil {
			continue
		}
		switch {
		case f.Multiple:
			// A list becomes the set: sorted, each member once. A set
			// operation stays as sent: the item service applies it.
			if _, isOp := v.(map[string]any); !isOp {
				set(id, StoredSet(SetMembers(v)))
			}
		case f.Type == DateTime:
			if stored, ok := NormalizeDateTime(v); ok && stored != v {
				set(id, stored)
			}
		}
	}
	return out
}

// AtFields returns the fields some state names in `at`, sorted.
func (t *Type) AtFields() []string {
	var out []string
	for _, s := range t.States {
		if s.At != nil && !slices.Contains(out, s.At.Field) {
			out = append(out, s.At.Field)
		}
	}
	slices.Sort(out)
	return out
}

// checkAtTimers validates each state's timer at a field's instant, with a
// duration timer's rules: it names a datetime field and a transition the
// Type declares from that state, whose `allowed` list, if any, includes the
// timer. A state has `timer` or `at`, not both.
func (t *Type) checkAtTimers(fail func(string, ...any) error) error {
	for _, s := range t.States {
		if s.At == nil {
			continue
		}
		if s.Timer != nil {
			return fail("state %s: it has both `timer` and `at`; a state has one timer", s.ID)
		}
		if s.At.Field == "" {
			return fail("state %s: at needs `field`, a datetime field", s.ID)
		}
		if s.At.To == "" {
			return fail("state %s: at needs `to`, the state to move to", s.ID)
		}
		f, ok := t.Field(s.At.Field)
		if !ok {
			return fail("state %s: at names field %q, which is not declared", s.ID, s.At.Field)
		}
		// A date has no time zone: which instant ends that day is not
		// decided. Datetime fields only.
		if f.Type != DateTime || f.Multiple {
			return fail("state %s: at names field %q, a %s; it must be a datetime", s.ID, f.ID, f.Type)
		}
		tr, ok := t.Transition(s.ID, s.At.To)
		if !ok {
			return fail("state %s: at moves to %q, but there is no transition %s -> %s", s.ID, s.At.To, s.ID, s.At.To)
		}
		timer := AllowedPrincipalPrefix + TimerPrincipal
		if len(tr.Allowed) > 0 && !slices.Contains(tr.Allowed, timer) {
			return fail("state %s: its timer uses transition %s -> %s, whose `allowed` list must include %s",
				s.ID, s.ID, s.At.To, timer)
		}
	}
	return nil
}

// newSetsEnv declares what a `sets` expression may read: every field,
// cause.at (the time of the event being written) and today. No links, and
// nobody's identity.
func newSetsEnv(t *Type) (*cel.Env, error) {
	opts := calendarFunctions()
	opts = append(opts, cel.Variable(VarToday, cel.TimestampType), cel.Variable(VarCauseAt, cel.TimestampType))
	for _, f := range t.celFields() {
		opts = append(opts, cel.Variable(f.ID, fieldCELType(f)))
	}
	return cel.NewEnv(opts...)
}

// checkSets validates and compiles each transition's computed effects. An
// effect that could never run, or that contradicts its own transition, is
// refused at load:
//   - the field is required or cleared by the same transition;
//   - the state the move leaves freezes it;
//   - a principal field, or a multi-value field, is set other than by a
//     plain reference (container.<field>, or a field of its shape): who
//     acted is identity, not data, and is never computed (Jira 2.4b);
//   - the transition's own `allowed` names it (field:<id>), unless it is
//     set by plain reference: a hand-over (Jira 2.4b, kernel change 11;
//     `allowed` is judged on the value before the write);
//   - the expression does not compile, or does not yield a value of the
//     field's type (or null, which unsets);
//   - it reads the container other than as container.<field> (its state
//     and category are a guard's);
//   - the transition is held by a check or a held reaction (as for clears).
//
// A field only the kernel or a role writes may be set: the Type writes it,
// not the caller.
func (t *Type) checkSets(fail func(string, ...any) error) error {
	var env *cel.Env
	for i := range t.Transitions {
		tr := &t.Transitions[i]
		tr.SetsContainer = nil
		if len(tr.Sets) == 0 {
			continue
		}
		if env == nil {
			var err error
			if env, err = newTransitionSetsEnv(t); err != nil {
				return fail("sets environment: %v", err)
			}
		}
		name := tr.From + " -> " + tr.To
		from, _ := t.State(tr.From)
		tr.SetPrograms = map[string]cel.Program{}
		// Sorted, so the same Type always fails with the same message.
		for _, id := range slices.Sorted(maps.Keys(tr.Sets)) {
			f, ok := t.Field(id)
			switch {
			case !ok:
				return fail("transition %s: sets %q, which is not a declared field", name, id)
			case slices.Contains(tr.Required, id):
				return fail("transition %s: requires and sets %q", name, id)
			case slices.Contains(tr.Clears, id):
				return fail("transition %s: clears and sets %q; to unset it, set it to null", name, id)
			case slices.Contains(from.Frozen, id):
				return fail("transition %s: sets %q, which state %s freezes", name, id, tr.From)
			}
			ref, err := checkCopy(f, tr.Sets[id], t)
			if err != nil {
				return fail("transition %s: sets %v", name, err)
			}
			if ref.Each {
				return fail("transition %s: sets %s from each, which only a create reaction with for_each reads", name, id)
			}
			_, plain := plainRef(tr.Sets[id])
			if slices.Contains(tr.Allowed, AllowedFieldPrefix+id) && !plain {
				return fail("transition %s: sets %q, which its own allowed list names; only a plain reference may hand it over", name, id)
			}
			rest, reads, err := takeContainerFields(tr.Sets[id])
			if err != nil {
				return fail("transition %s: sets %s: %v", name, id, err)
			}
			if linkedWord.MatchString(rest) {
				return fail("transition %s: sets %s: %q: an effect reads the container as container.<field>; linked Items are a create reaction's", name, id, tr.Sets[id])
			}
			for _, r := range reads {
				if !slices.Contains(tr.SetsContainer, r) {
					tr.SetsContainer = append(tr.SetsContainer, r)
				}
			}
			ast, iss := env.Compile(tr.Sets[id])
			if iss.Err() != nil {
				return fail("transition %s: sets %s: %q: %v", name, id, tr.Sets[id], iss.Err())
			}
			if out := ast.OutputType(); !out.IsExactType(cel.DynType) && !fieldCELType(f).IsAssignableType(out) {
				return fail("transition %s: sets %s: %q yields %s, not a value of a %s field", name, id, tr.Sets[id], out, f.Type)
			}
			prg, err := env.Program(ast)
			if err != nil {
				return fail("transition %s: sets %s: %v", name, id, err)
			}
			tr.SetPrograms[id] = prg
		}
		slices.Sort(tr.SetsContainer)
		for _, h := range t.Hooks {
			if h.Kind == HookCheck && h.From == tr.From && h.To == tr.To {
				return fail("transition %s: it sets fields and is held by the check %s; a held transition may not set fields",
					name, h.ID)
			}
		}
		for _, r := range t.Reactions {
			if r.Kind == ReactionHeld && r.State == tr.To && (len(r.From) == 0 || slices.Contains(r.From, tr.From)) {
				return fail("transition %s: it sets fields and is held by the reaction %s; a held transition may not set fields",
					name, r.ID)
			}
		}
	}
	return nil
}
