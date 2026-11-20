// Tests for Jira part 2, step 3b, through the public API, on the
// repository's own Types (the `sla` package, desk@1, incident@1).
//
//   - GetType shows what a Type offers to the hooks of contained Items, and
//     the roles that may write a link.
//   - LinkItems and UnlinkItems on such a link, without the role:
//     PERMISSION_DENIED / LINK_NOT_WRITABLE.
//   - A reaction's outcome record names its target as an ItemRef through
//     the visibility filter (ItemEvent.reaction_target): the name, or
//     restricted, never an ID the caller cannot see.
//   - HOOK_FAILED with the reason NO_RECIPIENT, in upper case like every
//     event reason.
//
// The reactions are run through the kernel's internal door, as the worker
// would; the scenario and the end-to-end tests run them for real.
package api_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const (
	incidentV1  = "namespaces/acme/types/incident/versions/1"
	deskV1      = "namespaces/acme/types/desk/versions/1"
	calendarV1  = "namespaces/acme/types/calendar/versions/1"
	slaPolicyV1 = "namespaces/acme/types/sla_policy/versions/1"
	slaGoalV1   = "namespaces/acme/types/sla_goal/versions/1"
)

func mkIn(t *testing.T, who, typ, container string, f map[string]any) string {
	t.Helper()
	res, err := as(who).CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: typ, Container: container, Fields: fields(t, f),
	}))
	if err != nil {
		t.Fatalf("create %s as %s: %v", typ, who, err)
	}
	return res.Msg.Item.Name
}

// itDesk is a desk with a calendar open around the clock and a policy,
// both linked by hana, who holds sla_admin in these tests.
func itDesk(t *testing.T) string {
	t.Helper()
	if _, err := pool.Exec(testCtx(t), `insert into principal_roles (namespace_id, principal, role)
		values ('acme', 'user:hana', 'sla_admin') on conflict do nothing`); err != nil {
		t.Fatalf("grant sla_admin to hana: %v", err)
	}
	week := []any{}
	for _, d := range []string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"} {
		week = append(week, d+" 00:00-24:00")
	}
	cal := mkIn(t, "hana", calendarV1, "", map[string]any{"f_name": "always", "f_week": week, "f_zone": "UTC"})
	pf := map[string]any{"f_name": "standard"}
	for i, p := range []string{"p1", "p2", "p3", "p4"} {
		pf["f_first_response_s_"+p] = 60 * (i + 1)
		pf["f_resolution_s_"+p] = 600 * (i + 1)
	}
	policy := mkIn(t, "hana", slaPolicyV1, "", pf)
	desk := mkIn(t, "hana", deskV1, "", map[string]any{"f_key": "IT", "f_name": "IT " + word()})
	mustLinkItems(t, "hana", desk, "calendar", cal)
	mustLinkItems(t, "hana", desk, "sla_policy", policy)
	return desk
}

func runReaction(source uuid.UUID, reaction string, cause uuid.UUID, seq int64) itemservice.ReactionRunRequest {
	return itemservice.ReactionRunRequest{SourceID: source, Reaction: reaction, CauseItem: cause, CauseSeq: seq, Hop: 1}
}

// startedIncident is ana's P1 incident with both goals created; it returns
// the incident's name and the goals' IDs (first response, resolution).
func startedIncident(t *testing.T, desk string, more map[string]any) (string, uuid.UUID, uuid.UUID) {
	t.Helper()
	f := map[string]any{"f_title": "VPN down " + word(), "f_priority": "P1"}
	for k, v := range more {
		f[k] = v
	}
	inc := mkIn(t, "ana", incidentV1, desk, f)
	id := itemID(t, inc)
	first, err := k.Internal().ReactCreate(testCtx(t), runReaction(id, "sla_start_first_response", id, 1))
	if err != nil || first.Outcome != itemservice.ReactionApplied {
		t.Fatalf("sla_start_first_response: %+v, %v", first, err)
	}
	res, err := k.Internal().ReactCreate(testCtx(t), runReaction(id, "sla_start_resolution", id, 1))
	if err != nil || res.Outcome != itemservice.ReactionApplied {
		t.Fatalf("sla_start_resolution: %+v, %v", res, err)
	}
	return inc, first.Target, res.Target
}

func nameOf(id uuid.UUID) string { return "namespaces/acme/items/" + id.String() }

// --- what the Types show ---

