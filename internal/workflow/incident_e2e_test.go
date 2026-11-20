// End-to-end tests for Jira part 2, step 3b: the `sla` package and its
// first host on the repository's own Type files (types/sla/, desk@1,
// incident@1), with Postgres, the relay, a real Temporal server and
// worker, and the email stub. Nothing here calls the internal door: the
// worker runs every reaction, timer and hook.
//
//   - An incident starts a first-response and a resolution goal when it is
//     created, with the targets its desk's policy gives its priority; each
//     shows its due instant on the incident.
//   - A public comment by someone else stops first response. Waiting for
//     the customer pauses resolution, and only resolution; the requester's
//     public comment sets the incident back in progress, which resumes it.
//     Resolving stops the goals; reopening starts a new resolution goal.
//   - At its due instant, never before, a goal is marked breached with
//     that instant, goes on running, and the incident's assignee at that
//     moment is emailed once. With nobody to tell: no mail, one
//     hook_failed no_recipient, no retry.
//   - Canceling ends the goals with no outcome, and nothing is told later.
//   - A resolved incident closes itself 120 hours later (on a scaled clock).
//
// The calendar is open around the clock in UTC and targets are seconds.
package workflow_test

import (
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/connstub"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

var registerIncidentOnce sync.Once

type incStack struct {
	*stack
	mail                 *connstub.Server
	admin, ana, sam, eve item.Principal
	desk                 uuid.UUID
	// hook is the breach hook's ID, which is also the email's template.
	hook string
}

// Targets in seconds. P1 breaches while a test watches; P4 never does.
const (
	p1First, p1Resolution = 2, 4
	p4First, p4Resolution = 3600, 7200
)

// newIncStack is a stack whose worker has the email stub, with a desk
// linked to an always-open calendar and a policy. devDay, when not zero,
// scales the incident's declared durations (a day lasts devDay).
func newIncStack(t *testing.T, devDay time.Duration) *incStack {
	t.Helper()
	registerIncidentOnce.Do(func() {
		for _, f := range []string{"bug.v3.yaml", "comment.v2.yaml", "sla/calendar.v1.yaml", "sla/sla_policy.v1.yaml",
			"sla/sla_goal.v1.yaml", "desk.v1.yaml", "incident.v1.yaml"} {
			registerTypeFile(t, f)
		}
	})
	if _, err := itemservice.New(pool).Type(serverCtx(t), "incident", 1); err != nil {
		t.Fatalf("the incident Type is not registered: %v", err)
	}
	s := &incStack{stack: newStack(t), mail: connstub.NewEmail(),
		admin: user("admin"), ana: user("ana"), sam: user("sam"), eve: user("eve")}
	t.Cleanup(s.mail.Close)
	s.stopWorker()
	opts := wf.WorkerOptions{
		Connectors: hooks.NewConnectors("http://127.0.0.1:1", "http://127.0.0.1:1").WithEmail(s.mail.URL), HookBudget: testBudget,
	}
	if devDay > 0 {
		opts.DevDay, opts.DevTypes = devDay, []string{"incident"}
	}
	s.worker = wf.NewWorker(tc, s.queue, s.k, opts)
	if err := s.worker.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	if _, err := pool.Exec(serverCtx(t),
		`insert into principal_roles (namespace_id, principal, role) values ('default', $1, 'sla_admin')`, s.admin.Actor); err != nil {
		t.Fatalf("grant sla_admin: %v", err)
	}
	cal := s.makeIn(s.admin, "calendar", nil, map[string]any{"f_name": "always", "f_zone": "UTC", "f_week": []any{
		"mon 00:00-24:00", "tue 00:00-24:00", "wed 00:00-24:00", "thu 00:00-24:00", "fri 00:00-24:00", "sat 00:00-24:00", "sun 00:00-24:00"}})
	policy := s.makeIn(s.admin, "sla_policy", nil, map[string]any{"f_name": "seconds",
		"f_first_response_s_p1": p1First, "f_resolution_s_p1": p1Resolution,
		"f_first_response_s_p2": 600, "f_resolution_s_p2": 1200, "f_first_response_s_p3": 900, "f_resolution_s_p3": 1800,
		"f_first_response_s_p4": p4First, "f_resolution_s_p4": p4Resolution})
	s.desk = s.makeIn(s.admin, "desk", nil, map[string]any{"f_key": "IT", "f_name": "IT"})
	for link, target := range map[string]uuid.UUID{"calendar": cal, "sla_policy": policy} {
		if _, err := s.k.Link(serverCtx(t), itemservice.LinkRequest{
			IdempotencyKey: "key-" + uuid.NewString(), Principal: s.admin, SourceID: s.desk, LinkType: link, TargetID: target,
		}); err != nil {
			t.Fatalf("link the desk's %s: %v", link, err)
		}
	}
	ty, _ := s.k.Type(serverCtx(t), "sla_goal", 1)
	running, _ := ty.State("running")
	if len(running.Acting) != 1 {
		t.Fatalf("sla_goal's running state has %d acting timers, want 1", len(running.Acting))
	}
	s.hook = running.Acting[0].Hook
	return s
}

// goalOf finds the incident's newest goal of a metric; uuid.Nil if none.
func goalOf(t *testing.T, inc uuid.UUID, metric string) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	rows, err := pool.Query(serverCtx(t), `select id from items where container_id = $1 and type_id = 'sla_goal'
		and fields->>'f_metric' = $2 order by fields->>'f_started_at' desc, id desc limit 1`, inc, metric)
	if err != nil {
		t.Fatalf("find the goal: %v", err)
	}
	defer rows.Close()
	if rows.Next() {
		if err := rows.Scan(&id); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}
	return id
}

