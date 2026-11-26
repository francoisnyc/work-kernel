package reading

import (
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The order of a reading: In short; Who starts it; Who writes what; Links; one section
// per state, in the Type's order (what entering it does, what happens
// while it is there, each move out with what it waits for, or that
// nothing leaves); When something is created inside it; What other Types
// may do to it; Who sees it. A section with nothing to say is left out.

// hookBudget is the kernel's budget for a hook (ScheduleToClose): the
// same for every hook, so a reading may say it.
const hookBudget = 15 * time.Minute

type renderer struct {
	t        *typedef.Type
	cat      *contracts.Catalog
	ctx      exprCtx
	sections []Section
	n        int
}

// Read renders a loaded Type. cat gives the actions' input names, reasons,
// risk and descriptions.
func Read(t *typedef.Type, cat *contracts.Catalog) *Reading {
	r := &renderer{t: t, cat: cat, ctx: newCtx(t)}
	r.inShort()
	r.whoStarts()
	r.whoMigrates()
	r.whoWrites()
	r.links()
	if !noLifecycle(t) {
		for i := range t.States {
			r.state(i)
		}
	}
	r.createdInside()
	r.others()
	r.whoSees()
	var secs []Section
	for _, s := range r.sections {
		if len(s.Sentences) > 0 {
			secs = append(secs, s)
		}
	}
	rd := &Reading{
		Type:     fmt.Sprintf("%s@%d", t.ID, t.Version),
		Fields:   fieldList(t),
		Sections: secs,
		Actions:  r.actions(),
		NotCovered: []string{
			"Who can see a given " + t.ID + ": that is set for each Item and its container, not by its Type.",
			// The latest version's start block judges a create of every
			// version, and its placement rule a move or a narrow of an
			// Item of every version, so no reading of one version can
			// say either.
			"Who may create " + withArticle(t.ID) + ", and where, and who may move or narrow one, once a later version exists: the latest version of " + t.ID + " decides, for every version.",
			"How the kernel keeps its promises (retries, each write made once, recovery after a failure): the same for every Type.",
		},
	}
	rd.Key = keyOf(rd)
	return rd
}

// noLifecycle reports whether the Type declared no states: it has the one
// implicit state and no move.
func noLifecycle(t *typedef.Type) bool {
	return len(t.Transitions) == 0 && len(t.States) == 1 &&
		t.States[0].ID == typedef.ImplicitState && t.States[0].Category == typedef.ImplicitStateCategory
}

func (r *renderer) section(title string) { r.sections = append(r.sections, Section{Title: title}) }

// say adds a sentence to the current section: its kind is its first part.
// Texts start as written: a sentence may start with an identifier (a Type,
// a field), which keeps its case.
func (r *renderer) say(parts []Part, paths []string, text string) {
	r.n++
	last := &r.sections[len(r.sections)-1]
	last.Sentences = append(last.Sentences, Sentence{N: r.n, Kind: parts[0], Parts: parts, Text: text, Paths: paths})
}

func capitalize(s string) string {
	if s == "" || s[0] == '`' {
		return s
	}
	rs := []rune(s)
	rs[0] = unicode.ToUpper(rs[0])
	return string(rs)
}

func at(format string, args ...any) string { return fmt.Sprintf(format, args...) }

// --- In short ---

func (r *renderer) inShort() {
	t := r.t
	r.section("In short")
	if noLifecycle(t) {
		r.say([]Part{NoLifecycle}, []string{"initial_state", "states[0]"}, at("%s %s has no lifecycle: it stays in `%s`.", capitalize(article(t.ID)), t.ID, typedef.ImplicitState))
	} else {
		r.say([]Part{Starts}, []string{"initial_state"}, at("%s %s starts in %s.", capitalize(article(t.ID)), t.ID, t.InitialState))
		r.say([]Part{Closed}, []string{"initial_state"}, "It moves only as listed below; any other move is refused.")
	}
	for i, inv := range t.Invariants {
		r.say([]Part{Invariant}, []string{at("invariants[%d]", i)},
			at("Every write keeps this true: %s; a write that would not is refused (%s).", r.ctx.condition(inv.Rule), inv.ID))
	}
}

// --- Who starts it ---

// whoStarts reads the start block: where an Item of the Type is created,
// who creates one (every entry reads the container), and that the block
// holds for every version while this version is the latest.
func (r *renderer) whoStarts() {
	t := r.t
	s := t.Start
	if s == nil {
		return
	}
	r.section("Who starts it")
	path := []string{"start"}
	subject := capitalize(withArticle(t.ID))
	if len(s.In) > 0 {
		var types []string
		top := false
		for _, id := range s.In {
			if id == typedef.StartTop {
				top = true
			} else {
				types = append(types, withArticle(id))
			}
		}
		switch {
		case len(types) == 0:
			r.say([]Part{StartIn, StartInTop}, path, subject+" is created only at the top level.")
		case top:
			r.say([]Part{StartIn, StartInTop}, path, subject+" is created only inside "+joinOr(types)+", or at the top level.")
		default:
			r.say([]Part{StartIn}, path, subject+" is created only inside "+joinOr(types)+".")
		}
	}
	if s.Nobody() {
		r.say([]Part{StartWho, StartWhoNobody}, path, "No person creates "+withArticle(t.ID)+": only a reaction does.")
	} else {
		parts := []Part{StartWho}
		var phrases []string
		if len(s.Allowed) == 0 {
			phrases, parts = append(phrases, "any staff member who can see the container"), append(parts, WhoStaff)
		}
		for _, e := range s.Allowed {
			switch {
			case strings.HasPrefix(e, typedef.AllowedRolePrefix):
				phrases, parts = append(phrases, "anyone with the role "+strings.TrimPrefix(e, typedef.AllowedRolePrefix)), append(parts, WhoRole)
			case strings.HasPrefix(e, typedef.AllowedFieldPrefix):
				phrases, parts = append(phrases, "whoever `"+strings.TrimPrefix(e, typedef.AllowedFieldPrefix)+"` of the container names"),
					append(parts, StartWhoField)
			case e == typedef.AllowedContainerCreator:
				phrases, parts = append(phrases, "anyone who took part in creating the container"), append(parts, StartWhoContainerCreator)
			default: // principal:user:<id>, the only principal a block may name
				phrases, parts = append(phrases, "`"+strings.TrimPrefix(e, typedef.AllowedPrincipalPrefix)+"`"), append(parts, WhoPrincipal)
			}
		}
		who := strings.Join(phrases, ", or ")
		if len(phrases) > 1 {
			who += ","
		}
		r.say(append(parts, StartWhoCustomers), path,
			capitalize(who)+" creates one; a customer creates one only where a container lets them.")
	}
	r.say([]Part{StartLatest}, path,
		at("While this is the latest version of %s, these rules apply to creating %s of any version.", t.ID, withArticle(t.ID)))
	// Which older versions may still be created (track 1, item 6, step 1).
	// A block without the key says nothing: only the latest version is
	// created then, the kernel's rule for every Type.
	if o := s.OlderVersions; o != nil {
		older := []string{"start.older_versions"}
		if o.All {
			r.say([]Part{StartOlder, StartOlderAll}, older,
				at("While this is the latest version of %s, %s of any older version may still be created.", t.ID, withArticle(t.ID)))
		} else {
			versions := slices.Clone(o.Versions)
			slices.Sort(versions)
			var names []string
			for _, v := range versions {
				names = append(names, strconv.Itoa(v))
			}
			r.say([]Part{StartOlder}, older,
				at("While this is the latest version of %s, %s of version %s may still be created; one of any other older version may not.",
					t.ID, withArticle(t.ID), joinOr(names)))
		}
	}
}

// --- Who migrates Items to it ---

// whoMigrates reads the migrate block: who may migrate an open Item of an
// older version to this one, while it is the latest. A Type without the
// block says nothing: nobody may, the kernel's rule for every Type.
func (r *renderer) whoMigrates() {
	t := r.t
	if t.Migrate == nil {
		return
	}
	r.section("Who migrates Items to it")
	parts := []Part{MigrateWho}
	var phrases []string
	for _, e := range t.Migrate.Allowed {
		if role, ok := strings.CutPrefix(e, typedef.AllowedRolePrefix); ok {
			phrases, parts = append(phrases, "anyone with the role "+role), append(parts, WhoRole)
		} else {
			phrases, parts = append(phrases, "`"+strings.TrimPrefix(e, typedef.AllowedPrincipalPrefix)+"`"), append(parts, WhoPrincipal)
		}
	}
	who := strings.Join(phrases, ", or ")
	if len(phrases) > 1 {
		who += ","
	}
	r.say(parts, []string{"migrate"},
		at("While this is the latest version of %s, %s may migrate an open %s of an older version to it, once it fits.", t.ID, who, t.ID))
}

// --- Who writes what ---

func (r *renderer) whoWrites() {
	t := r.t
	r.section("Who writes what")
	for i, f := range t.Fields {
		path := []string{at("fields[%d]", i)}
		switch {
		case f.WritableBy == typedef.WritableByKernel:
			if ws := r.kernelWriters(f.ID); len(ws) > 0 {
				r.say([]Part{FieldKernel}, path, at("Only the kernel writes `%s`: %s.", f.ID, joinGroups(ws)))
			} else {
				r.say([]Part{FieldKernel}, path, at("Only the kernel writes `%s`.", f.ID))
			}
		case f.WritableByAgents && !f.PeopleWrite() && len(f.WritableByRoles) > 0:
			r.say([]Part{FieldAgentsOnly, FieldRoles}, path,
				at("Only agents and holders of %s write `%s`.", joinOr(f.WritableByRoles), f.ID))
		case f.WritableByAgents && !f.PeopleWrite():
			r.say([]Part{FieldAgentsOnly}, path, at("Only agents write `%s`.", f.ID))
		case len(f.WritableByRoles) > 0 && !f.WritableByPeople && !f.WritableByAgents:
			r.say([]Part{FieldRoles}, path, at("Only holders of %s write `%s`.", joinOr(f.WritableByRoles), f.ID))
		}
		if len(f.UniqueAmongSiblings) > 0 {
			r.say([]Part{FieldUnique}, path, at("No two %s in one container, both in %s, hold the same `%s`.",
				plural([]string{t.ID}), joinOr(f.UniqueAmongSiblings), f.ID))
		}
	}
	r.agentWriters()
}

// agentWriters says, last in who writes what, which fields an agent writes:
// those its Type opens to agents, and one that names a role the agent
// holds (fields are closed to agents by default). An agent acting for a
// customer is held to the customer rules. A Type with no field but the
// kernel's says nothing.
func (r *renderer) agentWriters() {
	t := r.t
	var open, paths []string
	roles := false
	for i, f := range t.Fields {
		if f.WritableBy == typedef.WritableByKernel {
			continue
		}
		paths = append(paths, at("fields[%d]", i))
		if f.WritableByAgents {
			open = append(open, f.ID)
		} else if len(f.WritableByRoles) > 0 {
			roles = true
		}
	}
	if len(paths) == 0 {
		return
	}
	parts := []Part{FieldAgents}
	var text string
	switch {
	case len(open) == 0 && roles:
		text, parts = "No agent writes its fields, except one that names a role the agent holds", append(parts, FieldAgentsRoles)
	case len(open) == 0:
		text = "No agent writes its fields"
	case roles:
		text = "An agent writes only " + joinAnd(ticked(open)) + ", and a field that names a role the agent holds"
		parts = append(parts, FieldAgentsRoles)
	default:
		text = "An agent writes only " + joinAnd(ticked(open))
	}
	if len(t.CustomerFields) > 0 {
		text += "; an agent acting for a customer sets only what the customer may"
		parts, paths = append(parts, FieldAgentsCustomers), append(paths, "customer_fields")
	}
	r.say(parts, paths, text+".")
}

// kernelWriters are who writes a kernel field, as the Type names them:
// other Types' reactions it accepts, its hooks, its acting timers, its moves.
func (r *renderer) kernelWriters(field string) []string {
	t := r.t
	var groups []string
	byType := map[string][]string{}
	for _, key := range slices.Sorted(maps.Keys(t.AcceptsReactions)) {
		a := t.AcceptsReactions[key]
		if slices.Contains(a.Fields, field) || slices.Contains(a.Sets, field) || slices.Contains(a.Creates, field) {
			ty, id, _ := strings.Cut(key, ":")
			byType[ty] = append(byType[ty], id)
		}
	}
	for _, ty := range slices.Sorted(maps.Keys(byType)) {
		groups = append(groups, reactionsOf(ty, byType[ty]))
	}
	var hooks []string
	for _, h := range t.Hooks {
		if slices.Contains(h.Writes, field) {
			hooks = append(hooks, "`"+h.ID+"`")
		}
	}
	if len(hooks) > 0 {
		groups = append(groups, joinAnd(hooks))
	}
	for _, s := range t.States {
		for _, a := range s.Acting {
			if _, ok := a.Sets[field]; ok {
				groups = append(groups, "the acting timer `"+a.ID+"`")
			}
		}
	}
	for _, tr := range t.Transitions {
		if _, ok := tr.Sets[field]; ok {
			groups = append(groups, at("the move from %s to %s", tr.From, tr.To))
		}
	}
	return groups
}

// reactionsOf names reactions of a Type (or of any Type, for *).
func reactionsOf(ty string, ids []string) string {
	quoted := make([]string, len(ids))
	for i, id := range ids {
		quoted[i] = "`" + id + "`"
	}
	noun := "reaction"
	if len(ids) > 1 {
		noun = "reactions"
	}
	owner := ty + "'s"
	if ty == typedef.WildcardType {
		owner = "any Type's"
	}
	return owner + " " + noun + " " + joinAnd(quoted)
}

// joinGroups joins groups that may hold "and" themselves: "a, and b".
func joinGroups(groups []string) string {
	if len(groups) == 1 {
		return groups[0]
	}
	return strings.Join(groups[:len(groups)-1], ", ") + ", and " + groups[len(groups)-1]
}

// --- Links ---

func (r *renderer) links() {
	t := r.t
	r.section("Links")
	for i, l := range t.Links {
		parts := []Part{Link}
		who, verb := "Anyone who can see it", "links"
		switch {
		case l.WritableBy == typedef.WritableByKernel:
			who = "Only the kernel"
			parts = append(parts, LinkKernel)
		case len(l.WritableByRoles) > 0:
			who, verb = "Only holders of "+joinOr(l.WritableByRoles), "link"
			parts = append(parts, LinkRoles)
		}
		count := "any number"
		if l.SourceSingle() {
			count = "at most one"
			parts = append(parts, LinkOne)
		}
		each := "each"
		if len(l.Targets) == 1 {
			each = withArticle(l.Targets[0])
		}
		text := at("%s %s it to %s as `%s`, %s", who, verb, joinOr(withArticles(l.Targets)), l.Name, count)
		if l.TargetSingle() {
			text += at("; %s takes at most one", each)
			parts = append(parts, LinkInverseOne)
		}
		text += at("; %s sees it as `%s`.", each, l.Inverse)
		r.say(parts, []string{at("links[%d]", i)}, text)
	}
	for i, in := range t.Incoming {
		r.say([]Part{Incoming}, []string{at("incoming[%d]", i)},
			at("It reads the %s linked to it as `%s` under the name `%s`.", plural(in.From), in.Link, in.Name))
	}
	for i, ru := range t.Rollups {
		r.say([]Part{Rollup}, []string{at("rollups[%d]", i)},
			at("When it is read, `%s` counts its `%s` by %s.", ru.ID, ru.Over, ru.CountBy))
	}
}

// --- A state ---

var categoryWords = map[string]string{
	"triage": "to triage", "backlog": "backlog", "todo": "to do", "active": "active",
	"waiting": "waiting", "done": "done", "canceled": "canceled", "none": "not work",
}

func (r *renderer) state(i int) {
	t, s := r.t, r.t.States[i]
	r.section(at("%s (%s)", s.ID, categoryWords[s.Category]))

	// What entering it does.
	for li, l := range t.Links {
		if l.Resolve != nil && l.Resolve.OnEnter == s.ID {
			target := ""
			if len(l.Targets) > 0 {
				target = l.Targets[0]
			}
			r.say([]Part{Resolve}, []string{at("links[%d].resolve", li)},
				at("On entering %s, the kernel links `%s` to the requester's %s %s (the one whose `%s` is the requester); "+
					"if there is none, or more than one, the move into %s is refused.", s.ID, l.Name, l.Resolve.State, target, l.Resolve.Field, s.ID))
		}
	}
	for hi, h := range t.Hooks {
		if h.On != typedef.OnEnter || h.State != s.ID {
			continue
		}
		path := []string{at("hooks[%d]", hi)}
		if h.Kind == typedef.HookEnrich {
			parts := []Part{Enrich}
			text := at("On entering %s, `%s` calls %s and writes its answer into %s", s.ID, h.ID, r.call(h), joinAnd(ticked(h.Writes)))
			if len(h.Fallback) > 0 {
				var vals []string
				for _, f := range slices.Sorted(maps.Keys(h.Fallback)) {
					vals = append(vals, at("`%s` = %s", f, literal(h.Fallback[f])))
				}
				text += at("; with no answer within %s it writes %s", duration(hookBudget), joinAnd(vals))
				parts = append(parts, EnrichFallbk)
			}
			r.say(parts, path, text+".")
			continue
		}
		parts := []Part{Action}
		text := at("On entering %s, `%s` calls %s", s.ID, h.ID, r.call(h))
		if len(h.Writes) > 0 {
			text += " and writes what it made into " + joinAnd(ticked(h.Writes))
			parts = append(parts, ActionWrites)
		}
		r.say(parts, path, text+", once per entry, even if it has moved on.")
	}
	for ri, rx := range t.Reactions {
		if rx.On == typedef.OnEnter && rx.State == s.ID && rx.Kind != typedef.ReactionHeld {
			r.reaction(ri)
		}
	}

	// While it is there.
	if s.Claim != "" {
		r.say([]Part{Claim}, []string{at("states[%d].claim", i)},
			at("Whoever moves it into %s claims it: `%s` names them, and only they move it on from there, unless a move releases the claim.",
				s.ID, s.Claim))
	}
	r.waits(i, s)
	if len(s.Frozen) > 0 {
		r.say([]Part{Frozen}, []string{at("states[%d].frozen", i)},
			at("While it is in %s, nobody changes %s.", s.ID, joinOr(ticked(s.Frozen))))
	}
	if len(s.RequesterOnly) > 0 {
		r.say([]Part{RequesterOnly}, []string{at("states[%d].requester_only", i)},
			at("While it is in %s, only the requester changes %s.", s.ID, joinOr(ticked(s.RequesterOnly))))
	}
	if s.Timer != nil {
		parts := []Part{Timer}
		text := at("%s after it enters %s, if it is still there, the timer moves it to %s", duration(s.Timer.Duration), s.ID, s.Timer.To)
		if r.holdsLeaving(s.ID) {
			text += "; if a hold is open then, the timer waits for it to close and looks again"
			parts = append(parts, TimerWaits)
		}
		r.say(parts, []string{at("states[%d].timer", i)}, text+".")
	}
	if s.At != nil {
		r.say([]Part{AtTimer}, []string{at("states[%d].at", i)},
			at("When it is in %s and the instant in `%s` has passed, the timer moves it to %s.", s.ID, s.At.Field, s.At.To))
	}
	for j, a := range s.Acting {
		path := []string{at("states[%d].acting[%d]", i, j)}
		h, hi, hasHook := r.hook(a.Hook)
		if hasHook {
			path = append(path, at("hooks[%d]", hi))
		}
		if a.At == "" {
			parts := []Part{Acting}
			text := duration(a.AfterDuration) + " after it enters " + s.ID
			if a.Max > 1 && a.Every != "" {
				text += ", then every " + duration(a.EveryDuration)
				parts = append(parts, ActingEvery)
			}
			text += at(", `%s` calls %s, at most %s while it stays there.", a.Hook, r.call(h), times(a.Max))
			r.say(parts, path, text)
			continue
		}
		parts := []Part{ActingAt}
		text := at("When it is in %s and the instant in `%s` has passed, ", s.ID, a.At)
		if a.When != "" {
			text += "if " + r.ctx.condition(a.When) + ", "
			parts = append(parts, ActingAtWhen)
		}
		var doing []string
		if len(a.Sets) > 0 {
			c := r.ctx
			c.causeAt = "that instant"
			doing = append(doing, at("`%s` sets %s", a.ID, assignments(c, a.Sets)))
			parts = append(parts, ActingAtSets)
		}
		if hasHook {
			doing = append(doing, at("`%s` calls %s", a.Hook, r.call(h)))
		}
		r.say(parts, path, text+strings.Join(doing, " and ")+".")
	}

	// The moves out, and what each waits for.
	out := false
	for ti, tr := range t.Transitions {
		if tr.From != s.ID {
			continue
		}
		out = true
		r.move(ti)
		if h, ok := lifecycle.CheckFor(t, tr.From, tr.To); ok {
			_, hi, _ := r.hook(h.ID)
			hpath := at("hooks[%d]", hi)
			parts := []Part{Check}
			text := at("Asking for this move opens a hold instead: `%s` calls %s; if it allows", h.ID, r.call(h))
			if len(h.Writes) > 0 {
				text += ", or corrects " + joinAnd(ticked(h.Writes)) + ","
				parts = append(parts, CheckCorrect)
			}
			text += " the move happens; it may deny it"
			if reasons := r.reasons(h); len(reasons) > 0 {
				text += " (" + strings.Join(reasons, ", ") + ")"
			}
			text += at("; with no answer within %s the move fails.", duration(hookBudget))
			r.say(parts, []string{hpath}, text)
			r.hold(tr.From, fieldsOnly(t, h.Reads), hpath)
		}
		if rx, ok := lifecycle.HeldReaction(t, tr.From, tr.To); ok {
			ri := slices.IndexFunc(t.Reactions, func(x typedef.Reaction) bool { return x.ID == rx.ID })
			li := slices.IndexFunc(t.Links, func(l typedef.LinkType) bool { return l.Name == rx.Link })
			var adds []string
			for _, f := range slices.Sorted(maps.Keys(rx.Add)) {
				v, _ := r.valueWords(r.ctx, rx.Add[f])
				adds = append(adds, at("%s to `%s`", v, f))
			}
			target := rx.Link
			if li >= 0 {
				target = joinOr(t.Links[li].Targets)
			}
			paths := []string{at("reactions[%d]", ri)}
			if li >= 0 {
				paths = append(paths, at("links[%d]", li))
			}
			r.say([]Part{Held}, paths, at("Asking for this move opens a hold instead: `%s` adds %s on the %s linked as `%s`; "+
				"the move happens only if that Item accepts the write, else it is denied with that Item's reason.", rx.ID, joinAnd(adds), target, rx.Link))
			r.hold(tr.From, rx.Reads, at("reactions[%d]", ri))
		}
	}
	if !out {
		r.say([]Part{Final}, []string{at("states[%d]", i)}, at("Nothing leaves %s.", s.ID))
	}
}

// holdsLeaving reports whether a move out of the state waits for a check
// or a held reaction.
func (r *renderer) holdsLeaving(state string) bool {
	for _, tr := range r.t.Transitions {
		if tr.From != state {
			continue
		}
		if _, ok := lifecycle.CheckFor(r.t, tr.From, tr.To); ok {
			return true
		}
		if _, ok := lifecycle.HeldReaction(r.t, tr.From, tr.To); ok {
			return true
		}
	}
	return false
}

// hold says what a hold on a move out of from forbids.
func (r *renderer) hold(from string, frozen []string, decider string) {
	parts := []Part{Hold}
	paths := []string{decider}
	var escapes []string
	for ti, tr := range r.t.Transitions {
		if tr.From == from && tr.Escape {
			escapes = append(escapes, tr.To)
			paths = append(paths, at("transitions[%d]", ti))
		}
	}
	var clauses []string
	if len(frozen) > 0 {
		clauses = append(clauses, "nobody changes "+joinOr(ticked(frozen)))
		parts = append(parts, HoldFrozen)
	}
	switch len(escapes) {
	case 0:
		clauses = append(clauses, "nothing moves it")
		parts = append(parts, HoldNone)
	case 1:
		clauses = append(clauses, at("the only move out of %s is to %s", from, escapes[0]))
		parts = append(parts, HoldEscape)
	default:
		clauses = append(clauses, at("the only moves out of %s are to %s", from, joinOr(escapes)))
		parts = append(parts, HoldEscape)
	}
	r.say(parts, paths, "While that hold is open, "+strings.Join(clauses, ", and ")+".")
}

// move says who may make a move, and what it needs and does.
func (r *renderer) move(ti int) {
	tr := r.t.Transitions[ti]
	subject, verb, parts := whoMoves(tr)
	parts = append([]Part{Move}, parts...)
	text := at("%s %s it from %s to %s", capitalize(subject), verb, tr.From, tr.To)
	// Required fields are to be set; those the move itself must give
	// (given) read apart: a value written before does not count for them.
	var setOnly []string
	for _, id := range tr.Required {
		if !slices.Contains(tr.Given, id) {
			setOnly = append(setOnly, id)
		}
	}
	if len(setOnly) > 0 {
		be := "is"
		if len(setOnly) > 1 {
			be = "are"
		}
		text += at(", once %s %s set", joinAnd(ticked(setOnly)), be)
		parts = append(parts, MoveRequired)
	}
	if len(tr.Given) > 0 {
		if len(setOnly) > 0 {
			text += " and"
		} else {
			text += ", once"
		}
		text += at(" the mover gives %s in the move itself", joinAnd(ticked(tr.Given)))
		parts = append(parts, MoveGiven)
	}
	if tr.Guard != "" {
		text += ", only if " + r.ctx.condition(tr.Guard)
		parts = append(parts, MoveGuard)
	}
	if tr.Release {
		text += ", which releases the claim"
		parts = append(parts, MoveRelease)
	}
	if tr.Escape {
		text += ", even while a hold is open on it, which cancels the hold"
		parts = append(parts, MoveEscape)
	}
	if len(tr.Clears) > 0 {
		text += "; the move clears " + joinAnd(ticked(tr.Clears))
		parts = append(parts, MoveClears)
	}
	if len(tr.Sets) > 0 {
		text += "; the move sets " + assignments(r.ctx, tr.Sets)
		parts = append(parts, MoveSets)
	}
	r.say(parts, []string{at("transitions[%d]", ti)}, text+".")
}

// whoMoves is a move's subject and verb from its allowed and except lists.
func whoMoves(tr typedef.Transition) (string, string, []Part) {
	var phrases []string
	var parts []Part
	verb := "moves"
	switch {
	case slices.Contains(tr.Allowed, typedef.AllowedNobody):
		phrases, parts = []string{"only a reaction"}, []Part{WhoNobody}
	case len(tr.Allowed) == 0:
		phrases, parts = []string{"any staff member who can see it"}, []Part{WhoStaff}
	case len(tr.Allowed) == 1 && tr.Allowed[0] == typedef.AllowedPrincipalPrefix+typedef.TimerPrincipal:
		phrases, parts = []string{"only the timer"}, []Part{WhoTimer}
	case len(tr.Allowed) == 1 && strings.HasPrefix(tr.Allowed[0], typedef.AllowedPrincipalPrefix+"hook:"):
		phrases, parts = []string{"only the hook `" + hookOf(tr.Allowed[0]) + "`"}, []Part{WhoHook}
		verb = "may move"
	default:
		for _, e := range tr.Allowed {
			switch {
			case e == typedef.AllowedCreator:
				phrases, parts = append(phrases, "the requester"), append(parts, WhoCreator)
			case strings.HasPrefix(e, typedef.AllowedRolePrefix):
				phrases, parts = append(phrases, "anyone with the role "+strings.TrimPrefix(e, typedef.AllowedRolePrefix)), append(parts, WhoRole)
			case strings.HasPrefix(e, typedef.AllowedFieldPrefix):
				phrases, parts = append(phrases, "whoever `"+strings.TrimPrefix(e, typedef.AllowedFieldPrefix)+"` names"), append(parts, WhoField)
			case e == typedef.AllowedPrincipalPrefix+typedef.TimerPrincipal:
				phrases, parts = append(phrases, "the timer"), append(parts, WhoTimer)
			case strings.HasPrefix(e, typedef.AllowedPrincipalPrefix+"hook:"):
				phrases, parts = append(phrases, "the hook `"+hookOf(e)+"`"), append(parts, WhoHook)
			default:
				phrases, parts = append(phrases, "`"+strings.TrimPrefix(e, typedef.AllowedPrincipalPrefix)+"`"), append(parts, WhoPrincipal)
			}
		}
	}
	subject := strings.Join(phrases, ", or ")
	var except []string
	if slices.Contains(tr.Except, typedef.AllowedCreator) {
		except = append(except, "it")
		parts = append(parts, ExceptCreator)
	}
	if slices.Contains(tr.Except, typedef.AllowedContainerCreator) {
		except = append(except, "its container")
		parts = append(parts, ExceptContainerCreatr)
	}
	if len(except) > 0 {
		subject += ", but never anyone who took part in creating " + joinOr(except)
	}
	if len(phrases) > 1 || len(except) > 0 {
		subject += ","
	}
	return subject, verb, parts
}

// hookOf is the hook's ID in principal:hook:<type>:<hook>.
func hookOf(entry string) string {
	return entry[strings.LastIndex(entry, ":")+1:]
}

// --- Reactions ---

// reaction says what a reaction on entering a state does.
func (r *renderer) reaction(ri int) {
	t, rx := r.t, r.t.Reactions[ri]
	path := []string{at("reactions[%d]", ri)}
	if rx.Kind == typedef.ReactionAfter {
		r.say([]Part{Undo}, path, at("On entering %s from %s, `%s` gives back what `%s` added.", rx.State, joinOr(rx.From), rx.ID, rx.Undoes))
		r.reactionHooks(rx.ID)
		return
	}
	var extra []Part
	prefix := "On entering " + rx.State
	if len(rx.From) > 0 {
		prefix += " from " + joinOr(rx.From)
		extra = append(extra, ReactionFrom)
	}
	if rx.When != "" {
		prefix += ", if " + r.ctx.condition(rx.When)
		extra = append(extra, ReactionWhen)
	}
	var kind Part
	var body string
	switch rx.Kind {
	case typedef.ReactionMove:
		kind, body = r.moveBody(rx, &extra)
	case typedef.ReactionCreate:
		kind = Create
		body = at("`%s` creates %s %s@%d inside it", rx.ID, article(rx.Type), rx.Type, rx.Version)
		if rx.ForEach != "" {
			body += at(", one for each value of `%s`", rx.ForEach)
			extra = append(extra, CreateForEach)
		}
		if len(rx.Fields) > 0 {
			body += ", with " + r.createValues(rx.Fields)
		}
	case typedef.ReactionSet:
		kind = Set
		var sets []string
		for k, f := range slices.Sorted(maps.Keys(rx.Set)) {
			v, _ := r.valueWords(r.ctx, rx.Set[f])
			if k == 0 {
				sets = append(sets, at("`%s` on its container to %s", f, v))
			} else {
				sets = append(sets, at("`%s` to %s", f, v))
			}
		}
		body = at("`%s` sets %s", rx.ID, joinAnd(sets))
	default:
		return
	}
	_ = t
	r.say(append([]Part{kind}, extra...), path, prefix+", "+body+".")
	r.reactionHooks(rx.ID)
}

// moveBody is what a move reaction moves: the Items inside, its
// container, or the Item a link points to.
func (r *renderer) moveBody(rx typedef.Reaction, extra *[]Part) (Part, string) {
	from := ""
	if len(rx.TargetFrom) > 0 {
		from = " from " + joinOr(rx.TargetFrom)
	}
	skip := rx.OnGuard == typedef.OnGuardSkip
	switch {
	case rx.Children != nil:
		body := at("`%s` moves the %s inside it", rx.ID, plural(rx.Children.Of))
		if rx.Children.When != "" {
			body += " whose " + r.ctx.condition(rx.Children.When)
			*extra = append(*extra, MoveChildrenWhen)
		}
		body += from + " to " + rx.To
		if skip {
			body += "; one whose rules refuse the move is skipped"
			*extra = append(*extra, ReactionSkip)
		}
		return MoveChildren, body
	case rx.Target == typedef.TargetContainer:
		body := at("`%s` moves its container%s to %s", rx.ID, from, rx.To)
		if skip {
			body += "; if its container's rules refuse the move, it is skipped"
			*extra = append(*extra, ReactionSkip)
		}
		return MoveContainer, body
	}
	target := rx.Link
	if l, ok := r.t.Link(rx.Link); ok {
		target = joinOr(l.Targets)
	}
	body := at("`%s` moves the %s it links to as `%s`%s to %s", rx.ID, target, rx.Link, from, rx.To)
	if skip {
		body += "; if its rules refuse the move, it is skipped"
		*extra = append(*extra, ReactionSkip)
	}
	return MoveLink, body
}

// createValues are a create reaction's fields: those in words, then those
// computed, with what they read.
func (r *renderer) createValues(fields map[string]string) string {
	var said, computed []string
	var reads []string
	for _, f := range slices.Sorted(maps.Keys(fields)) {
		v, ok, refs := r.ctx.value(fields[f])
		if ok {
			said = append(said, at("`%s` = %s", f, v))
			continue
		}
		computed = append(computed, "`"+f+"`")
		reads = append(reads, refs...)
	}
	if len(computed) == 0 {
		return joinAnd(said)
	}
	c := joinAnd(computed) + " computed from " + joinAnd(r.orderRefs(reads))
	if len(said) == 0 {
		return c
	}
	return strings.Join(said, ", ") + ", and " + c
}

// orderRefs puts the phrases of several expressions' reads in one order:
// fields first, then today, the time of the write and each value, then
// the rest; each once.
func (r *renderer) orderRefs(refs []string) []string {
	rank := func(p string) int {
		switch {
		case strings.HasPrefix(p, "`"):
			return 0
		case p == "today":
			return 1
		case p == r.ctx.causeAt:
			return 2
		case p == "each value":
			return 3
		}
		return 4
	}
	refs = slices.Clone(refs)
	slices.SortStableFunc(refs, func(a, b string) int {
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra - rb
		}
		if rank(a) == 0 || rank(a) == 4 {
			return strings.Compare(a, b)
		}
		return 0
	})
	return slices.Compact(refs)
}

