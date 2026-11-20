// Tests for Jira part 2, step 3b: the two things a Type may newly declare
// (the Phase A answers of step 3b, items 8 and 12).
//
//   - offers: {<name>: <principal field>}: what a Type offers to the hooks
//     of the Items contained in its Items. A hook there reads
//     container:<name>; the value is read from the container when the hook
//     runs. The contained Type names no host and no field of one.
//   - A link's writable_by may be a list of roles, as a field's: only their
//     holders add or remove the link.
//
// Test Types: a crate (the host) contains a tag (which tells somebody when
// its instant passes). Nothing is named after a goal.
package typedef_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// crate: the host. It offers its keeper to what it contains, and makes a
// tag inside itself when created.
const crateSrc = `
id: crate
version: 1
initial_state: open
fields:
  - {id: f_title,   type: string}
  - {id: f_keeper,  type: principal}
  - {id: f_helpers, type: principal, multiple: true}
states:
  - {id: open, category: active}
OFFERS
reactions:
  - {id: tag_it, kind: create, on: enter, state: open, type: tag, version: 1, fields: {f_at: 'cause.at'}}
`

func crate(offers string) string { return strings.Replace(crateSrc, "OFFERS", offers, 1) }

const crateOffers = "offers: {contact: f_keeper}"

// tag: what a crate contains. Its hook tells the crate's contact.
const tagSrc = `
id: tag
version: 1
initial_state: live
fields:
  - {id: f_at,      type: datetime, writable_by: kernel}
  - {id: f_rang_at, type: datetime, writable_by: kernel}
  - {id: f_note,    type: string}
states:
  - id: live
    category: active
    acting:
      - {id: ring, at: f_at, sets: {f_rang_at: 'cause.at'}, hook: tell}
transitions: []
accepts_reactions:
  '*:tag_it': {creates: [f_at]}
hooks:
  - HOOK
`

func tag(hook string) string { return strings.Replace(tagSrc, "HOOK", hook, 1) }

const tellHook = "{id: tell, on: timer, kind: action, action: email.notify, reads: ['container:contact']}"

// --- offers ---

func TestOffers_Parse(t *testing.T) {
	for _, c := range []*typedef.Type{mustParse(t, crate(crateOffers)), roundTrip(t, mustParse(t, crate(crateOffers)))} {
		if len(c.Offers) != 1 || c.Offers["contact"] != "f_keeper" {
			t.Errorf("crate offers = %v, want contact -> f_keeper", c.Offers)
		}
	}
	for _, g := range []*typedef.Type{mustParse(t, tag(tellHook)), roundTrip(t, mustParse(t, tag(tellHook)))} {
		h, ok := g.Hook("tell")
		if !ok || !slices.Equal(h.Reads, []string{typedef.ContainerOfferPrefix + "contact"}) {
			t.Errorf("tell = %+v, want one read, container:contact", h)
		}
	}
	// A Type that offers nothing says nothing.
	if c := mustParse(t, crate("")); len(c.Offers) != 0 {
		t.Errorf("a crate without offers: %v", c.Offers)
	}
}

func TestOffers_RefusedAtLoad(t *testing.T) {
	cases := []struct{ name, offers, mention string }{
		{"a field that is not declared", "offers: {contact: f_nope}", "f_nope"},
		{"a field that is not a principal", "offers: {contact: f_title}", "f_title"},
		// One recipient: a set of principals is not offered (a list of
		// recipients is another action's business).
		{"a multi-value principal field", "offers: {contact: f_helpers}", "f_helpers"},
		{"a name that is no identifier", "offers: {'the contact': f_keeper}", "the contact"},
		{"a name with the prefix in it", "offers: {'container:contact': f_keeper}", "container:contact"},
		{"an offer of nothing", "offers: {contact: ''}", "contact"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, crate(c.offers), c.mention) })
	}
	// Two names may offer one field.
	mustParse(t, crate("offers: {contact: f_keeper, owner: f_keeper}"))
}

func TestHookReads_ContainerOffer_RefusedAtLoad(t *testing.T) {
	cases := []struct{ name, hook, mention string }{
		{"no offer named", "{id: tell, on: timer, kind: action, action: email.notify, reads: ['container:']}", "container:"},
		{"an offer name that is no identifier",
			"{id: tell, on: timer, kind: action, action: email.notify, reads: ['container:the contact']}", "the contact"},
		// Still one read for email.notify: the arity is the action's.
		{"an offer beside another recipient",
			"{id: tell, on: timer, kind: action, action: email.notify, reads: ['container:contact', creator]}", "email.notify"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, tag(c.hook), c.mention) })
	}
}

