// Tests for Jira part 2, step 3b: the Type files of the `sla` package
// (types/sla/: sla_goal@1, calendar@1, sla_policy@1 and a README), and its
// first host: desk@1 and incident@1 in types/.
//
// What is held here is the CONTRACT between the package and a host, which
// the README states: the reaction IDs a host uses, the fields a host
// declares, the offer a host makes. What the Types then DO is held by the
// lifecycle, item service and end-to-end tests.
package typedef_test

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// The reaction IDs of the contract: a host's reactions carry these IDs,
// and sla_goal consents to them from any Type (*:<id>).
var (
	slaCreates = []string{"sla_start_first_response", "sla_start_resolution", "sla_restart_resolution"}
	slaMoves   = map[string]string{
		"sla_pause": "paused", "sla_resume": "running", "sla_stop": "stopped", "sla_answered": "stopped", "sla_cancel": "canceled",
	}
)

var priorities = []string{"P1", "P2", "P3", "P4"}

func kernelWritten(t *testing.T, ty *typedef.Type, typ typedef.FieldType, ids ...string) {
	t.Helper()
	for _, id := range ids {
		f, ok := ty.Field(id)
		if !ok || f.Type != typ || f.WritableBy != typedef.WritableByKernel {
			t.Errorf("%s.%s = %+v (declared %v); want a %s only the kernel writes", ty.ID, id, f, ok, typ)
		}
	}
}

// --- the package alone ---

