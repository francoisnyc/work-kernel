package check

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// maxNodes bounds one search. Beyond it the Type is UNCHECKED: the checker
// says it did not finish rather than report what it saw as all there is.
const maxNodes = 50000

// node is one abstract Item: its state, whether it sits in a container,
// and what is known of each field (in the Type's field order).
type node struct {
	state  string
	inside bool
	vals   []val
}

func (n node) key() string {
	var b strings.Builder
	b.WriteString(n.state)
	if n.inside {
		b.WriteString("|in|")
	} else {
		b.WriteString("|top|")
	}
	for _, v := range n.vals {
		b.WriteByte('0' + byte(v))
	}
	return b.String()
}

// visit is how a search first reached a node: from where, by what step,
// and what that step rests on.
type visit struct {
	parent string // "" for a create
	step   Step
	deps   []string
}

// cand is the best witness found so far for one finding.
type cand struct {
	steps []Step
	deps  []string
	world int
	extra string // STUCK_STATE: the fields that name no one
}

func (a cand) better(b cand) bool {
	if len(a.deps) != len(b.deps) {
		return len(a.deps) < len(b.deps)
	}
	if len(a.steps) != len(b.steps) {
		return len(a.steps) < len(b.steps)
	}
	return a.world < b.world
}

type fkey struct {
	code Code
	path string
}

// typeCheck checks one Type version, with the others it is checked with.
type typeCheck struct {
	ty     *typedef.Type
	cat    *contracts.Catalog
	all    []*typedef.Type
	idx    map[string]int
	worlds []world
	// The Type's own Items are made only by a reaction, inside a
	// container whose creator is their requester.
	byReaction bool
	decision   map[string]bool
	found      map[fkey]cand
	unchecked  []Finding
	stuckMemo  map[string]bool
	allMovers  []struct {
		m mover
		w world
	}
}

func newTypeCheck(ty *typedef.Type, all []*typedef.Type, cat *contracts.Catalog) *typeCheck {
	c := &typeCheck{ty: ty, cat: cat, all: all, idx: map[string]int{},
		decision: map[string]bool{}, found: map[fkey]cand{}, stuckMemo: map[string]bool{},
		byReaction: ty.Start.Nobody()}
	for i, f := range ty.Fields {
		c.idx[f.ID] = i
	}
	// A decision state: done or canceled, and some move into it names
	// who may make it.
	for _, tr := range ty.Transitions {
		if st, ok := ty.State(tr.To); ok && final(st) && len(tr.Allowed) > 0 {
			c.decision[tr.To] = true
		}
	}
	rs := c.roles()
	opts := append([]string{""}, rs...)
	for _, req := range opts {
		for _, col := range opts {
			for _, ag := range opts {
				for _, f := range []int{forSelf, forRequester, forColleague} {
					c.worlds = append(c.worlds, world{req, col, ag, f})
				}
			}
		}
	}
	slices.SortStableFunc(c.worlds, func(a, b world) int { return a.roleCount() - b.roleCount() })
	// Every mover of every world, once each, for asking whether anyone
	// at all may leave a state.
	for _, r := range opts {
		c.allMovers = append(c.allMovers,
			struct {
				m mover
				w world
			}{mover{kind: mRequester}, world{req: r}},
			struct {
				m mover
				w world
			}{mover{kind: mColleague}, world{col: r}},
			struct {
				m mover
				w world
			}{mover{kind: mAgent}, world{agent: r}})
	}
	return c
}

func final(st typedef.State) bool { return st.Category == "done" || st.Category == "canceled" }

// roles are the roles the Type names anywhere a role decides who acts or
// writes, sorted.
func (c *typeCheck) roles() []string {
	set := map[string]bool{}
	addEntries := func(entries []string) {
		for _, e := range entries {
			if r, ok := strings.CutPrefix(e, typedef.AllowedRolePrefix); ok && r != "" {
				set[r] = true
			}
		}
	}
	for _, tr := range c.ty.Transitions {
		addEntries(tr.Allowed)
	}
	for _, f := range c.ty.Fields {
		for _, r := range f.WritableByRoles {
			set[r] = true
		}
	}
	if c.ty.Start != nil {
		addEntries(c.ty.Start.Allowed)
	}
	return sortedKeys(set)
}

