// Tests for Jira part 2, step 4a: what a Type declares about customers (the
// Phase A answers, items 1, 3 and 8).
//
//   - own and shared, on a container's Type: the Types a customer may
//     create inside its Items. Own: and see when they are the customer's
//     own (a desk admits incidents). Shared: and see, all of them, inside
//     an Item the customer sees (an incident shares its replies). A shared
//     or owned Type ID covers all its versions: changing what a Type means
//     for the audience needs a new Type ID, never a new version.
//   - customer_fields, on any Type whose Items a customer may see: the
//     field IDs, link names and rollup IDs a customer sees and may set at
//     creation. Fail-closed: nothing else is shown, a field added in a
//     later version included.
//
// Load rules: a Type named in own or shared must exist in the set; an
// entry of customer_fields must be one of the version's fields, link names
// or rollups; a field only the kernel writes may be listed (visible), and
// is still never set by a customer (the item service holds that).
//
// Test Types: a booth (the container) admits tickets (own) and a ticket
// shares its slips (shared); a memo is shared by nobody.
package typedef_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const boothSrc = `
id: booth
version: 1
fields:
  - {id: f_name, type: string}
  - {id: f_secret, type: string}
links:
  - {name: rules, inverse: booths, targets: [booth], max: one}
ADMITS
`

func booth(admits string) string { return strings.Replace(boothSrc, "ADMITS", admits, 1) }

const stubSrc = `
id: stub
version: 1
initial_state: open
fields:
  - {id: f_title, type: string}
  - {id: f_owner, type: principal}
  - {id: f_due,   type: datetime, writable_by: kernel}
states:
  - {id: open, category: active}
links:
  - {name: blocks, inverse: blocked_by, targets: [stub]}
rollups:
  - {id: progress, over: blocks, count_by: category}
accepts_reactions:
  '*:mark': {sets: [f_due]}
SHARES
`

func stub(shares string) string { return strings.Replace(stubSrc, "SHARES", shares, 1) }

const slipSrc = `
id: slip
version: 1
fields:
  - {id: f_body, type: string}
customer_fields: [f_body]
`

const memoSrc = `
id: memo
version: 1
fields:
  - {id: f_body, type: string}
`

// --- parsing ---

func TestCustomers_Parse(t *testing.T) {
	b := mustParse(t, booth("own: [stub]\ncustomer_fields: [f_name]"))
	s := mustParse(t, stub("shared: [slip]\ncustomer_fields: [f_title, f_due, blocks, progress]"))
	for _, ty := range []*typedef.Type{b, roundTrip(t, b)} {
		if !slices.Equal(ty.Own, []string{"stub"}) || len(ty.Shared) != 0 || !slices.Equal(ty.CustomerFields, []string{"f_name"}) {
			t.Errorf("booth: own %v, shared %v, customer_fields %v", ty.Own, ty.Shared, ty.CustomerFields)
		}
	}
	for _, ty := range []*typedef.Type{s, roundTrip(t, s)} {
		if !slices.Equal(ty.Shared, []string{"slip"}) ||
			!slices.Equal(ty.CustomerFields, []string{"f_title", "f_due", "blocks", "progress"}) {
			t.Errorf("stub: shared %v, customer_fields %v", ty.Shared, ty.CustomerFields)
		}
	}
	// A Type that says nothing about customers admits none and shows them
	// nothing but the header.
	if m := mustParse(t, memoSrc); len(m.Own)+len(m.Shared)+len(m.CustomerFields) != 0 {
		t.Errorf("memo: %v %v %v", m.Own, m.Shared, m.CustomerFields)
	}
}

func TestCustomers_RefusedAtLoad(t *testing.T) {
	cases := []struct{ name, src, mention string }{
		{"a customer field that is not declared", booth("customer_fields: [f_nope]"), "f_nope"},
		{"a customer field listed twice", booth("customer_fields: [f_name, f_name]"), "f_name"},
		{"an empty customer field", booth("customer_fields: ['']"), "customer_fields"},
		// An incoming name of another Type is not this Type's to show.
		{"a link name the Type does not declare", booth("customer_fields: [booths_of]"), "booths_of"},
		{"a rollup the Type does not declare", stub("customer_fields: [velocity]"), "velocity"},
		{"an own entry that is no Type ID", booth("own: ['Stub Type']"), "Stub Type"},
		{"an own entry with a version", booth("own: ['stub@1']"), "stub@1"},
		{"an own entry listed twice", booth("own: [stub, stub]"), "stub"},
		{"a shared entry listed twice", stub("shared: [slip, slip]"), "slip"},
		// One container says one thing of a Type: shared already means
		// "the customer sees all of them", own "only their own".
		{"a Type both own and shared", booth("own: [stub]\nshared: [stub]"), "stub"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, c.src, c.mention) })
	}
	// A field only the kernel writes may be shown to a customer.
	mustParse(t, stub("customer_fields: [f_due]"))
	// A principal field may be shown too: it is the Type's choice.
	mustParse(t, stub("customer_fields: [f_owner]"))
}

// Across Types: what own and shared name must be loaded with them.
func TestParseDir_Customers(t *testing.T) {
	good := map[string]string{
		"booth.yaml": booth("own: [stub]\ncustomer_fields: [f_name]"),
		"stub.yaml":  stub("shared: [slip]\ncustomer_fields: [f_title]"),
		"slip.yaml":  slipSrc, "memo.yaml": memoSrc,
	}
	if err := parseSet(t, good); err != nil {
		t.Fatalf("booth, stub, slip and memo: %v", err)
	}
	mustRefuseSet(t, "own names a Type that is not loaded",
		map[string]string{"booth.yaml": booth("own: [stub]"), "slip.yaml": slipSrc}, "stub")
	mustRefuseSet(t, "shared names a Type that is not loaded",
		map[string]string{"stub.yaml": stub("shared: [slip]")}, "slip")
	// The error names the container and the missing Type.
	err := parseSet(t, map[string]string{"booth.yaml": booth("own: [ghost]")})
	if err == nil || !strings.Contains(err.Error(), "booth") || !strings.Contains(err.Error(), "ghost") {
		t.Errorf("the refusal = %v, want one naming booth and ghost", err)
	}
}