func (r *renderer) reactionHooks(reaction string) {
	for hi, h := range r.t.Hooks {
		if h.On == typedef.OnReaction && h.Reaction == reaction {
			r.say([]Part{ReactionHook}, []string{at("hooks[%d]", hi)}, at("When `%s` has applied, `%s` calls %s.", reaction, h.ID, r.call(h)))
		}
	}
}

func (r *renderer) createdInside() {
	r.section("When something is created inside it")
	for ri, rx := range r.t.Reactions {
		if rx.On != typedef.OnCreatedInside {
			continue
		}
		var parts []Part
		by := "anyone"
		switch rx.By {
		case typedef.ByCreator:
			by, parts = "the requester", append(parts, CreatedByCreator)
		case typedef.ByNotCreator:
			by, parts = "a person other than the requester", append(parts, CreatedByOther)
		default:
			parts = append(parts, CreatedByAnyone)
		}
		text := at("When %s creates %s inside it", by, joinOr(withArticles(rx.Of)))
		if rx.When != "" {
			text += " whose " + r.ctx.condition(rx.When)
			parts = append(parts, ReactionWhen)
		}
		if len(rx.From) > 0 {
			text += " while it is in " + joinOr(rx.From)
			parts = append(parts, ReactionFrom)
		}
		kind := CreatedInsideSelf
		if rx.Children != nil {
			kind = CreatedInsideChildren
			body := at("`%s` moves the %s inside it", rx.ID, plural(rx.Children.Of))
			if rx.Children.When != "" {
				body += " whose " + r.ctx.condition(rx.Children.When)
				parts = append(parts, MoveChildrenWhen)
			}
			if len(rx.TargetFrom) > 0 {
				body += " from " + joinOr(rx.TargetFrom)
			}
			text += ", " + body + " to " + rx.To + "."
		} else {
			text += at(", `%s` moves it to %s.", rx.ID, rx.To)
		}
		r.say(append([]Part{kind}, parts...), []string{at("reactions[%d]", ri)}, text)
	}
}

