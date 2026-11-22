package typedef

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"cel.dev/cel-go/common/decls"
	"cel.dev/cel-go/common/types"
	"cel.dev/cel-go/common/types/ref"
	"cel.dev/cel-go/common/types/traits"

	"github.com/francoisnyc/work-kernel/internal/worktime"
)

// Jira part 2, step 3 (docs/design.md, "Decisions for Jira part 2"): what
// the kernel needs for goals kept as contained Items. Nothing here is named
// after them.

// ByNotCreator is Reaction.By's second value: the Item created inside was
// created by someone else than the declaring Item's requester (the
// negation of policy.IsRequester). The kernel's own actors are neither.
const ByNotCreator = "not_creator"

// ReactionSet runs once its trigger (entering a state) has committed and
// sets fields on its target.
const ReactionSet ReactionKind = "set"

// TargetContainer is the one value Reaction.Target takes: the Item the
// source is contained in.
const TargetContainer = "container"

// WildcardType stands for any Type in an accepts_reactions key: *:<id>.
// It is honoured only for a reaction that reaches the Item through
// containment.
const WildcardType = "*"

// LinkedPrefix and ContainerLinkedPrefix start the CEL names of a linked
// Item's field in a create reaction's fields: linked.<link>.<field> (one
// declared max-one link away from the source) and
// container.linked.<link>.<field> (from the source's container).
const (
	LinkedPrefix          = "linked."
	ContainerLinkedPrefix = "container.linked."
)

// Children says which contained Items a move reaction moves: those of one
// of Of whose fields make When hold (child.<id>; no condition: all).
type Children struct {
	Of   []string `yaml:"of" json:"of"`
	When string   `yaml:"when" json:"when,omitempty"`

	// When compiled, and the child fields it reads, sorted; filled when
	// the Type is loaded.
	WhenProgram cel.Program `yaml:"-" json:"-"`
	Reads       []string    `yaml:"-" json:"-"`
}

// LinkedRead is one field of a linked Item a create reaction reads.
type LinkedRead struct {
	// Container: the link is the source's container's, not the source's.
	Container bool
	Link      string
	Field     string
}

// Consent returns what this Type accepts of reaction reactionID declared by
// Type typeID. contained says the reaction reaches the Item through
// containment (its container creating it or moving it as a child, or a
// contained Item setting a field on it): only then does a wildcard key
// (*:<id>) count. An exact key wins over the wildcard, whole.
func (t *Type) Consent(typeID, reactionID string, contained bool) (Accepted, bool) {
	if a, ok := t.AcceptsReactions[ReactionKey(typeID, reactionID)]; ok {
		return a, true
	}
	if contained {
		if a, ok := t.AcceptsReactions[ReactionKey(WildcardType, reactionID)]; ok {
			return a, true
		}
	}
	return Accepted{}, false
}

// ReadableByReaction reports whether a create reaction of another Type may
// read field from an Item of this Type.
func (t *Type) ReadableByReaction(field string) bool {
	return slices.Contains(t.ReadableByReactions, field)
}

// --- the calendar functions ---

// calendarType is the CEL type of calendar(...)'s result: opaque, so it can
// be passed to working_after and working_between and nowhere else (no
// field holds one).
var calendarType = cel.OpaqueType("calendar")

// calendarVal is a calendar as a CEL value. ref.Val is CEL's interface for
// values; a Go type satisfies an interface just by having its methods.
type calendarVal struct{ c *worktime.Calendar }

func (v calendarVal) ConvertToNative(reflect.Type) (any, error) {
	return nil, errors.New("a calendar is not a value of a field")
}
func (v calendarVal) ConvertToType(t ref.Type) ref.Val {
	if t == types.TypeType {
		return calendarType
	}
	return types.NewErr("a calendar converts to nothing")
}
func (v calendarVal) Equal(other ref.Val) ref.Val {
	o, ok := other.(calendarVal)
	return types.Bool(ok && o.c == v.c)
}
func (v calendarVal) Type() ref.Type { return calendarType }
func (v calendarVal) Value() any     { return v.c }

