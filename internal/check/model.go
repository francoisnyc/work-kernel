package check

import (
	"cmp"
	"slices"
	"strings"

	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The abstract world the checker explores. An Item's fields are not
// values but what matters to who may act and to the invariants: empty,
// set, or (for a principal field) whom it names. A principal is a class
// with stand-in IDs, handed to policy as if it were a real one.

// val is what the checker knows of a field's value.
type val uint8

const (
	vEmpty val = iota
	vSet       // a value (not a principal)
	vAny       // empty or any value: written by someone, or not known
	vRequester // a principal field naming the requester
	vColleague // ... a colleague
	vAgent     // ... the agent
	vNamed     // ... whoever the field names: a person the kernel chose
)

// The stand-in principals. They are not real IDs: "@" is not in any.
const (
	requesterID = "user:@requester"
	colleagueID = "user:@colleague"
	agentID     = "agent:@agent"
	containerID = "user:@container_creator"
	namedPrefix = "user:@named:"
)

// The agent acts for itself, for the requester, or for a colleague.
const (
	forSelf = iota
	forRequester
	forColleague
)

// world fixes, for one search, the one role (or none) each class holds
// and whom the agent acts for.
type world struct {
	req, col, agent string
	agentFor        int
}

func (w world) roleCount() int {
	n := 0
	for _, r := range []string{w.req, w.col, w.agent} {
		if r != "" {
			n++
		}
	}
	return n
}

// Kinds of mover.
const (
	mRequester = iota
	mColleague
	mNamed
	mAgent
	mKernel
)

// mover is who makes a move: a class, the person a field names, or one of
// the kernel's own actors that a move's allowed names.
type mover struct {
	kind  int
	field string // mNamed: the field
	id    string // mKernel: the principal
}

func roles(r string) []string {
	if r == "" {
		return nil
	}
	return []string{r}
}

// who is the stand-in principal policy is asked about.
func (m mover) who(w world) policy.Who {
	switch m.kind {
	case mRequester:
		return policy.Who{Actor: requesterID, ActorRoles: roles(w.req)}
	case mColleague:
		return policy.Who{Actor: colleagueID, ActorRoles: roles(w.col)}
	case mNamed:
		return policy.Who{Actor: namedPrefix + m.field}
	case mAgent:
		who := policy.Who{Actor: agentID, ActorRoles: roles(w.agent)}
		switch w.agentFor {
		case forRequester:
			who.OnBehalfOf, who.OnBehalfOfRoles = requesterID, roles(w.req)
		case forColleague:
			who.OnBehalfOf, who.OnBehalfOfRoles = colleagueID, roles(w.col)
		}
		return who
	}
	return policy.Who{Actor: m.id}
}

func holding(r string) string {
	if r == "" {
		return ""
	}
	return " holding " + r
}

// words names the mover in a sentence: "an agent holding finance".
func (m mover) words(w world) string {
	switch m.kind {
	case mRequester:
		return "the requester" + holding(w.req)
	case mColleague:
		return "a colleague" + holding(w.col)
	case mNamed:
		return "whoever `" + m.field + "` names"
	case mAgent:
		s := "an agent" + holding(w.agent)
		switch w.agentFor {
		case forRequester:
			s += ", acting for the requester"
		case forColleague:
			s += ", acting for a colleague" + holding(w.col)
		}
		return s
	}
	return "the kernel (`" + m.id + "`)"
}

// roleDeps are the roles a mover's move rests on.
func (m mover) roleDeps(w world) []string {
	var out []string
	add := func(r string) {
		if r != "" {
			out = append(out, typedef.AllowedRolePrefix+r)
		}
	}
	switch m.kind {
	case mRequester:
		add(w.req)
	case mColleague:
		add(w.col)
	case mAgent:
		add(w.agent)
		switch w.agentFor {
		case forRequester:
			add(w.req)
		case forColleague:
			add(w.col)
		}
	}
	return out
}

// valOf is the value a principal field takes when id is written to it.
func valOf(id string) val {
	switch {
	case id == requesterID:
		return vRequester
	case id == colleagueID:
		return vColleague
	case id == agentID:
		return vAgent
	case strings.HasPrefix(id, namedPrefix):
		return vNamed
	}
	return vAny
}

// sample is a value of the field's type, for asking lifecycle whether an
// invariant can hold.
func samples(f typedef.Field) []any {
	var one []any
	switch f.Type {
	case typedef.Int:
		one = []any{int64(0), int64(1)}
	case typedef.Bool:
		one = []any{true, false}
	case typedef.Enum:
		for _, o := range f.Options {
			one = append(one, o)
		}
	case typedef.Date:
		one = []any{"2026-01-01"}
	case typedef.DateTime:
		one = []any{"2026-01-01T00:00:00.000000Z"}
	case typedef.Principal:
		one = []any{requesterID}
	default:
		one = []any{"x"}
	}
	if f.Multiple {
		for i, v := range one {
			one[i] = []any{v}
		}
	}
	return one
}

// sortedKeys returns a map's keys in order: a map's own order is random.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// addDeps appends what is not there yet, keeping the order.
func addDeps(deps []string, more ...string) []string {
	for _, d := range more {
		if d != "" && !slices.Contains(deps, d) {
			deps = append(deps, d)
		}
	}
	return deps
}

// effective is the user a principal acts as.
func effective(who policy.Who) string { return cmp.Or(who.OnBehalfOf, who.Actor) }