func goalCount(t *testing.T, inc uuid.UUID, metric string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(serverCtx(t), `select count(*) from items where container_id = $1 and type_id = 'sla_goal'
		and fields->>'f_metric' = $2`, inc, metric).Scan(&n); err != nil {
		t.Fatalf("count the goals: %v", err)
	}
	return n
}

// f reads a field as the admin, who sees everything in these tests.
func (s *incStack) f(id uuid.UUID, name string) any {
	s.t.Helper()
	it, err := s.k.Get(serverCtx(s.t), s.admin, id)
	if err != nil {
		s.t.Fatalf("Get: %v", err)
	}
	return it.Fields[name]
}

func (s *incStack) is(id uuid.UUID, want string) func() bool {
	return func() bool { st, _ := s.state(id); return st == want }
}

// started is ana's incident of a priority with both goals created and
// shown on it.
func (s *incStack) started(prio string, more map[string]any) (inc, first, res uuid.UUID) {
	s.t.Helper()
	fields := map[string]any{"f_title": "VPN down", "f_priority": prio}
	for k, v := range more {
		fields[k] = v
	}
	inc = s.makeIn(s.ana, "incident", &s.desk, fields)
	eventually(s.t, 20*time.Second, "both goals created", func() bool {
		first, res = goalOf(s.t, inc, "first_response"), goalOf(s.t, inc, "resolution")
		return first != uuid.Nil && res != uuid.Nil
	})
	return inc, first, res
}

func (s *incStack) act(p item.Principal, id uuid.UUID, to string, fields map[string]any) itemservice.TransitionResult {
	s.t.Helper()
	res, err := s.k.Transition(serverCtx(s.t), itemservice.TransitionRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, ItemID: id, ToState: to, Fields: fields,
	})
	if err != nil {
		s.t.Fatalf("move to %s as %s: %v", to, p.Actor, err)
	}
	return res
}

func (s *incStack) comment(p item.Principal, inc uuid.UUID, public bool) uuid.UUID {
	s.t.Helper()
	res, err := s.k.Create(serverCtx(s.t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: p, TypeID: "comment", TypeVersion: 2, ContainerID: &inc,
		Fields: map[string]any{"f_body": "a word", "f_public": public},
	})
	if err != nil {
		s.t.Fatalf("comment: %v", err)
	}
	return res.ItemID
}