// validate reports, as UNCHECKED, what the checker cannot read: an
// allowed or except entry of a form it does not know.
func (c *typeCheck) validate() {
	for ti, tr := range c.ty.Transitions {
		for ei, e := range tr.Allowed {
			if !knownAllowed(e) {
				c.unchecked = append(c.unchecked, c.uncheckedFinding(fmt.Sprintf("transitions[%d].allowed[%d]", ti, ei),
					fmt.Sprintf("pli check does not know the allowed entry %q: who may make this move is not checked.", e)))
			}
		}
		for ei, e := range tr.Except {
			if e != typedef.AllowedCreator && e != typedef.AllowedContainerCreator {
				c.unchecked = append(c.unchecked, c.uncheckedFinding(fmt.Sprintf("transitions[%d].except[%d]", ti, ei),
					fmt.Sprintf("pli check does not know the except entry %q: who may make this move is not checked.", e)))
			}
		}
	}
}

func knownAllowed(e string) bool {
	switch e {
	case typedef.AllowedCreator, typedef.AllowedNobody, typedef.AllowedContainerCreator:
		return true
	}
	for _, p := range []string{typedef.AllowedRolePrefix, typedef.AllowedPrincipalPrefix, typedef.AllowedFieldPrefix} {
		if rest, ok := strings.CutPrefix(e, p); ok && rest != "" {
			return true
		}
	}
	return false
}

func (c *typeCheck) uncheckedFinding(path, msg string) Finding {
	return Finding{Type: c.ty.ID, Version: c.ty.Version, Code: CodeUnchecked, Severity: SeverityError,
		Path: path, Message: msg, DependsOn: []string{}, Witness: []Step{}}
}

func (c *typeCheck) field(id string) (typedef.Field, int, bool) {
	i, ok := c.idx[id]
	if !ok {
		return typedef.Field{}, -1, false
	}
	return c.ty.Fields[i], i, true
}

func kernelOnly(f typedef.Field) bool { return f.WritableBy == typedef.WritableByKernel }

// facts is the Item as policy reads it: the principals its fields name,
// who created it and its container.
func (c *typeCheck) facts(n node) policy.ItemFacts {
	fields := map[string]any{}
	for i, f := range c.ty.Fields {
		var id string
		switch n.vals[i] {
		case vRequester:
			id = requesterID
		case vColleague:
			id = colleagueID
		case vAgent:
			id = agentID
		case vNamed:
			id = namedPrefix + f.ID
		case vSet:
			fields[f.ID] = samples(f)[0]
			continue
		default:
			continue
		}
		if f.Multiple {
			fields[f.ID] = []any{id}
		} else {
			fields[f.ID] = id
		}
	}
	facts := policy.ItemFacts{Fields: fields, CreatedBy: requesterID}
	if n.inside {
		facts.ContainerCreatedBy = containerID
		if c.byReaction {
			facts.ContainerCreatedBy = requesterID
		}
	}
	return facts
}

// mayWrite answers, from policy and lifecycle, whether who may write the
// field while the Item is in its state.
func (c *typeCheck) mayWrite(n node, f typedef.Field, who policy.Who, facts policy.ItemFacts) bool {
	if kernelOnly(f) || lifecycle.CheckFrozen(c.ty, n.state, []string{f.ID}) != nil {
		return false
	}
	st, _ := c.ty.State(n.state)
	return policy.MayWriteField(f, who) && policy.MayWriteInState(st, f.ID, who, facts)
}