// --- What other Types may do to it ---

func (r *renderer) others() {
	t := r.t
	r.section("What other Types may do to it")
	for _, key := range slices.Sorted(maps.Keys(t.AcceptsReactions)) {
		a := t.AcceptsReactions[key]
		ty, id, _ := strings.Cut(key, ":")
		var parts []Part
		var may []string
		if len(a.Fields) > 0 {
			may = append(may, "change "+joinAnd(ticked(slices.Sorted(slices.Values(a.Fields)))))
			parts = append(parts, ConsentAdd)
		}
		if len(a.Sets) > 0 {
			may = append(may, "set "+joinAnd(ticked(slices.Sorted(slices.Values(a.Sets)))))
			parts = append(parts, ConsentSet)
		}
		if a.MayCreate {
			c := "create it"
			if len(a.Creates) > 0 {
				c += ", setting " + joinAnd(ticked(slices.Sorted(slices.Values(a.Creates))))
			}
			may = append(may, c)
			parts = append(parts, ConsentCreate)
		}
		if len(a.Moves) > 0 {
			may = append(may, "move it to "+joinOr(a.Moves))
			parts = append(parts, ConsentMove)
		}
		if len(parts) == 0 {
			continue
		}
		who := ty + "'s reaction `" + id + "`"
		if ty == typedef.WildcardType {
			who = "Any Type's reaction `" + id + "`, from its container,"
			parts = append(parts, ConsentWildcard)
		}
		r.say(parts, []string{"accepts_reactions." + key}, who+" may "+joinGroups(may)+".")
	}
	if len(t.ReadableByReactions) > 0 {
		r.say([]Part{Readable}, []string{"readable_by_reactions"},
			"Another Type's create reaction may read "+joinAnd(ticked(t.ReadableByReactions))+" from it.")
	}
	for _, name := range slices.Sorted(maps.Keys(t.Offers)) {
		r.say([]Part{Offer}, []string{"offers." + name}, at("Items inside it may read `%s` as `%s`.", t.Offers[name], name))
	}
}