// told lists who was emailed about a goal's breach.
func (s *incStack) told(goal uuid.UUID) []string {
	var to []string
	for _, msg := range s.mail.Messages(goal.String()) {
		if msg.Template == s.hook {
			to = append(to, msg.To)
		}
	}
	return to
}

func opsOf(t *testing.T, id uuid.UUID, op string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, ev := range allEvents(t, id) {
		if ev.op == op {
			out = append(out, ev.payload)
		}
	}
	return out
}

type incEvent struct {
	seq     int64
	op      string
	actor   string
	at      time.Time
	payload map[string]any
}

func allEvents(t *testing.T, id uuid.UUID) []incEvent {
	t.Helper()
	rows, err := pool.Query(serverCtx(t),
		`select item_seq, op, actor, created_at, payload from item_events where item_id = $1 order by item_seq`, id)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	defer rows.Close()
	var out []incEvent
	for rows.Next() {
		var ev incEvent
		if err := rows.Scan(&ev.seq, &ev.op, &ev.actor, &ev.at, &ev.payload); err != nil {
			t.Fatalf("scan event: %v", err)
		}
		out = append(out, ev)
	}
	return out
}

func after(at time.Time, seconds int) string {
	return at.Add(time.Duration(seconds) * time.Second).UTC().Format(typedef.DateTimeLayout)
}

// --- done when ---

func TestE2E_Incident_StartsItsGoals_FromThePolicyByPriority(t *testing.T) {
	s := newIncStack(t, 0)
	inc, first, res := s.started("P4", nil)
	created := eventAt(t, inc, 1)
	if s.f(first, "f_due_at") != after(created, p4First) || s.f(res, "f_due_at") != after(created, p4Resolution) {
		t.Errorf("due instants: first response %v, resolution %v; want the incident's create time %v plus %d s and %d s",
			s.f(first, "f_due_at"), s.f(res, "f_due_at"), created, p4First, p4Resolution)
	}
	eventually(t, 20*time.Second, "both due instants shown on the incident", func() bool {
		return s.f(inc, "f_due_first_response") == after(created, p4First) && s.f(inc, "f_due_resolution") == after(created, p4Resolution)
	})
	want := []string{"reaction_applied incident:sla_start_first_response", "reaction_applied incident:sla_start_resolution"}
	if got := records(t, inc); !slices.Equal(got, want) {
		t.Errorf("the incident's records = %v, want %v", got, want)
	}
	// Each copy was written by the goal's own reaction, two hops from the
	// incident's create; nobody else wrote the incident.
	var sets int
	for _, ev := range allEvents(t, inc) {
		if ev.op == "update" {
			sets++
			if len(ev.actor) < len("reaction:sla_goal:") || ev.actor[:len("reaction:sla_goal:")] != "reaction:sla_goal:" {
				t.Errorf("the incident was updated by %s, want a reaction of sla_goal", ev.actor)
			}
		}
	}
	if sets != 2 {
		t.Errorf("%d writes of the due instants on the incident, want 2", sets)
	}
	// A P1 gets the P1 targets.
	urgent, _, ures := s.started("P1", nil)
	if s.f(ures, "f_due_at") != after(eventAt(t, urgent, 1), p1Resolution) {
		t.Errorf("a P1's resolution is due %v, want %d s after its creation", s.f(ures, "f_due_at"), p1Resolution)
	}
}

