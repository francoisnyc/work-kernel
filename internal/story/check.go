package story

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/francoisnyc/work-kernel/internal/api"
	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/hookproto"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Check holds a story against the Types it may use and the action
// contracts, before it runs: its people, Items, Types, fields, states and
// values; every fake answer against its action's contract; a fake for
// every action its Types' hooks may call. It returns every problem found.
func Check(s *Story, types []*typedef.Type, catalog *contracts.Catalog) Problems {
	c := &checker{s: s, conv: newConverter(s), catalog: catalog,
		byVersion: map[string]*typedef.Type{}, byID: map[string][]*typedef.Type{},
		items: map[string]*typedef.Type{}, ruled: map[string]string{}}
	for _, ty := range types {
		c.byVersion[fmt.Sprintf("%s@%d", ty.ID, ty.Version)] = ty
		c.byID[ty.ID] = append(c.byID[ty.ID], ty)
	}
	c.people()
	c.steps()
	c.names()
	c.hidden()
	c.fakes()
	slices.SortStableFunc(c.problems, func(a, b Problem) int { return a.Line - b.Line })
	return c.problems
}

type checker struct {
	s         *Story
	conv      converter
	catalog   *contracts.Catalog
	byVersion map[string]*typedef.Type   // id@version -> Type
	byID      map[string][]*typedef.Type // id -> every version
	items     map[string]*typedef.Type   // Item names steps create -> their Type
	ruled     map[string]string          // names name rules give -> their Type ID
	created   []string                   // id@version of every Item a step creates
	problems  Problems
}

func (c *checker) fail(line int, format string, args ...any) {
	c.problems = append(c.problems, Problem{Source: c.s.Source, Line: line, Message: fmt.Sprintf(format, args...)})
}

func (c *checker) people() {
	seen := map[string]bool{}
	for _, p := range c.s.People {
		if seen[p.Name] {
			c.fail(p.Line, "%s is one of the people twice", p.Name)
		}
		seen[p.Name] = true
		if p.Customer && len(p.Roles) > 0 {
			c.fail(p.Line, "%s is a customer, and customers hold no roles", p.Name)
		}
	}
	if c.s.Observer != "" && !seen[c.s.Observer] {
		c.fail(0, "observer %s is not one of the story's people", c.s.Observer)
	}
}

// known reports whether a step may name the Item name, and its Type's
// fields (every version, for an Item a name rule names).
func (c *checker) known(name string) ([]*typedef.Type, bool) {
	if ty, ok := c.items[name]; ok {
		if ty == nil {
			return nil, true
		}
		return []*typedef.Type{ty}, true
	}
	if id, ok := c.ruled[name]; ok {
		return c.byID[id], true
	}
	return nil, false
}

func (c *checker) steps() {
	// Name rules are known before the steps: a step may read an Item the
	// kernel creates (kim cannot see first).
	for _, r := range c.s.Names {
		c.ruled[r.Name] = r.Type
	}
	var last time.Time
	var lastLine int
	for i, st := range append(slices.Clone(c.s.Given), c.s.Steps...) {
		timed := i >= len(c.s.Given)
		if timed {
			if st.At.Before(last) {
				c.fail(st.Line, "steps are in time order: this one comes before the step on line %d", lastLine)
			}
			last, lastLine = st.At, st.Line
		}
		c.step(st)
	}
	if !c.s.Until.IsZero() && len(c.s.Steps) > 0 && c.s.Until.Before(c.s.Steps[len(c.s.Steps)-1].At) {
		c.fail(0, "until is before the last step")
	}
}