// --- Who sees it ---

func (r *renderer) whoSees() {
	t := r.t
	r.section("Who sees it")
	if len(t.CustomerFields) == 0 {
		r.say([]Part{CustomerFields, CustomerFieldsNone}, []string{"customer_fields"},
			"A customer who sees it sees none of its fields, only its header.")
	} else {
		which := "that"
		if len(t.CustomerFields) > 1 {
			which = "those"
		}
		r.say([]Part{CustomerFields}, []string{"customer_fields"},
			at("A customer who sees it sees only %s, and sets only %s when creating it.", joinAnd(ticked(t.CustomerFields)), which))
	}
	if len(t.Own) > 0 {
		r.say([]Part{Own}, []string{"own"},
			at("A customer may create %s inside it, and sees the ones they requested.", joinOr(withArticles(t.Own))))
	}
	if len(t.Shared) > 0 {
		r.say([]Part{Shared}, []string{"shared"},
			at("The customer who requested it sees every %s inside it, and may create one there.", joinOr(t.Shared)))
	}
	path := []string{"placement_writable_by"}
	switch {
	case len(t.PlacementWritableBy) == 0:
		r.say([]Part{PlacementAnyone}, path, "Anyone who can see it moves it to another container or narrows who sees it.")
	case slices.Contains(t.PlacementWritableBy, string(typedef.WritableByKernel)):
		r.say([]Part{PlacementKernel}, path, "No one moves it to another container or narrows who sees it.")
	default:
		var roles []string
		for _, e := range t.PlacementWritableBy {
			roles = append(roles, strings.TrimPrefix(e, typedef.AllowedRolePrefix))
		}
		r.say([]Part{PlacementRoles}, path, "Only holders of "+joinOr(roles)+" move it to another container or narrow who sees it.")
	}
}