func TestE2E_Incident_AnswerWaitResumeResolveReopen(t *testing.T) {
	s := newIncStack(t, 0)
	inc, first, res := s.started("P4", map[string]any{"f_assignee": s.sam.Actor})
	created := eventAt(t, inc, 1)
	s.act(s.sam, inc, "in_progress", nil)

	// ana's own comment, and sam's private note, answer nothing.
	s.comment(s.ana, inc, true)
	s.comment(s.sam, inc, false)
	staysFor(t, 2*time.Second, "first response still running", s.is(first, "running"))
	// sam's public answer stops first response: met, timed at the comment.
	answer := s.comment(s.sam, inc, true)
	eventually(t, 20*time.Second, "first response stopped", s.is(first, "stopped"))
	if s.f(first, "f_outcome") != "met" || s.f(first, "f_breached_at") != nil {
		t.Errorf("first response: %v, breached %v; want met, never breached", s.f(first, "f_outcome"), s.f(first, "f_breached_at"))
	}
	if got, _ := typedef.AsInt(s.f(first, "f_elapsed_s")); got != int64(eventAt(t, answer, 1).Sub(created)/time.Second) {
		t.Errorf("first response took %v s, want the time from the incident's creation to the answer's", s.f(first, "f_elapsed_s"))
	}
	eventually(t, 15*time.Second, "f_due_first_response unset", func() bool { return s.f(inc, "f_due_first_response") == nil })
	if st, _ := s.state(res); st != "running" || s.f(inc, "f_due_resolution") == nil {
		t.Errorf("resolution is %s, shown %v; want running and shown: an answer is not a resolution", st, s.f(inc, "f_due_resolution"))
	}

	// Waiting for the customer pauses resolution.
	waiting := s.act(s.sam, inc, "waiting_for_customer", nil)
	eventually(t, 20*time.Second, "resolution paused", s.is(res, "paused"))
	eventually(t, 15*time.Second, "f_due_resolution unset while waiting", func() bool { return s.f(inc, "f_due_resolution") == nil })
	elapsed, _ := typedef.AsInt(s.f(res, "f_elapsed_s"))
	if want := int64(eventAt(t, inc, waiting.ItemSeq).Sub(created) / time.Second); elapsed != want {
		t.Errorf("f_elapsed_s = %d, want %d", elapsed, want)
	}
	// Someone else's comment does not bring the incident back.
	s.comment(s.sam, inc, true)
	staysFor(t, 2*time.Second, "still waiting", s.is(inc, "waiting_for_customer"))

	// The requester's public comment does, and resolution resumes with
	// what was left, from the event that brought the incident back.
	s.comment(s.ana, inc, true)
	eventually(t, 20*time.Second, "the incident back in progress", s.is(inc, "in_progress"))
	_, backSeq := s.state(inc)
	var back incEvent
	for _, ev := range allEvents(t, inc) {
		if ev.op == "transition" && ev.payload["to"] == "in_progress" && ev.payload["from"] == "waiting_for_customer" {
			back = ev
		}
	}
	if back.seq == 0 || len(back.actor) < len("reaction:incident:") || back.actor[:len("reaction:incident:")] != "reaction:incident:" {
		t.Fatalf("the move back = %+v (the incident is at %d); want made by the incident's own reaction", back, backSeq)
	}
	eventually(t, 20*time.Second, "resolution running again", s.is(res, "running"))
	due := after(back.at, p4Resolution-int(elapsed))
	if got := s.f(res, "f_due_at"); got != due {
		t.Errorf("f_due_at after the resume = %v, want %s", got, due)
	}
	eventually(t, 15*time.Second, "f_due_resolution shown again", func() bool { return s.f(inc, "f_due_resolution") == due })

	// Resolved: met. The incident shows nothing more.
	s.act(s.sam, inc, "resolved", map[string]any{"f_resolution": "fixed"})
	eventually(t, 20*time.Second, "resolution stopped", s.is(res, "stopped"))
	if s.f(res, "f_outcome") != "met" {
		t.Errorf("resolution: %v, want met", s.f(res, "f_outcome"))
	}
	eventually(t, 15*time.Second, "f_due_resolution unset at the end", func() bool { return s.f(inc, "f_due_resolution") == nil })

	// Reopened: a second resolution goal, a whole new target; first
	// response is measured once.
	reopened := s.act(s.ana, inc, "in_progress", nil)
	var second uuid.UUID
	eventually(t, 20*time.Second, "a second resolution goal", func() bool {
		second = goalOf(t, inc, "resolution")
		return second != res && second != uuid.Nil
	})
	if got := s.f(second, "f_due_at"); got != after(eventAt(t, inc, reopened.ItemSeq), p4Resolution) {
		t.Errorf("the second cycle is due %v, want %d s after the reopening", got, p4Resolution)
	}
	eventually(t, 15*time.Second, "the second cycle shown on the incident", func() bool {
		return s.f(inc, "f_due_resolution") == s.f(second, "f_due_at")
	})
	staysFor(t, 2*time.Second, "one first response goal, two resolution goals, the first cycle as it ended", func() bool {
		st, _ := s.state(res)
		return goalCount(t, inc, "first_response") == 1 && goalCount(t, inc, "resolution") == 2 && st == "stopped" &&
			s.f(inc, "f_due_first_response") == nil
	})
	if got := s.told(res); len(got)+len(s.told(first))+len(s.told(second)) != 0 {
		t.Errorf("emails about goals that never breached: %v", got)
	}
}