// editable is the set of fields some class of the world may write while
// the Item is in its state: their value can be anything when a move is
// asked for.
func (c *typeCheck) editable(n node, ws ...world) map[string]bool {
	facts := c.facts(n)
	ed := map[string]bool{}
	for _, f := range c.ty.Fields {
		for _, w := range ws {
			for _, m := range []mover{{kind: mRequester}, {kind: mColleague}, {kind: mAgent}} {
				if c.mayWrite(n, f, m.who(w), facts) {
					ed[f.ID] = true
				}
			}
		}
	}
	return ed
}

// tried is the outcome of asking whether a mover may make a move.
type tried struct {
	ok     bool
	intent bool // let in by name: creator, for the requester
	deps   []string
	next   node
}

// try asks whether m may make the move ti from n, and where it leaves the
// Item. Who may move is policy's answer, for the field values the move
// may meet; what the move does is the Type's (sets, clears, hooks, the
// claim) and the invariants lifecycle judges.
func (c *typeCheck) try(n node, w world, ed map[string]bool, ti int, m mover) tried {
	tr := c.ty.Transitions[ti]
	st, _ := c.ty.State(n.state)
	if m.kind == mNamed && !slices.Contains(tr.Allowed, typedef.AllowedFieldPrefix+m.field) {
		return tried{}
	}
	who := m.who(w)
	base := c.facts(n)

	// The values the move may meet: as the Item holds them, then, for
	// each field: entry, the field naming the mover when it may.
	type variant struct {
		facts policy.ItemFacts
		dep   string
	}
	variants := []variant{{facts: base}}
	for _, e := range tr.Allowed {
		id, ok := strings.CutPrefix(e, typedef.AllowedFieldPrefix)
		if !ok {
			continue
		}
		f, i, ok := c.field(id)
		if !ok || f.Type != typedef.Principal {
			continue
		}
		v := n.vals[i]
		var name, dep string
		switch {
		case kernelOnly(f):
			// A person the kernel chose (never an agent): whoever the
			// field names, or perhaps the requester.
			switch {
			case v == vAny && m.kind == mNamed && m.field == id:
				name = namedPrefix + id
			case (v == vAny || v == vNamed) && m.kind == mRequester:
				name, dep = requesterID, typedef.AllowedFieldPrefix+id
			}
		case v == vAny || ed[id]:
			if m.kind == mRequester || m.kind == mColleague || m.kind == mAgent {
				name, dep = who.Actor, typedef.AllowedFieldPrefix+id
			}
		}
		if name == "" {
			continue
		}
		alt := base
		alt.Fields = make(map[string]any, len(base.Fields)+1)
		for k, x := range base.Fields {
			alt.Fields[k] = x
		}
		if f.Multiple {
			alt.Fields[id] = []any{name}
		} else {
			alt.Fields[id] = name
		}
		variants = append(variants, variant{alt, dep})
	}
	var facts policy.ItemFacts
	var deps []string
	ok := false
	for _, v := range variants {
		if policy.MayMove(st, tr, who, v.facts) {
			facts, ok = v.facts, true
			deps = addDeps(nil, v.dep)
			break
		}
	}
	if !ok {
		return tried{}
	}
	intent := slices.Contains(tr.Allowed, typedef.AllowedCreator) &&
		policy.IsRequester(who.Actor, who.OnBehalfOf, facts.CreatedBy, facts.CreatedOnBehalfOf)

	next := node{state: tr.To, inside: n.inside, vals: slices.Clone(n.vals)}
	gives := func(id string) bool {
		if m.kind == mKernel {
			return true
		}
		f, _, ok := c.field(id)
		return ok && c.mayWrite(n, f, who, facts)
	}
	for _, id := range tr.Given {
		_, i, ok := c.field(id)
		if !ok || !gives(id) {
			return tried{}
		}
		next.vals[i] = setTo(c.ty.Fields[i])
	}
	for _, id := range tr.Required {
		_, i, ok := c.field(id)
		if !ok {
			return tried{}
		}
		switch {
		case next.vals[i] == vAny:
			next.vals[i] = setTo(c.ty.Fields[i]) // required: not empty after the move
		case next.vals[i] != vEmpty:
		case ed[id] || gives(id):
			next.vals[i] = setTo(c.ty.Fields[i])
		default:
			return tried{}
		}
	}
	if !c.effects(n, &next, &tr, m, who, ed) {
		return tried{}
	}
	return tried{ok: true, intent: intent, deps: deps, next: next}
}