func (c *checker) step(st Step) {
	if !slices.ContainsFunc(c.s.People, func(p Person) bool { return p.Name == st.Actor }) {
		c.fail(st.Line, "%s is not one of the story's people", st.Actor)
		return
	}
	if st.Refused != "" {
		if _, ok := api.ReasonMessage(st.Refused); !ok {
			c.fail(st.Line, "refused %s: not a reason the API answers with (NOT_ALLOWED, GUARD_FAILED, ...)", st.Refused)
			return
		}
	}
	if st.Verb == Creates {
		if _, twice := c.items[st.Item]; twice {
			c.fail(st.Line, "%s is created twice", st.Item)
			return
		}
		// The Item is known from here on even if this step has a problem,
		// so later steps naming it add none on top (no cascades).
		ty, ok := c.byVersion[fmt.Sprintf("%s@%d", st.Type, st.Version)]
		c.items[st.Item] = ty
		if !ok {
			c.fail(st.Line, "no Type %s@%d in types/", st.Type, st.Version)
			return
		}
		c.created = append(c.created, fmt.Sprintf("%s@%d", st.Type, st.Version))
		if st.In != "" {
			if _, ok := c.known(st.In); !ok {
				c.fail(st.Line, "in %s: no step created %s before", st.In, st.In)
				return
			}
		}
		if msg := c.values([]*typedef.Type{ty}, st.Values); msg != "" {
			c.fail(st.Line, "%s", msg)
		}
		return
	}
	tys, ok := c.known(st.Item)
	if !ok {
		c.fail(st.Line, "%s: no step created it before, and no name rule names it", st.Item)
		return
	}
	if len(tys) == 0 || tys[0] == nil {
		return // its create had a problem already
	}
	switch st.Verb {
	case Updates, Sees:
		if msg := c.values(tys, st.Values); msg != "" {
			c.fail(st.Line, "%s", msg)
		}
	case Moves:
		if !slices.ContainsFunc(tys, func(ty *typedef.Type) bool { _, ok := ty.State(st.To); return ok }) {
			c.fail(st.Line, "%s has no state %s", typeName(tys), st.To)
			return
		}
		if msg := c.values(tys, st.Values); msg != "" {
			c.fail(st.Line, "%s", msg)
		}
	case Links, Unlinks:
		if !slices.ContainsFunc(tys, func(ty *typedef.Type) bool { _, ok := ty.Link(st.Link); return ok }) {
			c.fail(st.Line, "%s has no link %s", typeName(tys), st.Link)
			return
		}
		if _, ok := c.known(st.Target); !ok {
			c.fail(st.Line, "%s: no step created it before", st.Target)
		}
	case SeesFields:
		for _, f := range st.Fields {
			if _, ok := fieldOf(tys, f); !ok {
				c.fail(st.Line, "%s has no field %s", typeName(tys), f)
				return
			}
		}
	}
}

// stepItem reports whether a step creates an Item of that name.
func (c *checker) stepItem(name string) bool {
	_, ok := c.items[name]
	return ok
}

func typeName(tys []*typedef.Type) string {
	if len(tys) == 1 {
		return fmt.Sprintf("%s@%d", tys[0].ID, tys[0].Version)
	}
	if len(tys) > 0 {
		return tys[0].ID
	}
	return "its Type"
}

// fieldOf finds a field in any of the versions.
func fieldOf(tys []*typedef.Type, id string) (typedef.Field, bool) {
	for _, ty := range tys {
		if f, ok := ty.Field(id); ok {
			return f, true
		}
	}
	return typedef.Field{}, false
}

// values checks field = value against the Type: "" when every one fits.
func (c *checker) values(tys []*typedef.Type, vals []Assignment) string {
	for _, a := range vals {
		f, ok := fieldOf(tys, a.Field)
		if !ok {
			return fmt.Sprintf("%s has no field %s", typeName(tys), a.Field)
		}
		if _, err := c.conv.value(a.Value, fieldSlot(f)); err != nil {
			return err.Error()
		}
	}
	return ""
}

func (c *checker) names() {
	for _, r := range c.s.Names {
		tys, ok := c.byID[r.Type]
		switch {
		case !ok:
			c.fail(r.Line, "name %s: no Type %s in types/", r.Name, r.Type)
			continue
		case c.stepItem(r.Name):
			c.fail(r.Line, "name %s: a step already creates an Item of that name", r.Name)
			continue
		}
		if r.In != "" && !c.stepItem(r.In) {
			c.fail(r.Line, "name %s: in %s, which no step creates", r.Name, r.In)
			continue
		}
		if r.Where != nil {
			if msg := c.values(tys, []Assignment{*r.Where}); msg != "" {
				c.fail(r.Line, "name %s: %s", r.Name, msg)
			}
		}
	}
}

func (c *checker) hidden() {
	for _, id := range sortedKeys(c.s.HideValues) {
		tys, ok := c.byID[id]
		if !ok {
			c.fail(0, "hide_values: no Type %s in types/", id)
			continue
		}
		for _, f := range c.s.HideValues[id] {
			if _, ok := fieldOf(tys, f); !ok {
				c.fail(0, "hide_values: %s has no field %s", id, f)
			}
		}
	}
}