func TestGetType_ShowsOffersAndLinkRoles(t *testing.T) {
	inc := typeNamed(t, incidentV1)
	if len(inc.Offers) != 1 || inc.Offers[0].Name != "breach_contact" || inc.Offers[0].Field != "f_assignee" {
		t.Errorf("incident offers = %v, want breach_contact: f_assignee", inc.Offers)
	}
	desk := typeNamed(t, deskV1)
	var links []string
	for _, l := range desk.LinkTypes {
		links = append(links, l.Name)
		if !slices.Equal(l.WritableByRoles, []string{"sla_admin"}) || l.WritableBy != kernelv1.FieldWriter_FIELD_WRITER_UNSPECIFIED || !l.MaxOne {
			t.Errorf("desk link %s = %v; want at most one, writable by role sla_admin", l.Name, l)
		}
	}
	slices.Sort(links)
	if !slices.Equal(links, []string{"calendar", "sla_policy"}) {
		t.Errorf("desk links = %v", links)
	}
	// A Type that offers nothing, and a link anyone sets: both empty.
	if got := typeNamed(t, slaGoalV1); len(got.Offers) != 0 {
		t.Errorf("sla_goal offers %v", got.Offers)
	}
	for _, l := range inc.LinkTypes {
		if len(l.WritableByRoles) != 0 {
			t.Errorf("incident link %s is role-written: %v", l.Name, l.WritableByRoles)
		}
	}
	// The policy's targets are sla_admin's, as a leave balance's allowance
	// is hr_admin's.
	for _, f := range typeNamed(t, slaPolicyV1).Fields {
		if f.Type == kernelv1.FieldType_FIELD_TYPE_INT && !slices.Equal(f.WritableByRoles, []string{"sla_admin"}) {
			t.Errorf("sla_policy.%s = %v, want writable by role sla_admin", f.Id, f)
		}
	}
}

// --- a link only a role writes ---

func TestLinkItems_ARoleWrittenLink(t *testing.T) {
	desk := itDesk(t)
	other := mkIn(t, "ana", deskV1, "", map[string]any{"f_key": "HR", "f_name": "HR " + word()})
	cal := ""
	for _, l := range get(t, "hana", desk).Links {
		if l.LinkType == "calendar" {
			cal = l.Item.Name
		}
	}
	if cal == "" {
		t.Fatal("the desk shows no calendar link")
	}
	// ana made the desk: it is still not hers to give a calendar or a policy.
	_, err := linkItems("ana", other, "calendar", cal, "")
	apiError(t, "ana links a calendar", err, connect.CodePermissionDenied, "LINK_NOT_WRITABLE")
	_, err = as("ana").UnlinkItems(testCtx(t), connect.NewRequest(&kernelv1.UnlinkItemsRequest{Name: desk, RequestId: newID(), LinkType: "calendar", Target: cal}))
	apiError(t, "ana unlinks the desk's calendar", err, connect.CodePermissionDenied, "LINK_NOT_WRITABLE")
	if n := countEvents(t, other); n != 1 {
		t.Errorf("the refused link left %d events on the desk, want its create only", n)
	}
	// An agent acting for ana, with every power: the role is not a power.
	// (hana's own agents are no different: both must hold the role.)
	mustLinkItems(t, "hana", other, "calendar", cal)
	if got := linkStrings(get(t, "ana", other).Links); !slices.Equal(got, []string{"OUTGOING calendar " + cal}) {
		t.Errorf("the desk's links = %v", got)
	}
}

// --- reaction records name their target ---