// setTo is what a field holds once a write has set it: a value, or for a
// principal field someone not known.
func setTo(f typedef.Field) val {
	if f.Type == typedef.Principal {
		return vAny
	}
	return vSet
}

// effects applies what a move into next.state does besides the state:
// its check hook's answer, its sets and clears, the claim, the action and
// enrich hooks of the state it enters; and asks lifecycle whether the
// invariants can still hold. tr is nil for a timer or a reaction with no
// declared transition.
func (c *typeCheck) effects(n node, next *node, tr *typedef.Transition, m mover, who policy.Who, ed map[string]bool) bool {
	if tr != nil {
		if h, ok := lifecycle.CheckFor(c.ty, n.state, next.state); ok {
			c.hookWrites(h, next)
		}
		for _, id := range sortedKeys(tr.Sets) {
			if f, i, ok := c.field(id); ok {
				next.vals[i] = c.setVal(tr.Sets[id], f, n, next)
			}
		}
		for _, id := range tr.Clears {
			if _, i, ok := c.field(id); ok {
				next.vals[i] = vEmpty
			}
		}
	}
	from, _ := c.ty.State(n.state)
	to, _ := c.ty.State(next.state)
	if to.Claim != "" && from.Claim != to.Claim && m.kind != mKernel {
		if _, i, ok := c.field(to.Claim); ok {
			next.vals[i] = valOf(effective(who))
		}
	}
	for _, h := range append(lifecycle.EnrichHooks(c.ty, next.state), lifecycle.ActionHooks(c.ty, next.state)...) {
		for _, id := range h.Writes {
			if _, i, ok := c.field(id); ok {
				next.vals[i] = vAny
			}
		}
	}
	return c.mayHold(next.vals, ed)
}

// hookWrites sets the fields a check hook writes from its answer: never
// empty unless the action's contract lets that output be null.
func (c *typeCheck) hookWrites(h typedef.Hook, next *node) {
	var outs []contracts.Value
	if ct, ok := c.cat.Lookup(h.Action); ok {
		outs = ct.OutputValues()
	}
	for j, id := range h.Writes {
		f, i, ok := c.field(id)
		if !ok {
			continue
		}
		switch {
		case j >= len(outs) || outs[j].Nullable:
			next.vals[i] = vAny
		case f.Type == typedef.Principal:
			next.vals[i] = vNamed
		default:
			next.vals[i] = vSet
		}
	}
}

var ident = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// setVal is what a sets expression leaves in a field: the time of the
// write is set; a container's field is empty at the top level and not
// known inside; a field copied is what it held; anything else is not
// known.
func (c *typeCheck) setVal(expr string, f typedef.Field, n node, next *node) val {
	e := strings.TrimSpace(expr)
	switch {
	case e == "cause.at" || e == "today":
		return vSet
	case e == "null":
		return vEmpty
	}
	if rest, ok := strings.CutPrefix(e, "container."); ok && ident.MatchString(rest) {
		if !n.inside {
			return vEmpty
		}
		return vAny
	}
	if ident.MatchString(e) {
		if src, i, ok := c.field(e); ok && src.Type == f.Type {
			return next.vals[i]
		}
	}
	return vAny
}