// --- the repository's Types (item 8) ---

func TestCustomerTypes_Files(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir(types): %v", err)
	}
	byID := map[string]*typedef.Type{}
	for _, ty := range types {
		byID[ty.ID+"@"+string(rune('0'+ty.Version))] = ty
	}
	get := func(id string) *typedef.Type {
		t.Helper()
		ty, ok := byID[id]
		if !ok {
			t.Fatalf("types/ has no %s", id)
		}
		return ty
	}

	desk := get("desk@2")
	if !slices.Equal(desk.Own, []string{"incident"}) || len(desk.Shared) != 0 ||
		!slices.Equal(sorted(desk.CustomerFields), []string{"f_key", "f_name"}) {
		t.Errorf("desk@2: own %v, shared %v, customer_fields %v; want own [incident], the key and the name shown",
			desk.Own, desk.Shared, desk.CustomerFields)
	}
	// The desk's calendar and policy links are the desk's business.
	for _, l := range []string{"calendar", "sla_policy"} {
		if slices.Contains(desk.CustomerFields, l) {
			t.Errorf("desk@2 shows its %s link to customers", l)
		}
	}

	inc := get("incident@2")
	if len(inc.Own) != 0 || !slices.Equal(inc.Shared, []string{"reply"}) {
		t.Errorf("incident@2: own %v, shared %v; want shared [reply] only (no note, no sla_goal)", inc.Own, inc.Shared)
	}
	// What a customer reads and writes on their incident: no assignee, no
	// deadline, no link to a bug.
	for _, f := range []string{"f_title", "f_priority"} {
		if !slices.Contains(inc.CustomerFields, f) {
			t.Errorf("incident@2 hides %s from its customer: they must set it to open one", f)
		}
	}
	for _, f := range []string{"f_assignee", "f_due_first_response", "f_due_resolution", "caused_by"} {
		if slices.Contains(inc.CustomerFields, f) {
			t.Errorf("incident@2 shows %s to customers", f)
		}
	}
	// The requester's two moves; staff make them with a role, never
	// through an empty allowed.
	for _, move := range [][2]string{{"new", "canceled"}, {"resolved", "in_progress"}} {
		tr, ok := transitionOf(inc, move[0], move[1])
		if !ok || !slices.Equal(sorted(tr.Allowed), []string{"creator", "role:desk_agent"}) {
			t.Errorf("incident@2 %s -> %s: %+v; want allowed [creator, role:desk_agent]", move[0], move[1], tr)
		}
	}
	// No other move names creator: a customer takes only these two.
	for _, tr := range inc.Transitions {
		if slices.Contains(tr.Allowed, typedef.AllowedCreator) &&
			!((tr.From == "new" && tr.To == "canceled") || (tr.From == "resolved" && tr.To == "in_progress")) {
			t.Errorf("incident@2 %s -> %s names creator", tr.From, tr.To)
		}
	}
	// The reply is what answers, reopens and resumes: by who wrote it,
	// with no condition on a field.
	for id, by := range map[string]string{"customer_replied": "creator", "sla_answered": "not_creator"} {
		r, ok := reactionOf(inc, id)
		if !ok || !slices.Equal(r.Of, []string{"reply"}) || r.By != by || r.When != "" {
			t.Errorf("incident@2 reaction %s = %+v; want of [reply], by %s, no when", id, r, by)
		}
	}

	reply, note := get("reply@1"), get("note@1")
	if !slices.Equal(reply.CustomerFields, []string{"f_body"}) {
		t.Errorf("reply@1 customer_fields = %v, want [f_body]", reply.CustomerFields)
	}
	if _, ok := reply.Field("f_public"); ok {
		t.Error("reply@1 declares f_public: its audience is its Type")
	}
	if len(note.CustomerFields) != 0 {
		t.Errorf("note@1 shows %v to customers", note.CustomerFields)
	}
	// Nobody shares or owns a note, a goal or a comment: staff only, by Type.
	for _, ty := range types {
		for _, hidden := range []string{"note", "sla_goal", "comment"} {
			if slices.Contains(ty.Shared, hidden) || slices.Contains(ty.Own, hidden) {
				t.Errorf("%s@%d admits customers to %s", ty.ID, ty.Version, hidden)
			}
		}
	}
	// The versions scenario B runs on say nothing about customers.
	for _, id := range []string{"desk@1", "incident@1", "comment@2"} {
		ty := get(id)
		if len(ty.Own)+len(ty.Shared)+len(ty.CustomerFields) != 0 {
			t.Errorf("%s changed: own %v, shared %v, customer_fields %v", id, ty.Own, ty.Shared, ty.CustomerFields)
		}
	}
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func transitionOf(ty *typedef.Type, from, to string) (typedef.Transition, bool) {
	for _, tr := range ty.Transitions {
		if tr.From == from && tr.To == to {
			return tr, true
		}
	}
	return typedef.Transition{}, false
}

func reactionOf(ty *typedef.Type, id string) (typedef.Reaction, bool) {
	for _, r := range ty.Reactions {
		if r.ID == id {
			return r, true
		}
	}
	return typedef.Reaction{}, false
}