// --- Hooks and actions ---

// hook finds a hook by ID, with its index.
func (r *renderer) hook(id string) (typedef.Hook, int, bool) {
	i := slices.IndexFunc(r.t.Hooks, func(h typedef.Hook) bool { return h.ID == id })
	if i < 0 {
		return typedef.Hook{}, -1, false
	}
	return r.t.Hooks[i], i, true
}

// actionName is a hook's action with its major: email.notify@1.
func actionName(a string) string {
	if strings.Contains(a, "@") {
		return a
	}
	return a + "@1"
}

// call is "`action@1` with input = what, ...": the hook's reads given to
// the contract's inputs, in order.
func (r *renderer) call(h typedef.Hook) string {
	name := actionName(h.Action)
	s := "`" + name + "`"
	if len(h.Reads) == 0 {
		return s
	}
	var names []string
	if r.cat != nil {
		if c, ok := r.cat.Lookup(name); ok {
			for _, v := range c.InputValues() {
				names = append(names, v.Name)
			}
		}
	}
	var ins []string
	for i, read := range h.Reads {
		if i < len(names) {
			ins = append(ins, names[i]+" = "+readWords(read))
		} else {
			ins = append(ins, readWords(read))
		}
	}
	return s + " with " + strings.Join(ins, ", ")
}