// mayHold asks lifecycle whether some values the fields may hold keep
// every invariant: a field that is set or not known is tried with sample
// values of its type. Fields others may still write (ed) count as not
// known. Too many combinations to try counts as "may hold".
func (c *typeCheck) mayHold(vals []val, ed map[string]bool) bool {
	if len(c.ty.Invariants) == 0 {
		return true
	}
	var rules strings.Builder
	for _, inv := range c.ty.Invariants {
		rules.WriteString(inv.Rule)
		rules.WriteByte(' ')
	}
	type choice struct {
		id   string
		opts []any
	}
	var choices []choice
	combos := 1
	for i, f := range c.ty.Fields {
		if !regexp.MustCompile(`\b` + regexp.QuoteMeta(f.ID) + `\b`).MatchString(rules.String()) {
			continue
		}
		var opts []any
		v := vals[i]
		if ed[f.ID] {
			v = vAny
		}
		switch v {
		case vEmpty:
			opts = []any{nil}
		case vSet:
			opts = samples(f)
		case vAny:
			opts = append([]any{nil}, samples(f)...)
		case vRequester:
			opts = []any{requesterID}
		case vColleague:
			opts = []any{colleagueID}
		case vAgent:
			opts = []any{agentID}
		case vNamed:
			opts = []any{namedPrefix + f.ID}
		}
		if f.Type == typedef.Principal && v != vEmpty && f.Multiple {
			for k, o := range opts {
				if o != nil {
					opts[k] = []any{o}
				}
			}
		}
		combos *= len(opts)
		if combos > 4096 {
			return true
		}
		choices = append(choices, choice{f.ID, opts})
	}
	fields := map[string]any{}
	var try func(k int) bool
	try = func(k int) bool {
		if k == len(choices) {
			return lifecycle.CheckInvariants(c.ty, fields) == nil
		}
		for _, o := range choices[k].opts {
			if o == nil {
				delete(fields, choices[k].id)
			} else {
				fields[choices[k].id] = o
			}
			if try(k + 1) {
				return true
			}
		}
		return false
	}
	return try(0)
}

// movers are who may be asked to make the move: the classes, whoever a
// field: entry's field names, and the kernel's actors the move names.
func (c *typeCheck) movers(tr typedef.Transition) []mover {
	ms := []mover{{kind: mRequester}, {kind: mColleague}}
	for _, e := range tr.Allowed {
		if id, ok := strings.CutPrefix(e, typedef.AllowedFieldPrefix); ok {
			ms = append(ms, mover{kind: mNamed, field: id})
		}
	}
	ms = append(ms, mover{kind: mAgent})
	for _, e := range tr.Allowed {
		if p, ok := strings.CutPrefix(e, typedef.AllowedPrincipalPrefix); ok && item.IsKernelActor(p) {
			ms = append(ms, mover{kind: mKernel, id: p})
		}
	}
	return ms
}

// creates are the ways an Item of the Type comes to be, in one world:
// made by the requester in a container or at the top level, as start
// lets them, or by a reaction.
func (c *typeCheck) creates(w world) []struct {
	n    node
	step Step
	deps []string
} {
	type create = struct {
		n    node
		step Step
		deps []string
	}
	init := c.ty.InitialState
	var out []create
	if c.byReaction {
		vals := make([]val, len(c.ty.Fields))
		for i := range vals {
			vals[i] = vAny
		}
		n := node{state: init, inside: true, vals: vals}
		if c.mayHold(vals, nil) {
			out = append(out, create{n, Step{Who: "a reaction", Kind: StepCreate, To: init,
				Words: "A reaction creates it in " + init + ", inside a container."}, nil})
		}
		return out
	}
	requester := mover{kind: mRequester}
	who := requester.who(w)
	s := c.ty.Start
	var places []bool
	if s == nil || len(s.In) == 0 || slices.ContainsFunc(s.In, func(t string) bool { return t != typedef.StartTop }) {
		places = append(places, true)
	}
	if s.Admits("") {
		places = append(places, false)
	}
	for _, inside := range places {
		cf := policy.ContainerFacts{}
		if inside {
			cf.CreatedBy = containerID
		}
		if !policy.MayStart(s, who, cf) {
			continue
		}
		n := node{state: init, inside: inside, vals: make([]val, len(c.ty.Fields))}
		facts := c.facts(n)
		for i, f := range c.ty.Fields {
			if c.mayWrite(n, f, who, facts) {
				n.vals[i] = vAny
			}
		}
		if !c.mayHold(n.vals, nil) {
			continue
		}
		words := capitalize(requester.words(w)) + " creates it in " + init
		if !inside {
			words += ", outside any container"
		}
		out = append(out, create{n, Step{Who: requester.words(w), Kind: StepCreate, To: init, Words: words + "."},
			requester.roleDeps(w)})
	}
	return out
}