func TestE2E_Incident_Breach_TellsTheAssigneeOnce_ThenMissed(t *testing.T) {
	s := newIncStack(t, 0)
	inc, first, res := s.started("P1", map[string]any{"f_assignee": s.sam.Actor})
	created := eventAt(t, inc, 1)
	due := created.Add(p1Resolution * time.Second)
	if time.Now().Before(due.Add(-time.Second)) && (s.f(res, "f_breached_at") != nil || len(s.told(res)) != 0) {
		t.Fatalf("breached %v, told %v, more than a second before the due instant", s.f(res, "f_breached_at"), s.told(res))
	}
	eventually(t, 25*time.Second, "resolution breached and sam told", func() bool {
		return s.f(res, "f_breached_at") != nil && len(s.told(res)) == 1
	})
	if got := s.f(res, "f_breached_at"); got != after(created, p1Resolution) {
		t.Errorf("f_breached_at = %v, want the due instant exactly", got)
	}
	if st, _ := s.state(res); st != "running" || s.f(res, "f_outcome") != nil {
		t.Errorf("after the breach the goal is %s with outcome %v; want running, none yet", st, s.f(res, "f_outcome"))
	}
	// The mark was written at or after the instant, by the timer; the
	// email after the mark.
	for _, ev := range allEvents(t, res) {
		if ev.actor == "system:timer" && ev.at.Before(due) {
			t.Errorf("the breach mark was written at %v, before the due instant %v", ev.at, due)
		}
	}
	// First response breached too, two seconds earlier: its own mail.
	eventually(t, 10*time.Second, "first response breached and sam told", func() bool {
		return s.f(first, "f_breached_at") == after(created, p1First) && len(s.told(first)) == 1
	})
	// The incident goes on showing the missed instants.
	if s.f(inc, "f_due_resolution") != after(created, p1Resolution) {
		t.Errorf("the incident's f_due_resolution = %v, want the missed instant still", s.f(inc, "f_due_resolution"))
	}
	staysFor(t, 3*time.Second, "one email per breach, both to sam", func() bool {
		return slices.Equal(s.told(res), []string{s.sam.Actor}) && slices.Equal(s.told(first), []string{s.sam.Actor})
	})
	if ran := opsOf(t, res, "hook_ran"); len(ran) != 1 || ran[0]["hook"] != s.hook {
		t.Errorf("hook_ran on the goal = %v, want one, naming %s", ran, s.hook)
	}

	// Resolved late: both goals missed, the marks kept.
	s.act(s.sam, inc, "in_progress", nil)
	s.act(s.sam, inc, "resolved", map[string]any{"f_resolution": "fixed"})
	eventually(t, 20*time.Second, "both goals stopped", func() bool { return s.is(res, "stopped")() && s.is(first, "stopped")() })
	if s.f(res, "f_outcome") != "missed" || s.f(first, "f_outcome") != "missed" || s.f(res, "f_breached_at") != after(created, p1Resolution) {
		t.Errorf("resolution %v, first response %v, mark %v; want both missed, the mark kept",
			s.f(res, "f_outcome"), s.f(first, "f_outcome"), s.f(res, "f_breached_at"))
	}
}

