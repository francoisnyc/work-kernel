// Tests for Jira part 2, step 3a, through the public API, with test Types.
//
//   - GetType shows what a Type declares: a reaction that moves children
//     or sets a field on the container, by: not_creator, a consent by
//     reaction ID, the fields reactions may read, and an acting timer at
//     an instant.
//   - History: a set reaction's write and a timer's write are field
//     updates with their actor; the new reasons read in upper case.
//   - An Item that must be a calendar is refused when it is not.
//
// A package of its own: Types are global to a database, and package api's
// tests list exactly the repository's Types. Here the test Types (a job, a
// yard with hours, meters) are registered beside them.
package apistep3_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const a3Types = `
id: a3hours
version: 1
fields:
  - {id: f_name,     type: string}
  - {id: f_week,     type: string, multiple: true, max: 21}
  - {id: f_days_off, type: date,   multiple: true, max: 366}
  - {id: f_zone,     type: string}
readable_by_reactions: [f_week, f_days_off, f_zone]
invariants:
  - {id: hours_invalid, rule: 'calendar(f_week, f_days_off, f_zone) != null'}
---
id: a3yard
version: 1
fields:
  - {id: f_name, type: string}
links:
  - {name: hours, inverse: yards, targets: [a3hours], max: one}
---
id: a3remark
version: 1
fields:
  - {id: f_public, type: bool}
---
id: a3job
version: 1
initial_state: open
fields:
  - {id: f_title,    type: string}
  - {id: f_budget_s, type: int}
  - {id: f_due,      type: datetime, writable_by: kernel}
states:
  - {id: open,    category: todo}
  - {id: waiting, category: waiting}
transitions:
  - {from: open,    to: waiting}
  - {from: waiting, to: open}
reactions:
  - id: start
    kind: create
    on: enter
    state: open
    type: a3meter
    version: 1
    fields:
      f_kind: '"main"'
      f_started_at: 'cause.at'
      f_week: 'container.linked.hours.f_week'
      f_zone: 'container.linked.hours.f_zone'
      f_due_at: 'working_after(cause.at, f_budget_s, calendar(container.linked.hours.f_week, container.linked.hours.f_days_off, container.linked.hours.f_zone))'
  - {id: pause, kind: move, on: enter, state: waiting, children: {of: [a3meter], when: 'child.f_kind == "main"'}, to: paused, target_from: [running]}
  - {id: answered, kind: move, on: created_inside, of: [a3remark], when: 'child.f_public == true', by: not_creator,
     children: {of: [a3meter]}, to: paused, target_from: [running]}
accepts_reactions:
  a3meter:show: {sets: [f_due]}
---
id: a3meter
version: 1
initial_state: running
fields:
  - {id: f_kind,       type: enum, options: [main, reply]}
  - {id: f_started_at, type: datetime, writable_by: kernel}
  - {id: f_due_at,     type: datetime, writable_by: kernel}
  - {id: f_late_at,    type: datetime, writable_by: kernel}
  - {id: f_week,       type: string, multiple: true, max: 21, writable_by: kernel}
  - {id: f_zone,       type: string, writable_by: kernel}
states:
  - id: running
    category: active
    acting:
      - {id: late, at: f_due_at, when: 'f_late_at == null', sets: {f_late_at: 'cause.at'}}
  - {id: paused, category: waiting}
transitions:
  - {from: running, to: paused}
reactions:
  - {id: show, kind: set, on: enter, state: running, target: container, set: {f_due: 'f_due_at'}}
accepts_reactions:
  '*:start':    {creates: [f_kind, f_started_at, f_due_at, f_week, f_zone]}
  '*:pause':    {moves: [paused]}
  '*:answered': {moves: [paused]}
`

// registerA3 checks the test Types are there: TestMain registered them.
func registerA3(t *testing.T) {
	t.Helper()
	if _, err := k.Type(testCtx(t), "a3job", 1); err != nil {
		t.Fatalf("the step 3 test Types are not registered: %v", err)
	}
}

func a3(id string) string { return "namespaces/acme/types/" + id + "/versions/1" }

func mkA3(t *testing.T, who, typ, container string, f map[string]any) (string, error) {
	t.Helper()
	res, err := as(who).CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: a3(typ), Container: container, Fields: fields(t, f),
	}))
	if err != nil {
		return "", err
	}
	return res.Msg.Item.Name, nil
}