// crossExit is a reaction, of this Type or another checked with it, that
// moves an Item of this Type from a state, through a declared transition.
type crossExit struct {
	who        string
	transition int
}

func (c *typeCheck) crossExits(state string) []crossExit {
	var out []crossExit
	for _, u := range c.all {
		for _, r := range u.Reactions {
			if r.Kind != typedef.ReactionMove {
				continue
			}
			targets := false
			switch {
			case r.Children != nil:
				targets = slices.Contains(r.Children.Of, c.ty.ID)
			case r.Target == typedef.TargetContainer:
				targets = u.Start == nil || len(u.Start.In) == 0 || slices.Contains(u.Start.In, c.ty.ID)
			case r.Link != "":
				if l, ok := u.Link(r.Link); ok {
					targets = slices.Contains(l.Targets, c.ty.ID)
				}
			}
			if !targets || (len(r.TargetFrom) > 0 && !slices.Contains(r.TargetFrom, state)) {
				continue
			}
			for ti, tr := range c.ty.Transitions {
				if tr.From == state && tr.To == r.To {
					who := "reaction `" + r.ID + "`"
					if u != c.ty {
						who += fmt.Sprintf(" of %s@%d", u.ID, u.Version)
					}
					out = append(out, crossExit{who, ti})
					break
				}
			}
		}
	}
	return out
}