// readWords is what a hook reads, in words.
func readWords(read string) string {
	switch {
	case read == typedef.AttrCreator:
		return "the requester"
	case read == typedef.AttrCreatedBy:
		return "who created it"
	case read == typedef.AttrCreatedOnBehalfOf:
		return "who it was created for"
	case strings.HasPrefix(read, typedef.ContainerOfferPrefix):
		return "its container's `" + strings.TrimPrefix(read, typedef.ContainerOfferPrefix) + "`"
	}
	return "`" + read + "`"
}

// reasons are the reasons a check's action may deny with.
func (r *renderer) reasons(h typedef.Hook) []string {
	if r.cat == nil {
		return nil
	}
	c, ok := r.cat.Lookup(actionName(h.Action))
	if !ok {
		return nil
	}
	return c.Errors
}

func (r *renderer) actions() []ActionNote {
	var out []ActionNote
	seen := map[string]bool{}
	for _, h := range r.t.Hooks {
		name := actionName(h.Action)
		if seen[name] {
			continue
		}
		seen[name] = true
		note := ActionNote{Action: name}
		if r.cat != nil {
			if c, ok := r.cat.Lookup(name); ok {
				for _, k := range c.Kinds {
					note.Kinds = append(note.Kinds, string(k))
				}
				note.SideEffects = c.SideEffects == "some"
				note.Risk, note.Description = c.Risk, c.Description
			}
		}
		out = append(out, note)
	}
	return out
}