// Only an action hook reads an offer: a check's reads are what its hold
// freezes, and an enrich hook's are its own Item's facts. Neither can
// freeze or depend on a field of another Item.
func TestHookReads_ContainerOffer_OnlyActionHooks(t *testing.T) {
	const gated = `
id: gated
version: 1
initial_state: open
fields:
  - {id: f_plan, type: string}
states:
  - {id: open, category: todo}
  - {id: shut, category: done}
transitions:
  - {from: open, to: shut}
hooks:
  - HOOK
`
	for name, hook := range map[string]string{
		"an enrich hook": "{id: h, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: ['container:contact'], writes: [f_plan]}",
		"a check":        "{id: h, on: transition, from: open, to: shut, kind: check, action: hr.manager_of, reads: ['container:contact', created_by], writes: [f_plan]}",
	} {
		t.Run(name, func(t *testing.T) {
			mustRefuse(t, name, strings.Replace(gated, "HOOK", hook, 1), "container:contact")
		})
	}
	// No hook writes one.
	mustRefuse(t, "a hook that writes an offer", strings.Replace(gated, "HOOK",
		"{id: h, on: enter, state: open, kind: enrich, action: crm.get_plan, reads: [f_plan], writes: ['container:contact']}", 1), "container:contact")
}

// Across Types: where the container's Type is known (it declares a create
// reaction that makes the Item inside itself), it must offer what the
// Item's hooks read. A Type whose hooks read an offer loads alone: what
// contains its Items is then checked when the hook runs.
func TestParseDir_Offers(t *testing.T) {
	good := map[string]string{"crate.yaml": crate(crateOffers), "tag.yaml": tag(tellHook)}
	if err := parseSet(t, good); err != nil {
		t.Fatalf("crate and tag: %v", err)
	}
	if err := parseSet(t, map[string]string{"tag.yaml": tag(tellHook)}); err != nil {
		t.Errorf("the tag alone, with no host loaded: %v", err)
	}
	mustRefuseSet(t, "a host that creates tags and offers nothing",
		map[string]string{"crate.yaml": crate(""), "tag.yaml": tag(tellHook)}, "contact")
	mustRefuseSet(t, "a host that offers under another name",
		map[string]string{"crate.yaml": crate("offers: {keeper: f_keeper}"), "tag.yaml": tag(tellHook)}, "contact")
	// The error names the host and the Type whose hook reads the offer.
	err := parseSet(t, map[string]string{"crate.yaml": crate(""), "tag.yaml": tag(tellHook)})
	if err == nil || !strings.Contains(err.Error(), "crate") || !strings.Contains(err.Error(), "tag") {
		t.Errorf("the refusal = %v, want one naming crate and tag", err)
	}
	// A second host, with its own field under the same offer name.
	files := map[string]string{"crate.yaml": crate(crateOffers), "tag.yaml": tag(tellHook)}
	files["box.yaml"] = strings.NewReplacer("id: crate", "id: box", "f_keeper", "f_owner").Replace(crate(crateOffers))
	if err := parseSet(t, files); err != nil {
		t.Errorf("a second host offering another field as contact: %v", err)
	}
	// A host whose create reaction makes Items that read no offer needs none.
	plain := tag("{id: tell, on: timer, kind: action, action: email.notify, reads: [creator]}")
	if err := parseSet(t, map[string]string{"crate.yaml": crate(""), "tag.yaml": plain}); err != nil {
		t.Errorf("a host of tags that read no offer: %v", err)
	}
}

// --- a link only a role writes ---

const shelfSrc = `
id: shelf
version: 1
fields:
  - {id: f_name, type: string}
links:
  - LINK
`

func shelf(link string) string { return strings.Replace(shelfSrc, "LINK", link, 1) }

func TestLinkWritableByRoles_Parse(t *testing.T) {
	src := shelf("{name: plan, inverse: shelves, targets: [shelf], max: one, writable_by: ['role:shelf_admin', 'role:lead']}")
	for _, s := range []*typedef.Type{mustParse(t, src), roundTrip(t, mustParse(t, src))} {
		l, ok := s.Link("plan")
		if !ok || !slices.Equal(l.WritableByRoles, []string{"shelf_admin", "lead"}) || l.WritableBy != typedef.WritableByAnyone {
			t.Errorf("plan = %+v, want writable by the roles shelf_admin and lead, and not kernel-only", l)
		}
	}
	// As before: no writable_by, and kernel.
	l, _ := mustParse(t, shelf("{name: plan, inverse: shelves, targets: [shelf]}")).Link("plan")
	if len(l.WritableByRoles) != 0 || l.WritableBy != typedef.WritableByAnyone {
		t.Errorf("a link without writable_by = %+v", l)
	}
}

func TestLinkWritableByRoles_RefusedAtLoad(t *testing.T) {
	link := func(wb string) string {
		return shelf("{name: plan, inverse: shelves, targets: [shelf], max: one, writable_by: " + wb + "}")
	}
	cases := []struct{ name, wb, mention string }{
		{"an empty list", "[]", "writable_by"},
		{"a role without a name", "['role:']", "role"},
		{"a principal, not a role", "['principal:user:ana']", "principal:user:ana"},
		{"a bare name", "[shelf_admin]", "shelf_admin"},
		{"kernel in a list", "[kernel]", "kernel"},
		{"a role twice", "['role:lead', 'role:lead']", "lead"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { mustRefuse(t, c.name, link(c.wb), c.mention) })
	}
}