// kernelMove moves n to `to` for the timer or a reaction, through the
// declared transition if there is one: the move's effects apply, its
// allowed does not (the kernel's own actors, and reactions by consent).
func (c *typeCheck) kernelMove(n node, to string, ed map[string]bool) (node, bool) {
	next := node{state: to, inside: n.inside, vals: slices.Clone(n.vals)}
	var tr *typedef.Transition
	for i := range c.ty.Transitions {
		if c.ty.Transitions[i].From == n.state && c.ty.Transitions[i].To == to {
			tr = &c.ty.Transitions[i]
			break
		}
	}
	m := mover{kind: mKernel, id: "system:timer"}
	return next, c.effects(n, &next, tr, m, m.who(world{}), ed)
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (c *typeCheck) moveWords(who string, tr typedef.Transition) string {
	s := capitalize(who) + " moves it from " + tr.From + " to " + tr.To
	if tr.Guard != "" {
		s += ", if " + reading.Condition(c.ty, tr.Guard)
	}
	return s + "."
}

// search explores one world breadth first, so the first path to a node is
// a shortest one, and records every finding it meets.
func (c *typeCheck) search(wi int) bool {
	w := c.worlds[wi]
	seen := map[string]visit{}
	nodes := map[string]node{}
	var queue []string
	push := func(n node, v visit) {
		k := n.key()
		if _, ok := seen[k]; ok {
			return
		}
		seen[k], nodes[k] = v, n
		queue = append(queue, k)
	}
	path := func(k string) ([]Step, []string) {
		var steps []Step
		var chain [][]string
		for k != "" {
			v := seen[k]
			steps = append(steps, v.step)
			chain = append(chain, v.deps)
			k = v.parent
		}
		slices.Reverse(steps)
		slices.Reverse(chain)
		var deps []string
		for _, d := range chain {
			deps = addDeps(deps, d...)
		}
		return steps, deps
	}
	record := func(fk fkey, steps []Step, deps []string, extra string) {
		cd := cand{steps: steps, deps: deps, world: wi, extra: extra}
		if old, ok := c.found[fk]; !ok || cd.better(old) {
			c.found[fk] = cd
		}
	}

	for _, cr := range c.creates(w) {
		push(cr.n, visit{step: cr.step, deps: cr.deps})
	}
	for len(queue) > 0 {
		if len(seen) > maxNodes {
			return false
		}
		k := queue[0]
		queue = queue[1:]
		n := nodes[k]
		st, _ := c.ty.State(n.state)
		if !final(st) {
			if stuck, empty := c.stuck(n); stuck {
				steps, deps := path(k)
				record(fkey{CodeStuckState, fmt.Sprintf("states[%d]", c.stateIndex(n.state))}, steps, deps, empty)
			}
		}
		ed := c.editable(n, w)
		for _, ex := range lifecycle.Exits(c.ty, n.state) {
			switch ex.Kind {
			case lifecycle.ExitMove:
				tr := c.ty.Transitions[ex.Transition]
				for _, m := range c.movers(tr) {
					t := c.try(n, w, ed, ex.Transition, m)
					if !t.ok {
						continue
					}
					stepDeps := addDeps(nil, guardDep(tr))
					stepDeps = addDeps(stepDeps, m.roleDeps(w)...)
					stepDeps = addDeps(stepDeps, t.deps...)
					step := Step{Who: m.words(w), Kind: StepMove, From: tr.From, To: tr.To, Words: c.moveWords(m.words(w), tr)}
					if c.decision[tr.To] && !t.intent && (m.kind == mAgent || m.kind == mRequester) {
						steps, deps := path(k)
						code := CodeAgentDecides
						if m.kind == mRequester {
							code = CodeRequesterDecides
						}
						record(fkey{code, fmt.Sprintf("transitions[%d]", ex.Transition)},
							append(steps, step), addDeps(deps, stepDeps...), "")
					}
					push(t.next, visit{parent: k, step: step, deps: stepDeps})
				}
			case lifecycle.ExitTimer, lifecycle.ExitAt, lifecycle.ExitReaction:
				if ex.Kind == lifecycle.ExitAt {
					if _, i, ok := c.field(st.At.Field); ok && n.vals[i] == vEmpty && !ed[st.At.Field] {
						continue
					}
				}
				next, ok := c.kernelMove(n, ex.To, ed)
				if !ok {
					continue
				}
				who, words := "the timer", "The timer moves it from "+n.state+" to "+ex.To+"."
				kind := StepTimer
				switch ex.Kind {
				case lifecycle.ExitAt:
					kind, words = StepAt, "At the time in `"+st.At.Field+"`, the timer moves it from "+n.state+" to "+ex.To+"."
				case lifecycle.ExitReaction:
					kind, who = StepReaction, "reaction `"+ex.Reaction+"`"
					words = "Reaction `" + ex.Reaction + "` moves it from " + n.state + " to " + ex.To + "."
				}
				push(next, visit{parent: k, step: Step{Who: who, Kind: kind, From: n.state, To: ex.To, Words: words}})
			}
		}
		for _, cx := range c.crossExits(n.state) {
			tr := c.ty.Transitions[cx.transition]
			next, ok := c.kernelMove(n, tr.To, ed)
			if !ok {
				continue
			}
			push(next, visit{parent: k, step: Step{Who: cx.who, Kind: StepReaction, From: tr.From, To: tr.To,
				Words: capitalize(cx.who) + " moves it from " + tr.From + " to " + tr.To + "."}, deps: addDeps(nil, guardDep(tr))})
		}
	}
	return true
}

func guardDep(tr typedef.Transition) string {
	if tr.Guard == "" {
		return ""
	}
	return "guard:" + tr.Guard
}

func (c *typeCheck) stateIndex(id string) int {
	return slices.IndexFunc(c.ty.States, func(s typedef.State) bool { return s.ID == id })
}

// stuck reports whether no one, no timer and no reaction can move the
// Item out of n, whoever holds which role; and the fields that a move out
// reads to know who may make it, and that name no one.
func (c *typeCheck) stuck(n node) (bool, string) {
	k := n.key()
	if s, ok := c.stuckMemo[k]; ok && !s {
		return false, ""
	}
	var ws []world
	for _, am := range c.allMovers {
		ws = append(ws, am.w)
	}
	ed := c.editable(n, ws...)
	st, _ := c.ty.State(n.state)
	can := func() bool {
		if len(c.crossExits(n.state)) > 0 {
			return true
		}
		for _, ex := range lifecycle.Exits(c.ty, n.state) {
			switch ex.Kind {
			case lifecycle.ExitTimer, lifecycle.ExitReaction:
				if _, ok := c.kernelMove(n, ex.To, ed); ok {
					return true
				}
			case lifecycle.ExitAt:
				if _, i, ok := c.field(st.At.Field); ok && (n.vals[i] != vEmpty || ed[st.At.Field]) {
					if _, ok := c.kernelMove(n, ex.To, ed); ok {
						return true
					}
				}
			case lifecycle.ExitMove:
				tr := c.ty.Transitions[ex.Transition]
				for _, am := range c.allMovers {
					if c.try(n, am.w, ed, ex.Transition, am.m).ok {
						return true
					}
				}
				for _, m := range c.movers(tr) {
					if m.kind == mNamed || m.kind == mKernel {
						if c.try(n, world{}, ed, ex.Transition, m).ok {
							return true
						}
					}
				}
			}
		}
		return false
	}()
	c.stuckMemo[k] = !can
	if can {
		return false, ""
	}
	var empty []string
	for _, ex := range lifecycle.Exits(c.ty, n.state) {
		if ex.Kind != lifecycle.ExitMove {
			continue
		}
		for _, e := range c.ty.Transitions[ex.Transition].Allowed {
			if id, ok := strings.CutPrefix(e, typedef.AllowedFieldPrefix); ok {
				if _, i, ok := c.field(id); ok && n.vals[i] == vEmpty && !slices.Contains(empty, id) {
					empty = append(empty, id)
				}
			}
		}
	}
	return true, strings.Join(empty, ",")
}

// run checks the Type in every world and returns its findings.
func (c *typeCheck) run() []Finding {
	c.validate()
	out := append([]Finding{}, c.unchecked...)
	for wi := range c.worlds {
		if !c.search(wi) {
			out = append(out, c.uncheckedFinding("",
				fmt.Sprintf("more than %d abstract Items to explore: pli check stopped and checked nothing more of it.", maxNodes)))
			break
		}
	}
	for _, fk := range sortedFKeys(c.found) {
		cd := c.found[fk]
		f := Finding{Type: c.ty.ID, Version: c.ty.Version, Code: fk.code, Path: fk.path,
			DependsOn: append([]string{}, cd.deps...), Witness: cd.steps, Severity: SeverityWarning}
		if len(cd.deps) == 0 {
			f.Severity = SeverityError
		}
		last := cd.steps[len(cd.steps)-1]
		switch fk.code {
		case CodeStuckState:
			f.Message = "It can stay in " + last.To + " for ever: no one may move it on, and no timer or reaction does"
			if cd.extra != "" {
				ids := strings.Split(cd.extra, ",")
				verb := "names"
				if len(ids) > 1 {
					verb = "name"
				}
				f.Message += " (`" + strings.Join(ids, "` and `") + "` " + verb + " no one)"
			}
			f.Message += "."
		case CodeAgentDecides:
			f.Message = capitalize(last.Who) + " may move it from " + last.From + " to " + last.To + ", a decision."
		case CodeRequesterDecides:
			f.Message = capitalize(last.Who) + " may move it from " + last.From + " to " + last.To + ", a decision on their own request."
		}
		out = append(out, f)
	}
	return out
}

func sortedFKeys(m map[fkey]cand) []fkey {
	keys := make([]fkey, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b fkey) int {
		if a.path != b.path {
			return strings.Compare(a.path, b.path)
		}
		return strings.Compare(string(a.code), string(b.code))
	})
	return keys
}