// --- Values ---

// valueWords is a value in words, or "a value computed from ...".
func (r *renderer) valueWords(c exprCtx, expr string) (string, bool) {
	v, ok, refs := c.value(expr)
	if ok {
		return v, true
	}
	return "a value computed from " + joinAnd(refs), false
}

// assignments are field = value effects: "`a` to X and `b` to Y", by field.
func assignments(c exprCtx, sets map[string]string) string {
	var out []string
	r := &renderer{ctx: c}
	for _, f := range slices.Sorted(maps.Keys(sets)) {
		v, _ := r.valueWords(c, sets[f])
		out = append(out, at("`%s` to %s", f, v))
	}
	return joinAnd(out)
}

// literal is a value written in a Type (a fallback): a word bare, text quoted.
func literal(v any) string {
	if s, ok := v.(string); ok {
		if bareWord.MatchString(s) {
			return s
		}
		return strconv.Quote(s)
	}
	return fmt.Sprint(v)
}

// fieldsOnly keeps the reads that are fields (a check also reads who
// created the Item, which no write changes).
func fieldsOnly(t *typedef.Type, reads []string) []string {
	var out []string
	for _, rd := range reads {
		if _, ok := t.Field(rd); ok {
			out = append(out, rd)
		}
	}
	return out
}

// --- Words ---

func ticked(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = "`" + id + "`"
	}
	return out
}

func article(id string) string {
	if id != "" && strings.ContainsRune("aeiou", rune(id[0])) {
		return "an"
	}
	return "a"
}

func withArticle(id string) string { return article(id) + " " + id }

func withArticles(ids []string) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = withArticle(id)
	}
	return out
}

// plural names Items of a Type, or of several: "sla_goals", "Items of
// story, bug or task".
func plural(ids []string) string {
	if len(ids) == 1 {
		return ids[0] + "s"
	}
	return "Items of " + joinOr(ids)
}

func times(n int) string {
	if n == 1 {
		return "once"
	}
	return strconv.Itoa(n) + " times"
}

// duration is a duration in words: "48 hours", "1 hour 30 minutes".
func duration(d time.Duration) string {
	var parts []string
	unit := func(n int64, one string) {
		if n == 0 {
			return
		}
		if n == 1 {
			parts = append(parts, "1 "+one)
		} else {
			parts = append(parts, fmt.Sprintf("%d %ss", n, one))
		}
	}
	unit(int64(d/time.Hour), "hour")
	unit(int64(d%time.Hour/time.Minute), "minute")
	unit(int64(d%time.Minute/time.Second), "second")
	if len(parts) == 0 {
		return "at once"
	}
	return strings.Join(parts, " ")
}