func (c *checker) fakes() {
	faked := map[string]bool{}
	unknown := map[string]bool{} // connector.operation of a fake naming no contract
	for _, f := range c.s.Fakes {
		if faked[f.Action] {
			c.fail(f.Line, "%s has a fake already", f.Action)
			continue
		}
		faked[f.Action] = true
		contract, ok := c.catalog.Lookup(f.Action)
		if !ok || contract.Action() != f.Action {
			c.fail(f.Line, "no action %s in contracts/", f.Action)
			op, _, _ := strings.Cut(f.Action, "@")
			unknown[op] = true
			continue
		}
		if len(f.Rules) == 0 {
			c.fail(f.Line, "%s: a fake has at least one rule", f.Action)
			continue
		}
		for _, r := range f.Rules {
			if msg := c.rule(contract, r); msg != "" {
				c.fail(r.Line, "%s: %s", f.Action, msg)
			}
		}
	}
	// Every action the Types may call: those steps create, and those
	// their reactions create, all the way down.
	needed := map[string][]string{} // action -> the Types that call it
	seen := map[string]bool{}
	var walk func(name string)
	walk = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		ty := c.byVersion[name]
		if ty == nil {
			return
		}
		for _, h := range ty.Hooks {
			if contract, ok := c.catalog.Lookup(h.Action); ok && !slices.Contains(needed[contract.Action()], name) {
				needed[contract.Action()] = append(needed[contract.Action()], name)
			}
		}
		for _, r := range ty.Reactions {
			if r.Kind == typedef.ReactionCreate && r.Type != "" {
				walk(fmt.Sprintf("%s@%d", r.Type, r.Version))
			}
		}
	}
	for _, name := range c.created {
		walk(name)
	}
	for _, action := range sortedKeys(needed) {
		op, _, _ := strings.Cut(action, "@")
		if !faked[action] && !unknown[op] {
			c.fail(0, "%s has no fake: %s's hooks call it", action, strings.Join(needed[action], " and "))
		}
	}
}

// rule checks one rule against its action's contract: "" when it fits.
func (c *checker) rule(contract contracts.Contract, r Rule) string {
	kinds := contract.Kinds
	is := func(k hookproto.Kind) bool { return slices.Contains(kinds, k) }
	kindNames := make([]string, len(kinds))
	for i, k := range kinds {
		kindNames[i] = string(k)
	}
	kind := strings.Join(kindNames, " and ")
	switch r.Kind {
	case Allow, Deny, Correct:
		if !is(hookproto.KindCheck) {
			return fmt.Sprintf("%q answers a check; this is an %s", string(r.Kind), kind)
		}
	case OK:
		if !is(hookproto.KindEnrich) && !is(hookproto.KindAction) {
			return fmt.Sprintf("%q answers an enrich or an action; this is a %s", string(r.Kind), kind)
		}
	}
	if r.Kind == Deny && !slices.Contains(contract.Errors, r.Reason) {
		return fmt.Sprintf("deny %s: not a reason its contract declares (%s)", r.Reason, strings.Join(contract.Errors, ", "))
	}
	if r.Kind == Correct || r.Kind == OK {
		outs := contract.OutputValues()
		for _, a := range r.Values {
			i := slices.IndexFunc(outs, func(v contracts.Value) bool { return v.Name == a.Field })
			if i < 0 {
				return fmt.Sprintf("%s is not one of its outputs", a.Field)
			}
			x, err := c.conv.value(a.Value, contractSlot(outs[i]))
			if err == nil {
				err = outs[i].Check(x)
			}
			if err != nil {
				return err.Error()
			}
		}
		for _, o := range outs {
			if !slices.ContainsFunc(r.Values, func(a Assignment) bool { return a.Field == o.Name }) {
				return fmt.Sprintf("%s %s: want every output, and %s is missing", r.Kind, contract.Action(), o.Name)
			}
		}
	}
	ins := contract.InputValues()
	for _, a := range r.When {
		i := slices.IndexFunc(ins, func(v contracts.Value) bool { return v.Name == a.Field })
		if i < 0 {
			return fmt.Sprintf("when %s: not one of its inputs", a.Field)
		}
		x, err := c.conv.value(a.Value, contractSlot(ins[i]))
		if err == nil {
			err = ins[i].Check(x)
		}
		if err != nil {
			return "when " + err.Error()
		}
	}
	if !r.From.IsZero() && !r.Until.IsZero() && !r.Until.After(r.From) {
		return "the rule's until is not after its from"
	}
	return ""
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