// The recipient is read when the hook runs: reassigned before the due
// instant, the new assignee is told, and the old one is not.
func TestE2E_Incident_Breach_TellsWhoIsAssignedAtThatMoment(t *testing.T) {
	s := newIncStack(t, 0)
	inc, _, res := s.started("P1", map[string]any{"f_assignee": s.sam.Actor})
	if _, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: s.sam, ItemID: inc, Fields: map[string]any{"f_assignee": s.eve.Actor},
	}); err != nil {
		t.Fatalf("reassign: %v", err)
	}
	eventually(t, 25*time.Second, "eve told of the breach", func() bool { return len(s.told(res)) == 1 })
	staysFor(t, 2*time.Second, "eve, once; sam, never", func() bool { return slices.Equal(s.told(res), []string{s.eve.Actor}) })
}

func TestE2E_Incident_Breach_NobodyAssigned_NoMail_OneRecord(t *testing.T) {
	s := newIncStack(t, 0)
	inc, _, res := s.started("P1", nil)
	created := eventAt(t, inc, 1)
	eventually(t, 25*time.Second, "the breach marked and the failed notification recorded", func() bool {
		return s.f(res, "f_breached_at") != nil && len(opsOf(t, res, "hook_failed")) == 1
	})
	failed := opsOf(t, res, "hook_failed")[0]
	if failed["reason"] != "no_recipient" || failed["hook"] != s.hook {
		t.Errorf("the record = %v; want hook_failed, no_recipient, naming %s", failed, s.hook)
	}
	// Assigned a moment later: nobody is told after the fact, nothing is
	// retried, and the mark stands.
	if _, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: s.ana, ItemID: inc, Fields: map[string]any{"f_assignee": s.sam.Actor},
	}); err != nil {
		t.Fatalf("assign: %v", err)
	}
	staysFor(t, 4*time.Second, "no email, one record, the mark kept", func() bool {
		return len(s.mail.Messages(res.String())) == 0 && len(opsOf(t, res, "hook_failed")) == 1 && len(opsOf(t, res, "hook_ran")) == 0 &&
			s.f(res, "f_breached_at") == after(created, p1Resolution)
	})
	if types, _ := activityFailures(t, res); len(types) != 0 {
		t.Errorf("the goal's run has failed activities %v: nobody to tell is an answer, not an error to retry", types)
	}
}

// A recipient who cannot see the incident is told nothing.
func TestE2E_Incident_Breach_AnAssigneeWhoCannotSeeIt_IsNotTold(t *testing.T) {
	s := newIncStack(t, 0)
	res, err := s.k.Create(serverCtx(t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: s.ana, TypeID: "incident", TypeVersion: 1, ContainerID: &s.desk,
		Narrow: &itemservice.Narrow{Readers: []string{s.ana.Actor, s.admin.Actor}},
		Fields: map[string]any{"f_title": "payroll", "f_priority": "P1", "f_assignee": s.sam.Actor},
	})
	if err != nil {
		t.Fatalf("create a narrowed incident: %v", err)
	}
	var goal uuid.UUID
	eventually(t, 20*time.Second, "the resolution goal", func() bool { goal = goalOf(t, res.ItemID, "resolution"); return goal != uuid.Nil })
	eventually(t, 25*time.Second, "the failed notification recorded", func() bool { return len(opsOf(t, goal, "hook_failed")) == 1 })
	if got := opsOf(t, goal, "hook_failed")[0]["reason"]; got != "no_recipient" {
		t.Errorf("reason = %v, want no_recipient", got)
	}
	staysFor(t, 2*time.Second, "no email to anyone", func() bool { return len(s.mail.Messages(goal.String())) == 0 })
	// The goal inherited the incident's readers: sam cannot see it either.
	if _, err := s.k.Get(serverCtx(t), s.sam, goal); err == nil {
		t.Error("sam can read the goal of an incident he cannot see")
	}
}