func mustMkA3(t *testing.T, who, typ, container string, f map[string]any) string {
	t.Helper()
	name, err := mkA3(t, who, typ, container, f)
	if err != nil {
		t.Fatalf("CreateItem %s as %s: %v", typ, who, err)
	}
	return name
}

var a3Always = []any{"mon 00:00-24:00", "tue 00:00-24:00", "wed 00:00-24:00", "thu 00:00-24:00", "fri 00:00-24:00", "sat 00:00-24:00", "sun 00:00-24:00"}

// a3Yard is a yard linked to hours open around the clock.
func a3Yard(t *testing.T) string {
	t.Helper()
	hours := mustMkA3(t, "ana", "a3hours", "", map[string]any{"f_name": "always", "f_week": a3Always, "f_zone": "UTC"})
	yard := mustMkA3(t, "ana", "a3yard", "", map[string]any{"f_name": "yard " + word()})
	if _, err := as("ana").LinkItems(testCtx(t), connect.NewRequest(&kernelv1.LinkItemsRequest{
		Name: yard, RequestId: newID(), LinkType: "hours", Target: hours,
	})); err != nil {
		t.Fatalf("LinkItems: %v", err)
	}
	return yard
}

func TestGetType_ShowsStep3Declarations(t *testing.T) {
	registerA3(t)
	job := typeNamed(t, a3("a3job"))
	byID := map[string]*kernelv1.Reaction{}
	for _, r := range job.Reactions {
		byID[r.Id] = r
	}
	pause := byID["pause"]
	if pause == nil || pause.Kind != kernelv1.ReactionKind_REACTION_KIND_MOVE ||
		pause.Trigger != kernelv1.ReactionTrigger_REACTION_TRIGGER_ENTER || pause.State != "waiting" || pause.LinkType != "" ||
		!slices.Equal(pause.ChildrenOfTypes, []string{"namespaces/acme/types/a3meter"}) || pause.ChildrenWhen != `child.f_kind == "main"` ||
		pause.ToState != "paused" || !slices.Equal(pause.TargetFromStates, []string{"running"}) {
		t.Errorf("a3job pause = %v", pause)
	}
	answered := byID["answered"]
	if answered == nil || answered.Trigger != kernelv1.ReactionTrigger_REACTION_TRIGGER_CREATED_INSIDE || answered.By != "not_creator" ||
		answered.When != "child.f_public == true" || !slices.Equal(answered.ChildrenOfTypes, []string{"namespaces/acme/types/a3meter"}) ||
		answered.ChildrenWhen != "" {
		t.Errorf("a3job answered = %v", answered)
	}
	start := byID["start"]
	if start == nil || start.Kind != kernelv1.ReactionKind_REACTION_KIND_CREATE || len(start.FromStates) != 0 ||
		start.CreateFields["f_zone"] != "container.linked.hours.f_zone" || !strings.Contains(start.CreateFields["f_due_at"], "working_after(") {
		t.Errorf("a3job start = %v", start)
	}
	var sets *kernelv1.AcceptedReaction
	for _, a := range job.AcceptsReactions {
		if a.Reaction == "a3meter:show" {
			sets = a
		}
	}
	if sets == nil || !slices.Equal(sets.Sets, []string{"f_due"}) || sets.Creates || len(sets.Moves) != 0 || len(sets.Fields) != 0 {
		t.Errorf("a3job accepts = %v, want a3meter:show setting f_due", sets)
	}

	meter := typeNamed(t, a3("a3meter"))
	var show *kernelv1.Reaction
	for _, r := range meter.Reactions {
		if r.Id == "show" {
			show = r
		}
	}
	if show == nil || show.Kind != kernelv1.ReactionKind_REACTION_KIND_SET || show.Trigger != kernelv1.ReactionTrigger_REACTION_TRIGGER_ENTER ||
		show.State != "running" || show.Target != "container" || len(show.SetFields) != 1 || show.SetFields["f_due"] != "f_due_at" {
		t.Errorf("a3meter show = %v", show)
	}
	wild := map[string]*kernelv1.AcceptedReaction{}
	for _, a := range meter.AcceptsReactions {
		wild[a.Reaction] = a
	}
	if a := wild["*:pause"]; a == nil || !slices.Equal(a.Moves, []string{"paused"}) {
		t.Errorf("a3meter accepts *:pause = %v, want moves [paused]", a)
	}
	if a := wild["*:start"]; a == nil || !a.Creates || !slices.Contains(a.CreatesFields, "f_due_at") {
		t.Errorf("a3meter accepts *:start = %v, want creates", a)
	}
	var running *kernelv1.State
	for _, s := range meter.States {
		if s.Id == "running" {
			running = s
		}
	}
	if running == nil || len(running.Acting) != 1 {
		t.Fatalf("a3meter running = %v, want one acting timer", running)
	}
	late := running.Acting[0]
	if late.Id != "late" || late.AtField != "f_due_at" || late.When != "f_late_at == null" || len(late.Sets) != 1 ||
		late.Sets["f_late_at"] != "cause.at" || late.Hook != "" || late.After != "" || late.Every != "" || late.Max != 0 {
		t.Errorf("a3meter late = %v", late)
	}
	if got := typeNamed(t, a3("a3hours")).ReadableByReactions; !slices.Equal(got, []string{"f_week", "f_days_off", "f_zone"}) {
		t.Errorf("a3hours readable_by_reactions = %v", got)
	}
	// A duration acting timer still shows as before, and now names its hook.
	for _, s := range typeNamed(t, "namespaces/acme/types/vacation_request/versions/3").States {
		if s.Id != "submitted" {
			continue
		}
		if len(s.Acting) != 1 || s.Acting[0].After != "48h" || s.Acting[0].Max != 2 || s.Acting[0].AtField != "" || s.Acting[0].Hook != "remind_approver" {
			t.Errorf("vacation_request@3 submitted acting = %v", s.Acting)
		}
	}
}