// stringList reads a CEL list of strings; null is an empty list (an unset
// multi-value field).
func stringList(v ref.Val, each func(ref.Val) (string, bool)) ([]string, bool) {
	if v == types.NullValue {
		return nil, true
	}
	l, ok := v.(traits.Lister)
	if !ok {
		return nil, false
	}
	var out []string
	for it := l.Iterator(); it.HasNext() == types.True; {
		s, ok := each(it.Next())
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// The errors name what failed and never a value: they end up in refusals.
func newCalendar(args ...ref.Val) ref.Val {
	week, ok := stringList(args[0], func(v ref.Val) (string, bool) { s, ok := v.Value().(string); return s, ok })
	if !ok {
		return types.NewErr("calendar: the week is not a list of strings")
	}
	// A date is a timestamp at 00:00 UTC to CEL; the holiday is that date.
	// (Read from a linked Item without its Type, a date arrives as its
	// stored text, YYYY-MM-DD: taken as it is.)
	holidays, ok := stringList(args[1], func(v ref.Val) (string, bool) {
		switch d := v.Value().(type) {
		case time.Time:
			return d.UTC().Format(time.DateOnly), true
		case string:
			return d, true
		}
		return "", false
	})
	if !ok {
		return types.NewErr("calendar: the holidays are not a list of dates")
	}
	zone, ok := args[2].Value().(string)
	if !ok {
		return types.NewErr("calendar: the zone is not set")
	}
	c, err := worktime.New(week, holidays, zone)
	if err != nil {
		return types.NewErr("calendar: not a calendar")
	}
	return calendarVal{c}
}

func workingAfter(args ...ref.Val) ref.Val {
	t, ok1 := args[0].Value().(time.Time)
	secs, ok2 := args[1].Value().(int64)
	c, ok3 := args[2].(calendarVal)
	if !ok1 || !ok2 || !ok3 {
		return types.NewErr("working_after: an instant, a number of seconds and a calendar are needed")
	}
	// Seconds beyond what a Duration holds are out of range like any
	// answer past the horizon.
	if secs > int64(worktime.Horizon/time.Second) {
		return types.NewErr("working_after: out of range")
	}
	out, err := c.c.After(t, time.Duration(secs)*time.Second)
	if err != nil {
		return types.NewErr("working_after: out of range")
	}
	return types.Timestamp{Time: out}
}

func workingBetween(args ...ref.Val) ref.Val {
	t1, ok1 := args[0].Value().(time.Time)
	t2, ok2 := args[1].Value().(time.Time)
	c, ok3 := args[2].(calendarVal)
	if !ok1 || !ok2 || !ok3 {
		return types.NewErr("working_between: two instants and a calendar are needed")
	}
	d, err := c.c.Between(t1, t2)
	if err != nil {
		return types.NewErr("working_between: out of range")
	}
	return types.Int(d / time.Second) // whole seconds, truncated
}

// calendarFunctions declares calendar, working_after and working_between.
// They are pure, so every expression may use them. The type guards CEL puts
// in front of a function are off: an unset field arrives as null, and each
// function says itself what it then lacks.
func calendarFunctions() []cel.EnvOption {
	return []cel.EnvOption{
		cel.Function("calendar", decls.DisableTypeGuards(true),
			cel.Overload("calendar_list_list_string",
				[]*cel.Type{cel.ListType(cel.StringType), cel.ListType(cel.TimestampType), cel.StringType},
				calendarType, cel.FunctionBinding(newCalendar))),
		cel.Function("working_after", decls.DisableTypeGuards(true),
			cel.Overload("working_after_timestamp_int_calendar",
				[]*cel.Type{cel.TimestampType, cel.IntType, calendarType},
				cel.TimestampType, cel.FunctionBinding(workingAfter))),
		cel.Function("working_between", decls.DisableTypeGuards(true),
			cel.Overload("working_between_timestamp_timestamp_calendar",
				[]*cel.Type{cel.TimestampType, cel.TimestampType, calendarType},
				cel.IntType, cel.FunctionBinding(workingBetween))),
	}
}

// --- a create reaction reads linked Items ---

var (
	containerLinkedRef = regexp.MustCompile(`\bcontainer\.linked\.([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)`)
	linkedRef          = regexp.MustCompile(`\blinked\.([A-Za-z_][A-Za-z0-9_]*)\.([A-Za-z_][A-Za-z0-9_]*)`)
	linkedWord         = regexp.MustCompile(`\b(container|linked)\b`)
)

// linkedTypes are the CEL types of the two names a create reaction's
// fields may read beyond the source: linked.<link>.<field> and
// container.linked.<link>.<field>. The Types read from are not known when
// one Type loads, so the fields are dynamic here; ParseDir checks them.
var linkedVarType = cel.MapType(cel.StringType, cel.MapType(cel.StringType, cel.DynType))

// newCreateEnv is the `sets` environment plus the linked Items, the
// container (container.<field> and container.linked.<link>.<field>; dynamic
// here, ParseDir checks them) and, with for_each, the member (each).
func newCreateEnv(t *Type, each *Field) (*cel.Env, error) {
	env, err := newSetsEnv(t)
	if err != nil {
		return nil, err
	}
	opts := []cel.EnvOption{cel.Variable("linked", linkedVarType), cel.Variable(ContainerVar, cel.DynType)}
	if each != nil {
		opts = append(opts, cel.Variable(EachVar, memberCELType(*each)))
	}
	return env.Extend(opts...)
}

// linkedReads finds the linked fields an expression reads, and the
// container's own fields (container.<field>, Jira 2.4b), and refuses any
// other use of `linked` and `container`: a link alone, the container's
// state, a read two links away.
func linkedReads(expr string) ([]LinkedRead, []string, error) {
	var out []LinkedRead
	take := func(re *regexp.Regexp, container bool, s string) (string, error) {
		for _, m := range re.FindAllStringSubmatchIndex(s, -1) {
			// What follows a read must end it: linked.a.b.c reads no field.
			if rest := s[m[1]:]; strings.HasPrefix(rest, ".") || strings.HasPrefix(rest, "[") || strings.HasPrefix(rest, "(") {
				return "", exprError{CodeReadNotAllowed, fmt.Errorf("%s is no read of a linked Item's field (linked.<link>.<field>)", s[m[0]:m[1]]+rest)}
			}
			out = append(out, LinkedRead{Container: container, Link: s[m[2]:m[3]], Field: s[m[4]:m[5]]})
		}
		return re.ReplaceAllString(s, "x"), nil
	}
	rest, err := take(containerLinkedRef, true, expr)
	if err != nil {
		return nil, nil, err
	}
	if rest, err = take(linkedRef, false, rest); err != nil {
		return nil, nil, err
	}
	rest, fields, err := takeContainerFields(rest)
	if err != nil {
		return nil, nil, err
	}
	if linkedWord.MatchString(rest) {
		return nil, nil, exprError{CodeReadNotAllowed, fmt.Errorf("%q: linked and container are read as linked.<link>.<field>, container.<field> and "+
			"container.linked.<link>.<field> only", expr)}
	}
	return out, fields, nil
}

func sortLinkedReads(reads []LinkedRead) []LinkedRead {
	slices.SortFunc(reads, func(a, b LinkedRead) int {
		ac, bc := 0, 0
		if a.Container {
			ac = 1
		}
		if b.Container {
			bc = 1
		}
		return cmp.Or(cmp.Compare(ac, bc), strings.Compare(a.Link, b.Link), strings.Compare(a.Field, b.Field))
	})
	return slices.Compact(reads)
}

// soleLinkedRead reports whether expr is exactly one linked read.
func soleLinkedRead(expr string) bool {
	e := strings.TrimSpace(expr)
	for _, re := range []*regexp.Regexp{containerLinkedRef, linkedRef} {
		if loc := re.FindStringIndex(e); loc != nil && loc[0] == 0 && loc[1] == len(e) {
			return true
		}
	}
	return false
}

// checkReadable validates readable_by_reactions.
func (t *Type) checkReadable(r reporter) {
	seen := map[string]bool{}
	for i, id := range t.ReadableByReactions {
		_, ok := t.Field(id)
		switch p := at("readable_by_reactions", i); {
		case !ok:
			r.fail(CodeUnknownField, p, "Declare the field, or remove it", "readable_by_reactions: field %q is not declared", id)
		case seen[id]:
			r.fail(CodeDuplicate, p, "Remove the second one", "readable_by_reactions names %s twice", id)
		}
		// A principal field may be read too (Jira 2.4b): a reader copies it
		// by plain reference only, into a principal field (ParseDir).
		seen[id] = true
	}
}

// --- children ---

// checkChildren validates and compiles a move reaction's `children`.
func (r *Reaction) checkChildren(rep reporter, p, where string) bool {
	c := r.Children
	if len(c.Of) == 0 {
		rep.fail(CodeRequired, at(p, "of"), "List the Types of the contained Items it moves",
			"%s: children names the Types of the contained Items it moves (of)", where)
		return false
	}
	c.WhenProgram, c.Reads = nil, nil
	if c.When == "" {
		return true
	}
	q := at(p, "when")
	env, err := newChildEnv()
	if err != nil {
		rep.fail(CodeExpressionInvalid, q, "Fix the expression", "%s: children.when: %v", where, err)
		return false
	}
	ast, iss := env.Compile(c.When)
	if iss.Err() != nil {
		rep.fail(CodeExpressionInvalid, q, "Fix the CEL expression; it reads a contained Item as child.<id>",
			"%s: children.when %q: %v (it reads a contained Item's fields, as child.<id>)", where, c.When, iss.Err())
		return false
	}
	if !ast.OutputType().IsExactType(cel.BoolType) {
		rep.fail(CodeExpressionType, q, "Write a condition that is true or false", "%s: children.when %q must yield a bool, not %s", where, c.When, ast.OutputType())
		return false
	}
	if c.WhenProgram, err = env.Program(ast); err != nil {
		rep.fail(CodeExpressionInvalid, q, "Fix the CEL expression", "%s: children.when: %v", where, err)
		return false
	}
	for _, m := range childRef.FindAllStringSubmatch(c.When, -1) {
		if !slices.Contains(c.Reads, m[1]) {
			c.Reads = append(c.Reads, m[1])
		}
	}
	slices.Sort(c.Reads)
	return true
}

// --- set ---

// checkSetReaction validates and compiles a set reaction within its Type.
// The target's fields are another Type's: ParseDir checks them where a
// Type accepts the reaction, and the write checks them for the rest.
func (t *Type) checkSetReaction(rep reporter, p string, r *Reaction, where string, ownWhen func() bool) {
	switch {
	case r.On != OnEnter:
		rep.fail(CodeInvalidValue, at(p, "on"), "Write on: enter", "%s: a set reaction is on: enter, not %q", where, r.On)
		return
	case r.Link != "" || r.To != "" || r.Children != nil || len(r.Of) > 0 || r.By != "" || len(r.TargetFrom) > 0:
		rep.fail(CodeKeyNotApplicable, at(p, firstKey(keySet{"link", r.Link != ""}, keySet{"to", r.To != ""}, keySet{"children", r.Children != nil},
			keySet{"of", len(r.Of) > 0}, keySet{"by", r.By != ""}, keySet{"target_from", len(r.TargetFrom) > 0})),
			"Remove it: a set reaction writes its container", "%s: link, to, children, of, by and target_from are not for a set reaction", where)
		return
	case r.Type != "" || r.Version != 0 || len(r.Fields) > 0:
		rep.fail(CodeKeyNotApplicable, at(p, firstKey(keySet{"type", r.Type != ""}, keySet{"version", r.Version != 0}, keySet{"fields", len(r.Fields) > 0})),
			"Remove it, or make the reaction a create", "%s: type, version and fields are for a create reaction", where)
		return
	case r.Target == "":
		rep.fail(CodeRequired, at(p, "target"), "Write target: container", "%s: a set reaction names the Item it writes (target: %s)", where, TargetContainer)
		return
	case r.Target != TargetContainer:
		rep.fail(CodeInvalidValue, at(p, "target"), "Write target: container", "%s: target %q: the only value is %s", where, r.Target, TargetContainer)
		return
	case len(r.Set) == 0:
		rep.fail(CodeRequired, at(p, "set"), "Name the fields it sets, and their expressions", "%s: a set reaction sets some field (set)", where)
		return
	}
	if !t.hasState(r.State) {
		rep.fail(CodeUnknownState, at(p, "state"), "Name the declared state whose entry triggers it",
			"%s: unknown state %q (on: enter needs the state entered)", where, r.State)
		return
	}
	if !ownWhen() {
		return
	}
	env, err := newSetsEnv(t)
	if err != nil {
		rep.fail(CodeExpressionInvalid, at(p, "set"), "Fix the fields the message names", "%s: set: %v", where, err)
		return
	}
	r.SetPrograms, r.setTypes = map[string]cel.Program{}, map[string]*cel.Type{}
	for _, id := range slices.Sorted(maps.Keys(r.Set)) {
		q := at(p, "set", id)
		ast, iss := env.Compile(r.Set[id])
		if iss.Err() != nil {
			rep.fail(CodeExpressionInvalid, q, "Fix the CEL expression; it reads this Item's fields by their ids", "%s: set %s: %q: %v", where, id, r.Set[id], iss.Err())
			return
		}
		prg, err := env.Program(ast)
		if err != nil {
			rep.fail(CodeExpressionInvalid, q, "Fix the CEL expression", "%s: set %s: %v", where, id, err)
			return
		}
		r.SetPrograms[id], r.setTypes[id] = prg, ast.OutputType()
	}
}

// --- an acting timer at an instant ---

// checkActingAt validates and compiles an acting timer at an instant. The
// caller checks its ID and its hook.
func (t *Type) checkActingAt(r reporter, p string, s *State, a *ActingTimer) {
	for _, k := range []struct {
		key, set, msg string
	}{
		{"after", a.After, "has `at`, so no `after`: it acts at an instant, not after a delay"},
		{"every", a.Every, "has `at`, so no `every`: it acts once for each instant"},
	} {
		if k.set != "" {
			r.fail(CodeKeyNotApplicable, at(p, k.key), "Remove it: a timer at an instant has no schedule", "%s", k.msg)
		}
	}
	if a.Max != 0 {
		r.fail(CodeKeyNotApplicable, at(p, "max"), "Remove it: a timer at an instant has no schedule",
			"has `at`, so no `max`: it acts once for each instant")
	}
	f, ok := t.Field(a.At)
	switch {
	case !ok:
		r.fail(CodeUnknownField, at(p, "at"), "Name a declared datetime field", "at: %q is not a declared field", a.At)
	case f.Type != DateTime || f.Multiple:
		r.fail(CodeWrongFieldType, at(p, "at"), "Name a datetime field holding one instant", "at: %s is not a datetime field holding one instant", a.At)
	}
	if len(a.Sets) == 0 && a.Hook == "" {
		r.fail(CodeRequired, p, "Give it sets, a hook, or both", "does nothing: it needs sets, a hook, or both")
	}
	env, err := newSetsEnv(t)
	if err != nil {
		r.fail(CodeExpressionInvalid, p, "Fix the fields the message names", "environment: %v", err)
		return
	}
	a.WhenProgram = nil
	if a.When != "" {
		prg, _, out, err := compileFields(env, a.When)
		switch {
		case err != nil:
			r.failExpr(err, at(p, "when"), "when %q: %v", a.When, err)
		case !out.IsExactType(cel.BoolType):
			r.fail(CodeExpressionType, at(p, "when"), "Write a condition that is true or false", "when %q must yield a bool, not %s", a.When, out)
		default:
			a.WhenProgram = prg
		}
	}
	a.SetPrograms = map[string]cel.Program{}
	for _, id := range slices.Sorted(maps.Keys(a.Sets)) {
		q := at(p, "sets", id)
		sf, ok := t.Field(id)
		switch {
		case !ok:
			r.fail(CodeUnknownField, q, "Declare the field, or remove it from sets", "sets %q, which is not a declared field", id)
			continue
		case id == a.At:
			r.fail(CodeConflict, q, "Set another field: the timer waits for this one", "sets %s, the field it waits for: it would arm itself again", id)
			continue
		case slices.Contains(s.Frozen, id):
			r.fail(CodeWritesFrozenField, q, "Set another field, or stop freezing it in this state", "sets %s, which its state freezes", id)
			continue
		case sf.Type == Principal:
			r.fail(CodeWrongFieldType, q, "Set a field that is not a principal: a timer never chooses who", "sets %s, a principal field; a timer may not set who", id)
			continue
		case sf.Multiple:
			r.fail(CodeWrongFieldType, q, "Set a field holding one value", "sets %s, a multi-value field; an effect on a set is not built", id)
			continue
		}
		ast, iss := env.Compile(a.Sets[id])
		if iss.Err() != nil {
			r.fail(CodeExpressionInvalid, q, "Fix the CEL expression; it reads fields by their ids", "sets %s: %q: %v", id, a.Sets[id], iss.Err())
			continue
		}
		if out := ast.OutputType(); !celType(sf.Type).IsAssignableType(out) {
			r.fail(CodeExpressionType, q, "Compute a value of the field's type", "sets %s: %q yields %s, not a value of a %s field", id, a.Sets[id], out, sf.Type)
			continue
		}
		prg, err := env.Program(ast)
		if err != nil {
			r.fail(CodeExpressionInvalid, q, "Fix the CEL expression", "sets %s: %v", id, err)
			continue
		}
		a.SetPrograms[id] = prg
	}
}

// ActingAtFields returns the fields the Type's acting timers at an instant
// wait for, sorted, each once.
func (t *Type) ActingAtFields() []string {
	var out []string
	for _, s := range t.States {
		for _, a := range s.Acting {
			if a.At != "" && !slices.Contains(out, a.At) {
				out = append(out, a.At)
			}
		}
	}
	slices.Sort(out)
	return out
}

// --- across Types ---

// checkStep3Across checks, across the Types loaded together, what step 3
// added: the children a reaction moves, the fields a create reaction reads
// from linked Items, and the set reactions a Type accepts.
func checkStep3Across(c *crossCheck, types []*Type, byID map[string][]*Type) {
	for _, src := range types {
		rep := c.on(src)
		for i, r := range src.Reactions {
			checkStep3AcrossOne(rep, at("reactions", i), types, byID, src, r)
		}
	}
	for _, tgt := range types {
		rep := c.on(tgt)
		for _, key := range slices.Sorted(maps.Keys(tgt.AcceptsReactions)) {
			checkSetConsent(rep, at("accepts_reactions", key), byID, tgt, key)
		}
	}
}

// checkStep3AcrossOne checks one reaction's children, linked reads and
// created fields against the Types loaded; its first problem stops it.
func checkStep3AcrossOne(rep reporter, p string, types []*Type, byID map[string][]*Type, src *Type, r Reaction) error {
	someVersion := func(id string, ok func(*Type) bool) bool { return slices.ContainsFunc(byID[id], ok) }
	if r.Kind == ReactionMove && r.Children != nil {
		for j, of := range r.Children.Of {
			if len(byID[of]) == 0 {
				return rep.fail(CodeUnknownType, at(p, "children", "of", j), "Load that Type too, or name a loaded one",
					"reaction %s moves contained Items of %s, which is not loaded", r.ID, of)
			}
			for _, f := range r.Children.Reads {
				if !someVersion(of, func(c *Type) bool { _, ok := c.Field(f); return ok }) {
					return rep.fail(CodeUnknownField, at(p, "children", "when"), "Read a field some version of the Type declares",
						"reaction %s: children.when reads child.%s, which no version of %s declares", r.ID, f, of)
				}
			}
			for _, tgt := range byID[of] {
				if err := checkMoveInto(rep, p, r, tgt); err != nil {
					return err
				}
				if a, _ := tgt.Consent(src.ID, r.ID, true); !slices.Contains(a.Moves, r.To) {
					return rep.fail(CodeConsentMissing, p, "Add the consent to the contained Type's accepts_reactions",
						"reaction %s moves contained Items of %s v%d into %s, which does not accept it "+
							"(accepts_reactions: {'*:%s': {moves: [%s]}})", r.ID, tgt.ID, tgt.Version, r.To, r.ID, r.To)
				}
			}
		}
	}
	for _, lr := range r.LinkedReads {
		name := LinkedPrefix + lr.Link + "." + lr.Field
		readable := func(l LinkType) bool {
			return l.SourceSingle() && slices.ContainsFunc(l.Targets, func(target string) bool {
				return someVersion(target, func(c *Type) bool { return c.ReadableByReaction(lr.Field) })
			})
		}
		if !lr.Container {
			if l, _ := src.Link(lr.Link); !readable(l) {
				return rep.fail(CodeReadNotAllowed, at(p, "fields"), "List the field under readable_by_reactions in the linked Type",
					"reaction %s reads %s, but no Type link %s points to lets reactions read %s (readable_by_reactions)",
					r.ID, name, lr.Link, lr.Field)
			}
			continue
		}
		if !slices.ContainsFunc(types, func(c *Type) bool { l, ok := c.Link(lr.Link); return ok && readable(l) }) {
			return rep.fail(CodeReadNotAllowed, at(p, "fields"), "Declare the link on the container's Type, and list the field under readable_by_reactions",
				"reaction %s reads container.%s, but no loaded Type that could be the container declares "+
					"a max-one link %s to a Type that lets reactions read %s (readable_by_reactions)", r.ID, name, lr.Link, lr.Field)
		}
	}
	if r.Kind != ReactionCreate {
		return nil
	}
	for _, id := range slices.Sorted(maps.Keys(r.Fields)) {
		if !soleLinkedRead(r.Fields[id]) {
			continue
		}
		reads, _, _ := linkedReads(r.Fields[id])
		for _, created := range byID[r.Type] {
			cf, ok := created.Field(id)
			if created.Version != r.Version || !ok {
				continue
			}
			for _, c := range types {
				rf, declared := c.Field(reads[0].Field)
				if declared && c.ReadableByReaction(reads[0].Field) && (rf.Multiple != cf.Multiple || !sameStoredType(rf, cf)) {
					return rep.fail(CodeWrongFieldType, at(p, "fields", id), "Copy a field of the same type and shape",
						"reaction %s: field %s: %s of %s is not a value of a %s field of %s", r.ID, id, reads[0].Field, c.ID, cf.Type, created.ID)
				}
			}
		}
	}
	return nil
}

// checkSetConsent checks tgt's consent key to set reactions: each field the
// reaction sets is listed, declared and settable, with a value of its type.
func checkSetConsent(rep reporter, p string, byID map[string][]*Type, tgt *Type, key string) error {
	acc := tgt.AcceptsReactions[key]
	typeID, id, _ := strings.Cut(key, ":")
	if len(acc.Sets) == 0 || typeID == WildcardType {
		return nil
	}
	found := false
	for _, src := range byID[typeID] {
		r, ok := src.Reaction(id)
		if !ok || r.Kind != ReactionSet {
			continue
		}
		found = true
		for _, f := range slices.Sorted(maps.Keys(r.Set)) {
			tf, declared := tgt.Field(f)
			switch {
			case !slices.Contains(acc.Sets, f):
				return rep.fail(CodeConsentMissing, at(p, "sets"), "List the field under sets in the consent",
					"accepts_reactions %s: the reaction sets %s, which the consent does not list (sets)", key, f)
			case !declared:
				return rep.fail(CodeUnknownField, at(p, "sets"), "Declare the field, or ask the reaction to set another",
					"accepts_reactions %s: the reaction sets %s, which is not declared", key, f)
			case tf.Type == Principal || tf.Multiple:
				return rep.fail(CodeWrongFieldType, at(p, "sets"), "Let the reaction set a field holding one value that is not a principal",
					"accepts_reactions %s: the reaction sets %s, a field a reaction may not set", key, f)
			}
			if out := r.setTypes[f]; out != nil && !celType(tf.Type).IsAssignableType(out) {
				return rep.fail(CodeExpressionType, at(p, "sets"), "Have the reaction compute a value of the field's type",
					"accepts_reactions %s: the reaction sets %s to %q, which yields %s, not a value of a %s field", key, f, r.Set[f], out, tf.Type)
			}
		}
	}
	if !found {
		return rep.fail(CodeUnknownReaction, p, "Name a set reaction of that Type, or remove the consent",
			"accepts_reactions names %s for sets, which is no set reaction of %s", key, typeID)
	}
	return nil
}

// sameStoredType reports whether a value of field a is a value of field b,
// as far as their types say (an enum's options are judged at the write).
func sameStoredType(a, b Field) bool {
	text := func(t FieldType) bool { return t == String || t == Enum }
	return a.Type == b.Type || (text(a.Type) && text(b.Type))
}