func TestE2E_Incident_Canceled_GoalsEnd_NothingIsToldLater(t *testing.T) {
	s := newIncStack(t, 0)
	inc, first, res := s.started("P1", map[string]any{"f_assignee": s.sam.Actor})
	s.act(s.ana, inc, "canceled", nil)
	eventually(t, 20*time.Second, "both goals canceled", func() bool { return s.is(first, "canceled")() && s.is(res, "canceled")() })
	eventually(t, 15*time.Second, "the incident shows no due instant", func() bool {
		return s.f(inc, "f_due_first_response") == nil && s.f(inc, "f_due_resolution") == nil
	})
	// Past both due instants: no outcome, and no breach of the resolution
	// goal, which was canceled seconds before its instant.
	staysFor(t, (p1Resolution+2)*time.Second, "no outcome, no late mark, no email", func() bool {
		return s.f(res, "f_outcome") == nil && s.f(first, "f_outcome") == nil && s.f(res, "f_breached_at") == nil && len(s.told(res)) == 0
	})
	if got := records(t, inc); !slices.Contains(got, "reaction_applied incident:sla_cancel") {
		t.Errorf("the incident's records = %v, want sla_cancel applied", got)
	}
}

// A resolved incident closes itself 120 hours later. On a clock where a
// day lasts two seconds, that is ten seconds; reopened before, it does not.
func TestE2E_Incident_ClosesItself120hAfterResolved_OnAScaledClock(t *testing.T) {
	const day = 2 * time.Second
	s := newIncStack(t, day)
	inc, _, res := s.started("P4", map[string]any{"f_assignee": s.sam.Actor})
	s.act(s.sam, inc, "in_progress", nil)
	resolved := s.act(s.sam, inc, "resolved", map[string]any{"f_resolution": "fixed"})
	at := eventAt(t, inc, resolved.ItemSeq)
	staysFor(t, 4*day, "resolved for four days", s.is(inc, "resolved"))
	eventually(t, 3*day+15*time.Second, "closed by the timer", s.is(inc, "closed"))
	var closing incEvent
	for _, ev := range allEvents(t, inc) {
		if ev.op == "transition" && ev.payload["to"] == "closed" {
			closing = ev
		}
	}
	if closing.actor != "system:timer" || closing.at.Sub(at) < 5*day {
		t.Errorf("closed by %s %v after it was resolved; want system:timer, no sooner than five days (%v)", closing.actor, closing.at.Sub(at), 5*day)
	}
	// The scale is the incident's: its goals' instants are real time, and
	// were not touched by it.
	if s.f(res, "f_outcome") != "met" || s.f(res, "f_breached_at") != nil {
		t.Errorf("the resolution goal: %v, breached %v; want met, never breached", s.f(res, "f_outcome"), s.f(res, "f_breached_at"))
	}
	// Closed is the end: no goal starts, nothing moves.
	if n := goalCount(t, inc, "resolution"); n != 1 {
		t.Errorf("%d resolution goals after closing, want 1", n)
	}

	// Reopened on the second day, an incident is not closed on the fifth.
	again, _, _ := s.started("P4", nil)
	s.act(s.sam, again, "in_progress", nil)
	s.act(s.sam, again, "resolved", map[string]any{"f_resolution": "fixed"})
	time.Sleep(day)
	s.act(s.ana, again, "in_progress", nil)
	staysFor(t, 6*day, "in progress, not closed", s.is(again, "in_progress"))
}