// An Item that must be a calendar: refused when it is not, naming the rule.
func TestCalendarInvariant_API(t *testing.T) {
	registerA3(t)
	for name, f := range map[string]map[string]any{
		"an unknown zone":       {"f_week": a3Always, "f_zone": "Europe/Atlantis"},
		"no working time":       {"f_zone": "UTC"},
		"a line that is no day": {"f_week": []any{"someday 09:00-17:00"}, "f_zone": "UTC"},
	} {
		_, err := mkA3(t, "ana", "a3hours", "", f)
		ce := apiError(t, name, err, connect.CodeFailedPrecondition, "INVARIANT_FAILED")
		if ei := errorInfo(t, ce); ei.Metadata["invariant"] != "hours_invalid" {
			t.Errorf("%s: metadata = %v, want invariant hours_invalid", name, ei.Metadata)
		}
	}
	mustMkA3(t, "ana", "a3hours", "", map[string]any{"f_week": []any{"mon 09:00-17:00"}, "f_days_off": []any{"2026-12-25"}, "f_zone": "Europe/Paris"})
}

// What the step's kernel writes look like in an Item's history.
func TestStep3_API_ShowsInHistory(t *testing.T) {
	registerA3(t)
	yard := a3Yard(t)
	job := mustMkA3(t, "ana", "a3job", yard, map[string]any{"f_title": "gate " + word(), "f_budget_s": 600})
	jobID := itemID(t, job)
	run := func(source uuid.UUID, reaction string, cause uuid.UUID, seq int64, hop int) itemservice.ReactionRunRequest {
		return itemservice.ReactionRunRequest{SourceID: source, Reaction: reaction, CauseItem: cause, CauseSeq: seq, Hop: hop}
	}

	// Created: the meter, on the yard's hours.
	out, err := k.Internal().ReactCreate(testCtx(t), run(jobID, "start", jobID, 1, 1))
	if err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("start: %+v, %v", out, err)
	}
	list, err := as("ana").ListItems(testCtx(t), connect.NewRequest(&kernelv1.ListItemsRequest{Parent: job}))
	if err != nil {
		t.Fatalf("ListItems: %v", err)
	}
	if len(list.Msg.Items) != 1 || list.Msg.Items[0].Type != a3("a3meter") || list.Msg.Items[0].State != "running" {
		t.Fatalf("the job's contained Items = %v; want one running a3meter", list.Msg.Items)
	}
	meter := list.Msg.Items[0]
	meterID := itemID(t, meter.Name)
	mf := meter.Fields.AsMap()
	if mf["f_zone"] != "UTC" || len(strs(mf["f_week"])) != 7 || mf["f_due_at"] == nil {
		t.Errorf("the meter's fields = %v", mf)
	}

	// The meter shows its due instant on the job: a field update by the
	// meter's reaction. Nobody else writes that field.
	if out, err := k.Internal().ReactSet(testCtx(t), run(meterID, "show", meterID, 1, 2)); err != nil || out.Outcome != itemservice.ReactionApplied {
		t.Fatalf("show: %+v, %v", out, err)
	}
	if got := get(t, "ana", job).Fields.AsMap()["f_due"]; got == nil || got != mf["f_due_at"] {
		t.Errorf("the job's f_due = %v, want the meter's f_due_at %v", got, mf["f_due_at"])
	}
	_, err = as("ana").UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: job, RequestId: newID(), Fields: fields(t, map[string]any{"f_due": "2030-01-01T00:00:00Z"}),
	}))
	apiError(t, "f_due by hand", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")

	// An hour later the meter is late: a field update by system:timer,
	// naming its timer, with the instant as the value.
	late := itemservice.NewWithOptions(pool, itemservice.Options{ClockSkew: time.Hour})
	due, _ := typedef.ParseDateTime(mf["f_due_at"])
	if res, err := late.Internal().ActAt(testCtx(t), itemservice.ActAtRequest{
		ItemID: meterID, Timer: "late", EnteredSeq: 1, Instant: due, Attribution: item.Attribution{Hop: 2},
	}); err != nil || !res.Applied {
		t.Fatalf("ActAt: %+v, %v", res, err)
	}

	// The job waits: its one meter is paused, one record.
	waiting := transition(t, "ana", job, "waiting")
	if out, err := k.Internal().ReactMove(testCtx(t), run(jobID, "pause", jobID, waiting.ItemSeq, 1)); err != nil ||
		out.Outcome != itemservice.ReactionApplied || len(out.Each) != 1 {
		t.Fatalf("pause: %+v, %v", out, err)
	}

	var jobKinds []string
	var set *kernelv1.ItemEvent
	for _, ev := range events(t, "ana", job) {
		switch ev.Kind {
		case kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_APPLIED:
			jobKinds = append(jobKinds, "applied "+ev.Reaction)
		case kernelv1.ItemEventKind_ITEM_EVENT_KIND_UPDATED:
			set = ev
		}
	}
	if want := []string{"applied a3job:start", "applied a3job:pause"}; !slices.Equal(jobKinds, want) {
		t.Errorf("the job's reaction records = %v, want %v", jobKinds, want)
	}
	if set == nil || set.Actor != "reaction:a3meter:show" || set.CausationId != meterID.String()+":1" ||
		set.Fields.AsMap()["f_due"] != mf["f_due_at"] {
		t.Errorf("the job's update = %v, want f_due set by the meter's reaction, caused by the meter's create", set)
	}

	var timer, paused *kernelv1.ItemEvent
	for _, ev := range events(t, "ana", meter.Name) {
		switch {
		case ev.Kind == kernelv1.ItemEventKind_ITEM_EVENT_KIND_UPDATED && ev.Actor == "system:timer":
			timer = ev
		case ev.Kind == kernelv1.ItemEventKind_ITEM_EVENT_KIND_TRANSITIONED:
			paused = ev
		}
	}
	if timer == nil || timer.Timer != "late" || timer.Fields.AsMap()["f_late_at"] != mf["f_due_at"] ||
		timer.CausationId != meterID.String()+":1" {
		t.Errorf("the timer's event = %v, want an update naming timer late, f_late_at = the due instant", timer)
	}
	if paused == nil || paused.Actor != "reaction:a3job:pause" || paused.OnBehalfOf != "user:ana" || paused.ToState != "paused" {
		t.Errorf("the meter's move = %v, want the job's reaction, for ana", paused)
	}

	// The new reasons, in upper case like every event reason.
	alone := mustMkA3(t, "ana", "a3job", "", map[string]any{"f_title": "no yard", "f_budget_s": 60})
	if out, err := k.Internal().ReactCreate(testCtx(t), run(itemID(t, alone), "start", itemID(t, alone), 1, 1)); err != nil ||
		out.Outcome != itemservice.ReactionFailed {
		t.Fatalf("start with no container: %+v, %v", out, err)
	}
	evs := events(t, "ana", alone)
	if len(evs) == 0 {
		t.Fatal("the job has no events")
	}
	if last := evs[len(evs)-1]; last.Kind != kernelv1.ItemEventKind_ITEM_EVENT_KIND_REACTION_FAILED || last.Reason != "NO_CONTAINER" {
		t.Errorf("the record = %v, want REACTION_FAILED / NO_CONTAINER", last)
	}
}