// The package names no host: its folder loads by itself, and holds exactly
// these Types.
func TestSLAPackage_LoadsWithoutAHost(t *testing.T) {
	types, err := typedef.ParseDir("../../types/sla")
	if err != nil {
		t.Fatalf("ParseDir(types/sla): %v", err)
	}
	var got []string
	for _, ty := range types {
		got = append(got, fmt.Sprintf("%s@%d", ty.ID, ty.Version))
	}
	if want := "calendar@1 sla_goal@1 sla_policy@1"; strings.Join(got, " ") != want {
		t.Errorf("types/sla holds %v, want %s", got, want)
	}
	// No Type of the package names a Type outside it.
	data := ""
	for _, name := range []string{"sla_goal.v1.yaml", "calendar.v1.yaml", "sla_policy.v1.yaml"} {
		b, err := os.ReadFile(filepath.Join("../../types/sla", name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		data += string(b)
	}
	for _, host := range []string{"incident", "desk", "comment", "ticket"} {
		for _, line := range strings.Split(data, "\n") {
			if code, _, _ := strings.Cut(line, "#"); strings.Contains(code, host) {
				t.Errorf("the package names %q outside a comment: %s", host, strings.TrimSpace(line))
			}
		}
	}
}

// The README is the contract a host reads: it names every reaction ID, the
// two fields a host declares, the offer, and the links of the container.
func TestSLAPackage_READMEStatesTheContract(t *testing.T) {
	b, err := os.ReadFile("../../types/sla/README.md")
	if err != nil {
		t.Fatalf("read the README: %v", err)
	}
	readme := string(b)
	words := append([]string{}, slaCreates...)
	for id := range slaMoves {
		words = append(words, id)
	}
	words = append(words, "f_due_first_response", "f_due_resolution", "breach_contact", "offers",
		"calendar", "sla_policy", "readable_by_reactions", "accepts_reactions", "sla_goal")
	for _, w := range words {
		if !strings.Contains(readme, w) {
			t.Errorf("the README does not mention %s", w)
		}
	}
}

func TestCalendarV1(t *testing.T) {
	c := parseFile(t, "sla/calendar.v1.yaml")
	if c.ID != "calendar" || c.Version != 1 {
		t.Fatalf("calendar = %s@%d", c.ID, c.Version)
	}
	week, _ := c.Field("f_week")
	off, _ := c.Field("f_days_off")
	zone, _ := c.Field("f_zone")
	if week.Type != typedef.String || !week.Multiple || week.Max != 21 || off.Type != typedef.Date || !off.Multiple || off.Max != 366 ||
		zone.Type != typedef.String || zone.Multiple {
		t.Errorf("calendar: f_week %+v, f_days_off %+v, f_zone %+v", week, off, zone)
	}
	if _, ok := c.Field("f_name"); !ok {
		t.Error("calendar has no f_name")
	}
	for _, f := range []string{"f_week", "f_days_off", "f_zone"} {
		if !c.ReadableByReaction(f) {
			t.Errorf("calendar does not let reactions read %s", f)
		}
	}
	if c.ReadableByReaction("f_name") {
		t.Error("calendar lets reactions read f_name: only what a goal needs")
	}
	// An Item of the Type is a calendar, or does not exist.
	if len(c.Invariants) != 1 || !strings.Contains(c.Invariants[0].Rule, "calendar(f_week, f_days_off, f_zone)") {
		t.Errorf("calendar invariants = %+v, want the one that holds it to being a calendar", c.Invariants)
	}
}

// The policy: a target in seconds for each metric and priority, written by
// sla_admin only, readable by the reaction that starts a goal.
func TestSLAPolicyV1(t *testing.T) {
	p := parseFile(t, "sla/sla_policy.v1.yaml")
	if p.ID != "sla_policy" || p.Version != 1 {
		t.Fatalf("sla_policy = %s@%d", p.ID, p.Version)
	}
	for _, metric := range []string{"first_response", "resolution"} {
		for _, prio := range priorities {
			id := fmt.Sprintf("f_%s_s_%s", metric, strings.ToLower(prio))
			f, ok := p.Field(id)
			if !ok || f.Type != typedef.Int || !slices.Equal(f.WritableByRoles, []string{"sla_admin"}) {
				t.Errorf("sla_policy.%s = %+v (declared %v); want an int written by role:sla_admin", id, f, ok)
			}
			if !p.ReadableByReaction(id) {
				t.Errorf("sla_policy does not let reactions read %s", id)
			}
		}
	}
	if len(p.Invariants) == 0 {
		t.Error("sla_policy has no invariant: a target must be a positive number of seconds")
	}
}

func TestSLAGoalV1(t *testing.T) {
	g := parseFile(t, "sla/sla_goal.v1.yaml")
	if g.ID != "sla_goal" || g.Version != 1 || g.InitialState != "running" {
		t.Fatalf("sla_goal = %s@%d starting in %q; want sla_goal@1 starting in running", g.ID, g.Version, g.InitialState)
	}
	for id, cat := range map[string]string{"running": "active", "paused": "waiting", "stopped": "done", "canceled": "canceled"} {
		if s, ok := g.State(id); !ok || s.Category != cat {
			t.Errorf("state %s = %+v (found %v), want category %s", id, s, ok, cat)
		}
	}
	if len(g.States) != 4 {
		t.Errorf("sla_goal has %d states, want 4", len(g.States))
	}
	moves := [][2]string{{"running", "paused"}, {"paused", "running"}, {"running", "stopped"}, {"paused", "stopped"},
		{"running", "canceled"}, {"paused", "canceled"}}
	for _, mv := range moves {
		if _, ok := g.Transition(mv[0], mv[1]); !ok {
			t.Errorf("no transition %s -> %s", mv[0], mv[1])
		}
	}
	if len(g.Transitions) != len(moves) {
		t.Errorf("sla_goal has %d transitions, want %d: nothing leaves stopped or canceled", len(g.Transitions), len(moves))
	}

	// Every field is the kernel's: nobody edits a goal.
	kernelWritten(t, g, typedef.Int, "f_target_s", "f_elapsed_s")
	kernelWritten(t, g, typedef.DateTime, "f_started_at", "f_due_at", "f_breached_at")
	kernelWritten(t, g, typedef.Enum, "f_metric", "f_outcome")
	kernelWritten(t, g, typedef.String, "f_week", "f_zone")
	kernelWritten(t, g, typedef.Date, "f_days_off")
	for _, f := range g.Fields {
		if f.WritableBy != typedef.WritableByKernel {
			t.Errorf("sla_goal.%s is writable by others than the kernel", f.ID)
		}
		if f.Type == typedef.Principal {
			t.Errorf("sla_goal.%s is a principal field: a goal names nobody", f.ID)
		}
	}
	if f, _ := g.Field("f_metric"); !slices.Equal(f.Options, []string{"first_response", "resolution"}) {
		t.Errorf("f_metric options = %v", f.Options)
	}
	if f, _ := g.Field("f_outcome"); !slices.Equal(f.Options, []string{"met", "missed"}) {
		t.Errorf("f_outcome options = %v", f.Options)
	}

	// Breach: an acting timer at the due instant marks it once, and tells
	// the container's breach contact. The goal goes on running.
	run, _ := g.State("running")
	if len(run.Acting) != 1 || run.At != nil || run.Timer != nil {
		t.Fatalf("running: acting %+v, at %+v, timer %+v; want one acting timer and no move", run.Acting, run.At, run.Timer)
	}
	a := run.Acting[0]
	if a.At != "f_due_at" || a.When != "f_breached_at == null" || len(a.Sets) != 1 || a.Sets["f_breached_at"] != "cause.at" || a.Hook == "" {
		t.Errorf("the breach timer = %+v; want at f_due_at, when not yet breached, setting f_breached_at to the instant, with a hook", a)
	}
	h, ok := g.Hook(a.Hook)
	if !ok || h.On != typedef.OnTimer || h.Action != "email.notify" ||
		!slices.Equal(h.Reads, []string{typedef.ContainerOfferPrefix + "breach_contact"}) {
		t.Errorf("the breach hook = %+v; want email.notify to container:breach_contact", h)
	}
	if len(g.Hooks) != 1 {
		t.Errorf("sla_goal has %d hooks, want the breach notification only", len(g.Hooks))
	}

	// Consent by reaction ID, from any Type; nothing else.
	want := map[string]bool{}
	for _, id := range slaCreates {
		key := "*:" + id
		want[key] = true
		acc := g.AcceptsReactions[key]
		for _, f := range []string{"f_metric", "f_target_s", "f_started_at", "f_due_at", "f_week", "f_days_off", "f_zone"} {
			if !slices.Contains(acc.Creates, f) {
				t.Errorf("%s may not set %s at creation (creates %v)", key, f, acc.Creates)
			}
		}
		for _, f := range []string{"f_elapsed_s", "f_breached_at", "f_outcome"} {
			if slices.Contains(acc.Creates, f) {
				t.Errorf("%s may set %s at creation: a goal starts with none", key, f)
			}
		}
	}
	for id, to := range slaMoves {
		key := "*:" + id
		want[key] = true
		if acc := g.AcceptsReactions[key]; !slices.Equal(acc.Moves, []string{to}) {
			t.Errorf("%s moves = %v, want [%s]", key, acc.Moves, to)
		}
	}
	for key := range g.AcceptsReactions {
		if !want[key] {
			t.Errorf("sla_goal accepts %s, which the contract does not list", key)
		}
	}

	// Its own reactions only set, on the container, the due field of the
	// goal's metric: nothing else of a host is written.
	if len(g.Reactions) == 0 {
		t.Fatal("sla_goal declares no reaction: nothing shows its due instant on its container")
	}
	states := map[string]bool{}
	for _, r := range g.Reactions {
		if r.Kind != typedef.ReactionSet || r.Target != typedef.TargetContainer || r.On != typedef.OnEnter || len(r.Set) != 1 {
			t.Errorf("reaction %s = %+v; want a set of one field on the container, on entering a state", r.ID, r)
			continue
		}
		for field, expr := range r.Set {
			metric := strings.TrimPrefix(field, "f_due_")
			if (field != "f_due_first_response" && field != "f_due_resolution") || !strings.Contains(r.When, `"`+metric+`"`) {
				t.Errorf("reaction %s sets %s when %q; want the due field of its own metric", r.ID, field, r.When)
			}
			if r.State != "running" && expr != "null" {
				t.Errorf("reaction %s sets %s = %s on entering %s; want null outside running", r.ID, field, expr, r.State)
			}
			if r.State == "running" && expr != "f_due_at" {
				t.Errorf("reaction %s sets %s = %s on entering running; want f_due_at", r.ID, field, expr)
			}
			states[metric+" "+r.State] = true
		}
	}
	// Shown while running; unset on pause, stop and cancel, for each metric.
	for _, metric := range []string{"first_response", "resolution"} {
		for _, st := range []string{"running", "paused", "stopped", "canceled"} {
			if !states[metric+" "+st] {
				t.Errorf("no reaction writes f_due_%s on entering %s", metric, st)
			}
		}
	}
}

// --- the host ---

func TestDeskV1(t *testing.T) {
	d := parseFile(t, "desk.v1.yaml")
	if d.ID != "desk" || d.Version != 1 || len(d.States) != 1 || d.States[0].ID != typedef.ImplicitState {
		t.Fatalf("desk = %s@%d with states %v; want desk@1 without a lifecycle", d.ID, d.Version, d.States)
	}
	for _, f := range []string{"f_key", "f_name"} {
		if got, ok := d.Field(f); !ok || got.Type != typedef.String {
			t.Errorf("desk.%s = %+v (declared %v)", f, got, ok)
		}
	}
	// The requester of an incident must never choose its calendar or its
	// policy: both links are sla_admin's.
	for _, name := range []string{"calendar", "sla_policy"} {
		l, ok := d.Link(name)
		if !ok || !slices.Equal(l.Targets, []string{name}) || !l.SourceSingle() || !slices.Equal(l.WritableByRoles, []string{"sla_admin"}) {
			t.Errorf("desk link %s = %+v (declared %v); want at most one %s, written by role:sla_admin", name, l, ok, name)
		}
	}
	if len(d.Links) != 2 {
		t.Errorf("desk has %d links, want 2", len(d.Links))
	}
}

func TestIncidentV1_Lifecycle(t *testing.T) {
	inc := parseFile(t, "incident.v1.yaml")
	if inc.ID != "incident" || inc.Version != 1 || inc.InitialState != "new" {
		t.Fatalf("incident = %s@%d starting in %q; want incident@1 starting in new", inc.ID, inc.Version, inc.InitialState)
	}
	cats := map[string]string{"new": "triage", "in_progress": "active", "waiting_for_customer": "waiting",
		"resolved": "done", "closed": "done", "canceled": "canceled"}
	for id, cat := range cats {
		if s, ok := inc.State(id); !ok || s.Category != cat {
			t.Errorf("state %s = %+v (found %v), want category %s", id, s, ok, cat)
		}
	}
	if len(inc.States) != len(cats) {
		t.Errorf("incident has %d states, want %d", len(inc.States), len(cats))
	}
	moves := [][2]string{
		{"new", "in_progress"}, {"in_progress", "waiting_for_customer"}, {"waiting_for_customer", "in_progress"},
		{"in_progress", "resolved"}, {"waiting_for_customer", "resolved"}, {"resolved", "closed"}, {"resolved", "in_progress"},
		{"new", "canceled"}, {"in_progress", "canceled"},
	}
	for _, mv := range moves {
		if _, ok := inc.Transition(mv[0], mv[1]); !ok {
			t.Errorf("no transition %s -> %s", mv[0], mv[1])
		}
	}
	if len(inc.Transitions) != len(moves) {
		t.Errorf("incident has %d transitions, want %d", len(inc.Transitions), len(moves))
	}
	// Closed by a timer, 120 hours after resolved.
	res, _ := inc.State("resolved")
	if res.Timer == nil || res.Timer.To != "closed" || res.Timer.Duration != 120*time.Hour {
		t.Errorf("resolved's timer = %+v, want 120h to closed", res.Timer)
	}

	if f, ok := inc.Field("f_title"); !ok || f.Type != typedef.String {
		t.Errorf("f_title = %+v", f)
	}
	if f, ok := inc.Field("f_priority"); !ok || f.Type != typedef.Enum || !slices.Equal(f.Options, priorities) {
		t.Errorf("f_priority = %+v, want an enum P1 to P4", f)
	}
	if f, ok := inc.Field("f_impact"); !ok || f.Type != typedef.Enum || len(f.Options) == 0 {
		t.Errorf("f_impact = %+v (declared %v), want an enum", f, ok)
	}
	if f, ok := inc.Field("f_assignee"); !ok || f.Type != typedef.Principal || f.Multiple {
		t.Errorf("f_assignee = %+v, want one principal", f)
	}
	if f, ok := inc.Field("f_resolution"); !ok || f.Type != typedef.Enum {
		t.Errorf("f_resolution = %+v, want an enum", f)
	}
	kernelWritten(t, inc, typedef.DateTime, "f_due_first_response", "f_due_resolution")

	// caused_by: to bugs, many, no reaction on it (8d).
	l, ok := inc.Link("caused_by")
	if !ok || l.Inverse != "causes" || !slices.Equal(l.Targets, []string{"bug"}) || l.SourceSingle() || l.TargetSingle() ||
		l.WritableBy != typedef.WritableByAnyone || len(l.WritableByRoles) != 0 {
		t.Errorf("caused_by = %+v (declared %v); want many bugs, inverse causes, anyone's to set", l, ok)
	}
	for _, r := range inc.Reactions {
		if r.Link != "" {
			t.Errorf("reaction %s follows the link %s: no reaction runs over a link of the incident", r.ID, r.Link)
		}
	}
}

func TestIncidentV1_TheContractWithThePackage(t *testing.T) {
	inc := parseFile(t, "incident.v1.yaml")
	goal := parseFile(t, "sla/sla_goal.v1.yaml")

	if len(inc.Offers) != 1 || inc.Offers["breach_contact"] != "f_assignee" {
		t.Errorf("incident offers = %v, want breach_contact -> f_assignee", inc.Offers)
	}

	// Both goals start when the incident is created (no `from`, on the
	// initial state), each with its target picked by priority from the
	// desk's policy and its calendar copied from the desk's.
	for id, metric := range map[string]string{"sla_start_first_response": "first_response", "sla_start_resolution": "resolution"} {
		r, ok := inc.Reaction(id)
		if !ok || r.Kind != typedef.ReactionCreate || r.On != typedef.OnEnter || r.State != "new" || len(r.From) != 0 ||
			r.Type != "sla_goal" || r.Version != 1 {
			t.Errorf("%s = %+v (declared %v); want a create of sla_goal@1 at creation", id, r, ok)
			continue
		}
		if r.Fields["f_metric"] != `"`+metric+`"` || r.Fields["f_started_at"] != "cause.at" {
			t.Errorf("%s: f_metric %q, f_started_at %q", id, r.Fields["f_metric"], r.Fields["f_started_at"])
		}
		var reads []string
		for _, lr := range r.LinkedReads {
			if !lr.Container {
				t.Errorf("%s reads %+v: only the desk's linked Items are read", id, lr)
			}
			reads = append(reads, lr.Link+"."+lr.Field)
		}
		for _, w := range []string{"calendar.f_week", "calendar.f_days_off", "calendar.f_zone"} {
			if !slices.Contains(reads, w) {
				t.Errorf("%s does not read %s (reads %v)", id, w, reads)
			}
		}
		for _, p := range priorities {
			if w := fmt.Sprintf("sla_policy.f_%s_s_%s", metric, strings.ToLower(p)); !slices.Contains(reads, w) {
				t.Errorf("%s does not read %s (reads %v)", id, w, reads)
			}
		}
		if !strings.Contains(r.Fields["f_target_s"], "f_priority") || !strings.Contains(r.Fields["f_due_at"], "working_after(cause.at,") {
			t.Errorf("%s: f_target_s %q, f_due_at %q; want the target by f_priority and the due instant in working time",
				id, r.Fields["f_target_s"], r.Fields["f_due_at"])
		}
	}
	// Reopening starts a new resolution goal, and only that.
	if r, ok := inc.Reaction("sla_restart_resolution"); !ok || r.Kind != typedef.ReactionCreate || r.State != "in_progress" ||
		!slices.Equal(r.From, []string{"resolved"}) || r.Fields["f_metric"] != `"resolution"` {
		t.Errorf("sla_restart_resolution = %+v (declared %v); want a resolution goal on resolved -> in_progress", r, ok)
	}

	type move struct {
		state       string
		from        []string
		childWhen   string
		to          string
		targetFrom  []string
		createdSide bool
	}
	onlyResolution := `child.f_metric == "resolution"`
	moves := map[string]move{
		// Only resolution pauses, only in waiting_for_customer.
		"sla_pause":  {state: "waiting_for_customer", childWhen: onlyResolution, to: "paused", targetFrom: []string{"running"}},
		"sla_resume": {state: "in_progress", from: []string{"waiting_for_customer"}, childWhen: onlyResolution, to: "running", targetFrom: []string{"paused"}},
		"sla_stop":   {state: "resolved", to: "stopped", targetFrom: []string{"running", "paused"}},
		"sla_cancel": {state: "canceled", to: "canceled", targetFrom: []string{"running", "paused"}},
		"sla_answered": {createdSide: true, childWhen: `child.f_metric == "first_response"`, to: "stopped",
			targetFrom: []string{"running"}},
	}
	for id, want := range moves {
		r, ok := inc.Reaction(id)
		if !ok || r.Kind != typedef.ReactionMove || r.Children == nil || !slices.Equal(r.Children.Of, []string{"sla_goal"}) {
			t.Errorf("%s = %+v (declared %v); want a move of the contained sla_goals", id, r, ok)
			continue
		}
		if r.To != want.to || !slices.Equal(r.TargetFrom, want.targetFrom) || r.Children.When != want.childWhen {
			t.Errorf("%s: to %s from %v, children when %q; want to %s from %v when %q",
				id, r.To, r.TargetFrom, r.Children.When, want.to, want.targetFrom, want.childWhen)
		}
		if want.createdSide {
			// A public comment by someone other than the requester.
			if r.On != typedef.OnCreatedInside || !slices.Equal(r.Of, []string{"comment"}) || r.By != typedef.ByNotCreator ||
				r.When != "child.f_public == true" {
				t.Errorf("%s = %+v; want on a public comment by someone other than the requester", id, r)
			}
			continue
		}
		if r.On != typedef.OnEnter || r.State != want.state || !slices.Equal(r.From, want.from) {
			t.Errorf("%s: on %s, state %s, from %v; want on entering %s from %v", id, r.On, r.State, r.From, want.state, want.from)
		}
	}
	// The requester's public comment sets a waiting incident back in
	// progress (as ticket@6's reopen): one more reaction, the host's own.
	var back []typedef.Reaction
	for _, r := range inc.Reactions {
		if r.Kind == typedef.ReactionMove && r.Children == nil {
			back = append(back, r)
		}
	}
	if len(back) != 1 || back[0].On != typedef.OnCreatedInside || !slices.Equal(back[0].Of, []string{"comment"}) ||
		back[0].By != typedef.ByCreator || back[0].When != "child.f_public == true" ||
		!slices.Equal(back[0].From, []string{"waiting_for_customer"}) || back[0].To != "in_progress" {
		t.Errorf("the incident's own moves = %+v; want one: the requester's public comment, waiting_for_customer -> in_progress", back)
	}
	if len(inc.Reactions) != len(slaCreates)+len(slaMoves)+1 {
		t.Errorf("incident declares %d reactions, want %d", len(inc.Reactions), len(slaCreates)+len(slaMoves)+1)
	}

	// The incident consents to each of the goal's set reactions, for the
	// one field it sets, and to nothing else.
	for _, r := range goal.Reactions {
		var fields []string
		for f := range r.Set {
			fields = append(fields, f)
		}
		if acc := inc.AcceptsReactions["sla_goal:"+r.ID]; !slices.Equal(acc.Sets, fields) {
			t.Errorf("incident accepts sla_goal:%s sets %v, want %v", r.ID, acc.Sets, fields)
		}
	}
	if len(inc.AcceptsReactions) != len(goal.Reactions) {
		t.Errorf("incident accepts %d reactions, the goal declares %d", len(inc.AcceptsReactions), len(goal.Reactions))
	}
}

// Everything in types/ still loads together, the package with its host.
func TestRepositoryTypes_LoadWithTheSLAPackage(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir(types): %v", err)
	}
	have := map[string]bool{}
	for _, ty := range types {
		have[fmt.Sprintf("%s@%d", ty.ID, ty.Version)] = true
	}
	for _, want := range []string{"calendar@1", "sla_goal@1", "sla_policy@1", "desk@1", "incident@1"} {
		if !have[want] {
			t.Errorf("types/ does not hold %s", want)
		}
	}
}