// --- The field list ---

var fieldWords = map[typedef.FieldType][2]string{
	typedef.String:    {"text", "texts"},
	typedef.Int:       {"a whole number", "whole numbers"},
	typedef.Bool:      {"yes or no", "yes-or-no values"},
	typedef.Date:      {"a date", "dates"},
	typedef.DateTime:  {"an instant", "instants"},
	typedef.Principal: {"a person", "people"},
}

func fieldList(t *typedef.Type) []string {
	var out []string
	for _, f := range t.Fields {
		var w string
		if f.Type == typedef.Enum {
			if f.Multiple {
				w = "values among " + joinOr(f.Options)
			} else {
				w = "one of " + joinOr(f.Options)
			}
		} else if words, ok := fieldWords[f.Type]; ok {
			w = words[0]
			if f.Multiple {
				w = words[1]
			}
		} else {
			w = string(f.Type)
		}
		if f.Multiple {
			if f.Max > 0 {
				w = fmt.Sprintf("a set of at most %d %s", f.Max, strings.TrimPrefix(w, "values "))
			} else {
				w = "a set of " + strings.TrimPrefix(w, "values ")
			}
		}
		out = append(out, "`"+f.ID+"`: "+w)
	}
	return out
}

// --- The key ---

var keyTerms = []struct {
	name, meaning string
	used          *regexp.Regexp
}{
	{"the requester", "the person an Item is for: whoever created it, or the person they created it for.", regexp.MustCompile(`requester`)},
	{"staff", "people in the organisation, and agents acting for them; a customer is never staff.", regexp.MustCompile(`\bstaff\b`)},
	{"a customer", "a person outside the organisation.", regexp.MustCompile(`customer`)},
	{"an agent", "a program acting for a person; it may do only what both it and that person may do, and a field that names a person never names an agent.",
		regexp.MustCompile(`\bstaff\b|\bagents?\b|` + "` names|with the role|holders of")},
	{"the kernel", "the system that runs the rules of every Type; it is never a person.", regexp.MustCompile(`\bkernel\b`)},
	{"the timer", "the kernel's clock; it makes only the moves a Type gives it.", regexp.MustCompile(`\bthe timer\b`)},
	{"a hold", "while a check or a reaction decides a move, the Item waits where it is; that wait is a hold.", regexp.MustCompile(`\b(a|the|that) hold\b`)}, // the noun, not the verb ("rules hold", "hold the same")
	{"a reaction", "a write a Type makes on another Item when something happens to one of its own; the other Item's Type must allow it.",
		regexp.MustCompile(`\breactions?\b`)},
}

// keyOf defines the terms the reading's sentences use, each once.
func keyOf(rd *Reading) []Term {
	var all strings.Builder
	for _, s := range rd.Sections {
		for _, sn := range s.Sentences {
			all.WriteString(sn.Text)
			all.WriteString("\n")
		}
	}
	text := all.String()
	var out []Term
	for _, k := range keyTerms {
		if k.used.MatchString(text) {
			out = append(out, Term{Name: k.name, Meaning: k.meaning})
		}
	}
	return out
}

// --- Text ---

var kindWords = map[string]string{"check": "a check", "enrich": "a lookup", "action": "an action"}

// Text is the reading as a person reads it: the heading, the key, the
// fields, the numbered sentences under their sections, the actions and
// what is not covered.
func (r *Reading) Text() string {
	var blocks []string
	blocks = append(blocks, r.Type)
	if len(r.Key) > 0 {
		lines := []string{"Key"}
		for _, k := range r.Key {
			lines = append(lines, "- "+k.Name+": "+k.Meaning)
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	if len(r.Fields) > 0 {
		blocks = append(blocks, "Fields\n- "+strings.Join(r.Fields, "\n- "))
	}
	for _, s := range r.Sections {
		lines := []string{s.Title}
		for _, sn := range s.Sentences {
			lines = append(lines, fmt.Sprintf("%d. %s", sn.N, sn.Text))
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	if len(r.Actions) > 0 {
		lines := []string{"Actions it calls"}
		for _, a := range r.Actions {
			var kinds []string
			for _, k := range a.Kinds {
				kinds = append(kinds, kindWords[k])
			}
			what := strings.Join(kinds, " or ")
			if a.SideEffects {
				what += " with side effects"
			}
			lines = append(lines, fmt.Sprintf("- `%s` (%s, %s risk): %s", a.Action, what, a.Risk, a.Description))
		}
		blocks = append(blocks, strings.Join(lines, "\n"))
	}
	blocks = append(blocks, "Not covered by this reading\n- "+strings.Join(r.NotCovered, "\n- "))
	return strings.Join(blocks, "\n\n") + "\n"
}

// waits says who a state waits on ("waiting on me", track 1 item 5): the
// people the moves out of it name, in the Type's order, each once, in the
// words a move's own sentence uses. The rule is policy.WaitingMoves, the
// one the item service's waiting_on filter reads, so a state whose moves
// name nobody, or in category done or canceled, has no such sentence.
func (r *renderer) waits(i int, s typedef.State) {
	// A claimed Item waits on its claimant, whoever its moves name.
	if s.Claim != "" && len(policy.WaitingMoves(r.t, s.ID)) > 0 {
		r.say([]Part{Waits, WaitsClaimant}, []string{at("states[%d].claim", i)},
			at("While it is in %s, it waits on whoever claimed it.", s.ID))
		return
	}
	var phrases, paths []string
	for _, tr := range policy.WaitingMoves(r.t, s.ID) {
		for ti, other := range r.t.Transitions {
			if other.From == tr.From && other.To == tr.To {
				paths = append(paths, at("transitions[%d]", ti))
			}
		}
		for _, e := range policy.NamedEntries(tr) {
			var phrase string
			switch {
			case strings.HasPrefix(e, typedef.AllowedRolePrefix):
				phrase = "anyone with the role " + strings.TrimPrefix(e, typedef.AllowedRolePrefix)
			case strings.HasPrefix(e, typedef.AllowedFieldPrefix):
				phrase = "whoever `" + strings.TrimPrefix(e, typedef.AllowedFieldPrefix) + "` names"
			default:
				phrase = "`" + strings.TrimPrefix(e, typedef.AllowedPrincipalPrefix) + "`"
			}
			if !slices.Contains(phrases, phrase) {
				phrases = append(phrases, phrase)
			}
		}
	}
	if len(phrases) == 0 {
		return
	}
	r.say([]Part{Waits}, paths, at("While it is in %s, it waits on %s.", s.ID, strings.Join(phrases, ", or ")))
}
