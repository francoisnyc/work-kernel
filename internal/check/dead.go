package check

import (
	"fmt"
	"slices"
	"strings"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// PR 2's checks, over the graph every world's search built: what no Item
// can ever reach or do (UNREACHABLE_STATE, NEVER_FIRES), a loop with no
// end (NO_WAY_OUT), and a colleague editing the requester's draft
// (COLLEAGUE_EDITS_DRAFT). "It can never happen" holds only because the
// searches took every guard as true and included the world where each
// class holds every role.

// deadAndDrafts records PR 2's findings in c.found. It reports false when
// judging a loop went past maxNodes.
func (c *typeCheck) deadAndDrafts() bool {
	reached := map[string]bool{}
	for _, s := range c.nodeState {
		reached[s] = true
	}
	c.unreachableAndNeverFires(reached)
	if !c.noWayOut() {
		return false
	}
	c.drafts()
	return true
}

func (c *typeCheck) dead(path, msg string) {
	c.found[fkey{CodeNeverFires, path}] = cand{message: msg}
}

func (c *typeCheck) unreachableAndNeverFires(reached map[string]bool) {
	// entered reports whether some Item enters state (from one of from;
	// none: from anywhere, a create into the initial state included).
	entered := func(state string, from []string) bool {
		if len(from) == 0 && state == c.ty.InitialState && reached[state] {
			return true
		}
		for p := range c.firedPair {
			a, b, _ := strings.Cut(p, ">")
			if b == state && (len(from) == 0 || slices.Contains(from, a)) {
				return true
			}
		}
		return false
	}
	noItemIn := func(s string) string { return "no Item can be in " + s + "." }
	for i, st := range c.ty.States {
		if !reached[st.ID] {
			c.found[fkey{CodeUnreachableState, fmt.Sprintf("states[%d]", i)}] = cand{
				message: "No Item can ever be in " + st.ID + ": nothing leads there, whoever holds which role and whatever the guards say."}
		}
		why := noItemIn(st.ID)
		if reached[st.ID] {
			why = "the move it makes is never allowed."
		}
		if st.Timer != nil && !c.firedPair[st.ID+">"+st.Timer.To] {
			c.dead(fmt.Sprintf("states[%d].timer", i), "The timer of "+st.ID+" never fires: "+why)
		}
		if st.At != nil && !c.firedPair[st.ID+">"+st.At.To] {
			c.dead(fmt.Sprintf("states[%d].at", i), "The timer at `"+st.At.Field+"` in "+st.ID+" never fires: "+why)
		}
	}
	for i, tr := range c.ty.Transitions {
		if c.firedPair[tr.From+">"+tr.To] {
			continue
		}
		why := noItemIn(tr.From)
		if reached[tr.From] {
			why = "no one may make it, and no reaction does."
		}
		c.dead(fmt.Sprintf("transitions[%d]", i), "The move from "+tr.From+" to "+tr.To+" never happens: "+why)
	}
	reactionRuns := map[string]bool{}
	for i, r := range c.ty.Reactions {
		var ok bool
		why := "no Item can enter " + r.State + "."
		if r.On == typedef.OnCreatedInside {
			ok = (len(r.From) == 0 && len(reached) > 0) || slices.ContainsFunc(r.From, func(s string) bool { return reached[s] })
			if len(r.From) > 0 {
				why = noItemIn(strings.Join(r.From, " or "))
			}
		} else {
			ok = entered(r.State, r.From)
		}
		reactionRuns[r.ID] = ok
		if !ok {
			c.dead(fmt.Sprintf("reactions[%d]", i), "Reaction `"+r.ID+"` never runs: "+why)
		}
	}
	for i, h := range c.ty.Hooks {
		ok, why := true, ""
		switch h.On {
		case typedef.OnEnter:
			ok, why = entered(h.State, nil), "no Item can enter "+h.State+"."
		case typedef.OnTransition:
			ok, why = c.firedPair[h.From+">"+h.To], "the move from "+h.From+" to "+h.To+" never happens."
		case typedef.OnReaction:
			ok, why = reactionRuns[h.Reaction], "reaction `"+h.Reaction+"` never runs."
		case typedef.OnTimer:
			// An acting timer's hook runs while its state is held.
			for _, st := range c.ty.States {
				for _, at := range st.Acting {
					if at.Hook == h.ID && !reached[st.ID] {
						ok, why = false, noItemIn(st.ID)
					}
				}
			}
		}
		if !ok {
			c.dead(fmt.Sprintf("hooks[%d]", i), "Hook `"+h.ID+"` never runs: "+why)
		}
	}
}

// noWayOut records NO_WAY_OUT for a reachable state, not done or
// canceled and not stuck, from which no path leads to a done or canceled
// state, its ways out judged under every world, and from which no stuck
// state can be reached either (that is STUCK_STATE's finding).
func (c *typeCheck) noWayOut() bool {
	rev := map[string][]string{}
	for a, bs := range c.edges {
		for _, b := range bs {
			rev[b] = append(rev[b], a)
		}
	}
	ends := map[string]bool{}
	var q []string
	for k, s := range c.nodeState {
		if st, _ := c.ty.State(s); final(st) {
			ends[k] = true
			q = append(q, k)
		}
	}
	for len(q) > 0 {
		k := q[0]
		q = q[1:]
		for _, p := range rev[k] {
			if !ends[p] {
				ends[p] = true
				q = append(q, p)
			}
		}
	}
	for _, k := range sortedKeys(c.nodeState) {
		st, _ := c.ty.State(c.nodeState[k])
		if final(st) || ends[k] || c.stuckMemo[k] {
			continue
		}
		end, stuck, ok := c.canEnd(c.nodes[k])
		if !ok {
			return false
		}
		if end || stuck {
			continue
		}
		cd := c.firstPath[k]
		cd.message = "From " + st.ID + " it can never reach a done or canceled state: every way out leads back into the loop."
		fk := fkey{CodeNoWayOut, fmt.Sprintf("states[%d]", c.stateIndex(st.ID))}
		if old, has := c.found[fk]; !has || cd.better(old) {
			c.found[fk] = cd
		}
	}
	return true
}

// canEnd explores from n with the ways out of every world at each step:
// whether a done or canceled state can be reached, and whether a stuck
// state can. ok is false past maxNodes.
func (c *typeCheck) canEnd(n node) (end, stuck, ok bool) {
	seen := map[string]bool{n.key(): true}
	queue := []node{n}
	for len(queue) > 0 {
		if len(seen) > maxNodes {
			return false, false, false
		}
		cur := queue[0]
		queue = queue[1:]
		if st, _ := c.ty.State(cur.state); final(st) {
			return true, false, true
		}
		if s, _ := c.stuck(cur); s {
			stuck = true
			continue
		}
		for _, next := range c.successors(cur) {
			if k := next.key(); !seen[k] {
				seen[k] = true
				queue = append(queue, next)
			}
		}
	}
	return false, stuck, true
}

// successors are where any mover of any world, the timer, or a reaction
// can take n in one step.
func (c *typeCheck) successors(n node) []node {
	var ws []world
	for _, am := range c.allMovers {
		ws = append(ws, am.w)
	}
	ed := c.editable(n, ws...)
	var out []node
	for _, ex := range lifecycle.Exits(c.ty, n.state) {
		if ex.Kind != lifecycle.ExitMove {
			if next, ok := c.kernelMove(n, ex.To, ed); ok {
				out = append(out, next)
			}
			continue
		}
		tr := c.ty.Transitions[ex.Transition]
		for _, am := range c.allMovers {
			if t := c.try(n, am.w, ed, ex.Transition, am.m); t.ok {
				out = append(out, t.next)
			}
		}
		for _, m := range c.movers(tr) {
			if m.kind == mNamed || m.kind == mKernel {
				for _, w := range []world{{}, c.everyWorld()} {
					if t := c.try(n, w, ed, ex.Transition, m); t.ok {
						out = append(out, t.next)
					}
				}
			}
		}
	}
	for _, cx := range c.crossExits(n.state) {
		if next, ok := c.kernelMove(n, c.ty.Transitions[cx.transition].To, ed); ok {
			out = append(out, next)
		}
	}
	return out
}

// drafts records COLLEAGUE_EDITS_DRAFT for each draft: a reachable state,
// not done or canceled, whose every move out is allowed: [creator] (an
// open move, or one for anyone else, makes it not a draft). The fields a
// colleague holding no role may write there are listed, in the Type's
// order: a field limited to holders of a role is the Type's intent.
func (c *typeCheck) drafts() {
	for i, st := range c.ty.States {
		if final(st) || !c.isDraft(st.ID) {
			continue
		}
		var best string
		for _, k := range sortedKeys(c.nodeState) {
			if c.nodeState[k] != st.ID {
				continue
			}
			if best == "" || c.firstPath[k].better(c.firstPath[best]) {
				best = k
			}
		}
		if best == "" {
			continue
		}
		n := c.nodes[best]
		facts := c.facts(n)
		colleague := mover{kind: mColleague}.who(world{})
		var fields []string
		for _, f := range c.ty.Fields {
			if c.mayWrite(n, f, colleague, facts) {
				fields = append(fields, "`"+f.ID+"`")
			}
		}
		if len(fields) == 0 {
			continue
		}
		cd := c.firstPath[best]
		list := joinAnd(fields)
		cd.steps = append(slices.Clone(cd.steps), Step{Who: "a colleague", Kind: StepWrite, From: st.ID, To: st.ID,
			Words: "A colleague changes " + list + "."})
		cd.deps = addDeps(slices.Clone(cd.deps), scopeReaders)
		cd.message = "While it is in " + st.ID + ", the requester's draft, a colleague may change " + list + "."
		c.found[fkey{CodeColleagueEditsDraft, fmt.Sprintf("states[%d]", i)}] = cd
	}
}

// scopeReaders is what a draft finding depends on: who can see the Item,
// which its scope decides, not the Type.
const scopeReaders = "scope:readers"

func (c *typeCheck) isDraft(state string) bool {
	moves := 0
	for _, tr := range c.ty.Transitions {
		if tr.From != state {
			continue
		}
		moves++
		if len(tr.Allowed) != 1 || tr.Allowed[0] != typedef.AllowedCreator {
			return false
		}
	}
	return moves > 0
}

// joinAnd joins "a", "b" and "c" as "a, b and c".
func joinAnd(items []string) string {
	if len(items) <= 1 {
		return strings.Join(items, "")
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}
