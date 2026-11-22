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
func (t *Type) checkAtTimers(r reporter) {
	for i, s := range t.States {
		if s.At == nil {
			continue
		}
		p := at("states", i, "at")
		if s.Timer != nil {
			r.fail(CodeConflict, p, "Keep timer or at, not both", "state %s: it has both `timer` and `at`; a state has one timer", s.ID)
			continue
		}
		if s.At.Field == "" {
			r.fail(CodeRequired, at(p, "field"), "Name the datetime field whose instant the timer waits for",
				"state %s: at needs `field`, a datetime field", s.ID)
		} else if f, ok := t.Field(s.At.Field); !ok {
			r.fail(CodeUnknownField, at(p, "field"), "Declare the field, or name a declared datetime field",
				"state %s: at names field %q, which is not declared", s.ID, s.At.Field)
		} else if f.Type != DateTime || f.Multiple {
			// A date has no time zone: which instant ends that day is not
			// decided. Datetime fields only.
			r.fail(CodeWrongFieldType, at(p, "field"), "Name a datetime field holding one instant",
				"state %s: at names field %q, a %s; it must be a datetime", s.ID, f.ID, f.Type)
		}
		if s.At.To == "" {
			r.fail(CodeRequired, at(p, "to"), "Name the state the timer moves to", "state %s: at needs `to`, the state to move to", s.ID)
			continue
		}
		j := t.transitionIndex(s.ID, s.At.To)
		if j < 0 {
			r.fail(CodeUnknownTransition, at(p, "to"), "Declare the transition from this state, or move to another state",
				"state %s: at moves to %q, but there is no transition %s -> %s", s.ID, s.At.To, s.ID, s.At.To)
			continue
		}
		timer := AllowedPrincipalPrefix + TimerPrincipal
		if tr := t.Transitions[j]; len(tr.Allowed) > 0 && !slices.Contains(tr.Allowed, timer) {
			r.fail(CodeTimerNotAllowed, at("transitions", j, "allowed"), "Add principal:system:timer to the transition's allowed list",
				"state %s: its timer uses transition %s -> %s, whose `allowed` list must include %s", s.ID, s.ID, s.At.To, timer)
		}
	}
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
func (t *Type) checkSets(r reporter) {
	var env *cel.Env
	for i := range t.Transitions {
		tr := &t.Transitions[i]
		tr.SetsContainer = nil
		if len(tr.Sets) == 0 {
			continue
		}
		p := at("transitions", i)
		if env == nil {
			var err error
			if env, err = newTransitionSetsEnv(t); err != nil {
				r.fail(CodeExpressionInvalid, at(p, "sets"), "Fix the fields the message names", "sets environment: %v", err)
				return
			}
		}
		name := tr.From + " -> " + tr.To
		from, _ := t.State(tr.From)
		tr.SetPrograms = map[string]cel.Program{}
		// Sorted: Go randomizes map order, and problems come in a stable one.
		for _, id := range slices.Sorted(maps.Keys(tr.Sets)) {
			t.checkSet(r, at(p, "sets", id), tr, name, from, id, env)
		}
		slices.Sort(tr.SetsContainer)
		for _, h := range t.Hooks {
			if h.Kind == HookCheck && h.From == tr.From && h.To == tr.To {
				r.fail(CodeHeldTransition, at(p, "sets"), "Remove sets, or compute the fields on a transition that is not held",
					"transition %s: it sets fields and is held by the check %s; a held transition may not set fields", name, h.ID)
				break
			}
		}
		for _, rx := range t.Reactions {
			if rx.Kind == ReactionHeld && rx.State == tr.To && (len(rx.From) == 0 || slices.Contains(rx.From, tr.From)) {
				r.fail(CodeHeldTransition, at(p, "sets"), "Remove sets, or compute the fields on a transition that is not held",
					"transition %s: it sets fields and is held by the reaction %s; a held transition may not set fields", name, rx.ID)
				break
			}
		}
	}
}

// checkSet checks and compiles one computed effect, tr.Sets[id], at p.
func (t *Type) checkSet(r reporter, p string, tr *Transition, name string, from State, id string, env *cel.Env) {
	expr := tr.Sets[id]
	f, ok := t.Field(id)
	switch {
	case !ok:
		r.fail(CodeUnknownField, p, "Declare the field, or remove it from sets", "transition %s: sets %q, which is not a declared field", name, id)
		return
	case slices.Contains(tr.Required, id):
		r.fail(CodeConflict, p, "Either require the field or compute it, not both", "transition %s: requires and sets %q", name, id)
		return
	case slices.Contains(tr.Clears, id):
		r.fail(CodeConflict, p, "Remove it from clears, and set it to null to unset it", "transition %s: clears and sets %q; to unset it, set it to null", name, id)
		return
	case slices.Contains(from.Frozen, id):
		r.fail(CodeWritesFrozenField, p, "Compute it on another transition, or stop freezing it in the state the move leaves",
			"transition %s: sets %q, which state %s freezes", name, id, tr.From)
		return
	}
	ref, err := checkCopy(f, expr, t)
	if err != nil {
		r.failExpr(err, p, "transition %s: sets %v", name, err)
		return
	}
	if ref.Each {
		r.fail(CodeReadNotAllowed, p, "Read a field or container.<field>: each is a create reaction's",
			"transition %s: sets %s from each, which only a create reaction with for_each reads", name, id)
		return
	}
	if _, plain := plainRef(expr); slices.Contains(tr.Allowed, AllowedFieldPrefix+id) && !plain {
		r.fail(CodeConflict, p, "Set it by a plain reference (a field or container.<field>), or compute another field",
			"transition %s: sets %q, which its own allowed list names; only a plain reference may hand it over", name, id)
		return
	}
	rest, reads, err := takeContainerFields(expr)
	if err != nil {
		r.failExpr(err, p, "transition %s: sets %s: %v", name, id, err)
		return
	}
	if linkedWord.MatchString(rest) {
		r.fail(CodeReadNotAllowed, p, "Read the container as container.<field>; linked Items are a create reaction's",
			"transition %s: sets %s: %q: an effect reads the container as container.<field>; linked Items are a create reaction's", name, id, expr)
		return
	}
	for _, x := range reads {
		if !slices.Contains(tr.SetsContainer, x) {
			tr.SetsContainer = append(tr.SetsContainer, x)
		}
	}
	ast, iss := env.Compile(expr)
	if iss.Err() != nil {
		r.fail(CodeExpressionInvalid, p, "Fix the CEL expression; it reads fields by their ids", "transition %s: sets %s: %q: %v", name, id, expr, iss.Err())
		return
	}
	if out := ast.OutputType(); !out.IsExactType(cel.DynType) && !fieldCELType(f).IsAssignableType(out) {
		r.fail(CodeExpressionType, p, "Compute a value of the field's type, or null to unset it",
			"transition %s: sets %s: %q yields %s, not a value of a %s field", name, id, expr, out, f.Type)
		return
	}
	prg, err := env.Program(ast)
	if err != nil {
		r.fail(CodeExpressionInvalid, p, "Fix the CEL expression", "transition %s: sets %s: %v", name, id, err)
		return
	}
	tr.SetPrograms[id] = prg
}