func TestListItemEvents_ReactionTarget(t *testing.T) {
	desk := itDesk(t)
	inc, first, res := startedIncident(t, desk, nil)
	id := itemID(t, inc)
	transition(t, "bob", inc, "in_progress")
	resolved, err := moveWith(t, "bob", inc, "resolved", map[string]any{"f_resolution": "fixed"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if out, err := k.Internal().ReactMove(testCtx(t), runReaction(id, "sla_stop", id, resolved.ItemSeq)); err != nil || len(out.Each) != 2 {
		t.Fatalf("sla_stop: %+v, %v", out, err)
	}
	late := mkIn(t, "bob", commentV2, inc, map[string]any{"f_body": "done", "f_public": true})
	if out, err := k.Internal().ReactMove(testCtx(t), runReaction(id, "sla_answered", itemID(t, late), 1)); err != nil || out.Reason != itemservice.SkipNoTarget {
		t.Fatalf("sla_answered after the end: %+v, %v", out, err)
	}

	targets := func(who string) map[string][]string {
		t.Helper()
		out := map[string][]string{}
		for _, ev := range events(t, who, inc) {
			switch ev.Kind {
			case kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_APPLIED, kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_FAILED,
				kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_SKIPPED:
				switch rt := ev.ReactionTarget; {
				case rt == nil:
					out[ev.Reaction] = append(out[ev.Reaction], "none")
				case rt.Restricted:
					if rt.Name != "" {
						t.Errorf("a restricted target carries the name %s", rt.Name)
					}
					out[ev.Reaction] = append(out[ev.Reaction], "restricted")
				default:
					out[ev.Reaction] = append(out[ev.Reaction], rt.Name)
				}
			default:
				if ev.ReactionTarget != nil {
					t.Errorf("event %d (%v) names a reaction target", ev.ItemSeq, ev.Kind)
				}
			}
		}
		return out
	}

	both := []string{nameOf(first), nameOf(res)}
	slices.Sort(both) // one record per child, in the order of their IDs
	want := map[string][]string{
		"incident:sla_start_first_response": {nameOf(first)},
		"incident:sla_start_resolution":     {nameOf(res)},
		"incident:sla_stop":                 both,
		"incident:sla_answered":             {"none"},
	}
	if got := targets("ana"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("the incident's reaction records name\n got %v\nwant %v", got, want)
	}

	// bob no longer sees the resolution goal: its records say restricted,
	// and the stop's two records still stand in the same order.
	if _, err := as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: nameOf(res), RequestId: newID(), Policy: &kernelv1.PolicyChange{Change: &kernelv1.PolicyChange_Narrow{
			Narrow: &kernelv1.Readers{Principals: []string{"user:ana"}}}},
	})); err != nil {
		t.Fatalf("narrow the resolution goal: %v", err)
	}
	hidden := func(name string) string {
		if name == nameOf(res) {
			return "restricted"
		}
		return name
	}
	want["incident:sla_start_resolution"] = []string{"restricted"}
	want["incident:sla_stop"] = []string{hidden(both[0]), hidden(both[1])}
	if got := targets("bob"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("for bob, who cannot see the resolution goal:\n got %v\nwant %v", got, want)
	}
	// Reading the history is not reading the goal it names.
	_, err = as("bob").GetItem(testCtx(t), connect.NewRequest(&kernelv1.GetItemRequest{Name: nameOf(res)}))
	notFound(t, "bob reads the hidden goal", err)
}

// --- nobody to tell ---

func TestListItemEvents_HookFailedNoRecipient(t *testing.T) {
	desk := itDesk(t)
	_, _, res := startedIncident(t, desk, nil) // nobody is assigned
	goal := get(t, "ana", nameOf(res))
	due, ok := typedef.ParseDateTime(goal.Fields.AsMap()["f_due_at"])
	if !ok {
		t.Fatalf("the goal's f_due_at = %v", goal.Fields.AsMap()["f_due_at"])
	}
	ty, err := k.Type(testCtx(t), "sla_goal", 1)
	if err != nil {
		t.Fatalf("the sla_goal Type: %v", err)
	}
	running, _ := ty.State("running")
	timer, hook := running.Acting[0].ID, running.Acting[0].Hook
	late := itemservice.NewWithOptions(pool, itemservice.Options{ClockSkew: time.Hour})
	if out, err := late.Internal().ActAt(testCtx(t), itemservice.ActAtRequest{ItemID: res, Timer: timer, EnteredSeq: 1, Instant: due,
		Attribution: item.Attribution{Hop: 2}}); err != nil || !out.RunHook {
		t.Fatalf("the breach: %+v, %v", out, err)
	}
	hookP := item.Principal{Actor: "hook:sla_goal:" + hook}
	cause := item.Attribution{OriginatingHook: "hook:sla_goal@v1:" + hook, CausationID: res.String() + ":1", Hop: 3}
	if _, err := k.Internal().HookInput(testCtx(t), itemservice.HookInputRequest{Principal: hookP, ItemID: res, Attribution: cause}); err == nil {
		t.Fatal("the breach hook has an input with nobody assigned")
	}
	if _, err := k.Internal().HookFailed(testCtx(t), itemservice.HookFailedRequest{
		IdempotencyKey: fmt.Sprintf("timer:%s:%s:1:%s:hook", res, timer, due.UTC().Format(typedef.DateTimeLayout)),
		Principal:      hookP, ItemID: res, Reason: itemservice.ReasonNoRecipient, Timer: timer, Attribution: cause,
	}); err != nil {
		t.Fatalf("HookFailed: %v", err)
	}

	evs := events(t, "ana", nameOf(res))
	last := evs[len(evs)-1]
	if last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_HOOK_FAILED || last.Reason != "NO_RECIPIENT" || last.Hook != hook || last.Timer != timer {
		t.Errorf("the goal's last event = %v; want HOOK_FAILED / NO_RECIPIENT naming the hook and its timer", last)
	}
	// And the mark before it: an update by system:timer holding the instant.
	mark := evs[len(evs)-2]
	if mark.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_UPDATED || mark.Actor != "system:timer" ||
		mark.Fields.AsMap()["f_breached_at"] != goal.Fields.AsMap()["f_due_at"] {
		t.Errorf("the breach mark = %v; want f_breached_at = the due instant, by system:timer", mark)
	}
}
