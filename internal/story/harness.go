package story

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/activity"
	sdkconverter "go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/log"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/internal/api"
	"github.com/francoisnyc/work-kernel/internal/hooks"
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/relay"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// The harness's own workflow, activities and signals. They are harness
// code, not the kernel's: the driver stands in for the Temporal server's
// Update-With-Start (it starts each Item's workflow as its child), and the
// settle activity runs the real relay. While any activity runs, the test
// environment's clock does not skip, so everything a step causes happens
// at the step's instant.
const (
	driverName   = "story.driver"
	activityStep = "story.step"
	activitySet  = "story.settle"
	signalStart  = "story.start"
	signalSettle = "story.settle"
	signalStop   = "story.stop"
)

// A year after the last step: how long a story without until runs.
const defaultRun = 365 * 24 * time.Hour

// harness is one run of a story.
type harness struct {
	env   *Env
	story *Story
	opts  runOptions
	ns    string
	conv  converter
	k     *itemservice.Service
	tenv  *testsuite.TestWorkflowEnvironment
	relay *relay.Relay
	steps []Step // given, then steps

	mu        sync.Mutex
	ids       map[string]uuid.UUID             // Item name -> ID (the steps')
	names     map[uuid.UUID]string             // ID -> name (every Item, once named)
	own       map[uuid.UUID]map[int64][]string // the fields a step's own event sent
	given     map[uuid.UUID]map[int64]bool     // the events of given steps
	calls     []Call
	sideKeys  map[string]bool // side effects seen, by idempotency key
	sideLines []Call
	failures  []Failure
	current   int // the step running or last run, an index into steps; -1 before any
	running   map[string]bool
	started   map[string]chan struct{}
	runs      map[string]int
	canned    map[string]int
	lastErr   map[string]error // the relay adapter's last error, by Item
	settle    sync.Mutex       // one settle at a time: the relay is single-instance

	// The kernel's activities in flight, for settling: fresh counts those
	// scheduled whose first attempt has not started (by type), executing
	// those running an attempt now. An activity waiting to retry is in
	// neither: its retry comes later in story time.
	fresh     map[string]int
	executing int
	begun     map[string]bool // workflow/activity IDs whose first attempt started
}

func run(ctx context.Context, env *Env, s *Story, opts runOptions) (*Result, error) {
	if ps := Check(s, env.types, env.catalog); len(ps) > 0 {
		return nil, ps
	}
	h := &harness{env: env, story: s, opts: opts, conv: newConverter(s),
		ns:  "story-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:20],
		ids: map[string]uuid.UUID{}, names: map[uuid.UUID]string{}, own: map[uuid.UUID]map[int64][]string{},
		given: map[uuid.UUID]map[int64]bool{}, sideKeys: map[string]bool{}, current: -1,
		running: map[string]bool{}, started: map[string]chan struct{}{}, runs: map[string]int{}, canned: map[string]int{},
		lastErr: map[string]error{}, fresh: map[string]int{}, begun: map[string]bool{}}
	h.steps = append(slices.Clone(s.Given), s.Steps...)
	if err := h.setUp(ctx); err != nil {
		return nil, err
	}
	end := s.Until
	if end.IsZero() {
		end = s.Day1
		if len(s.Steps) > 0 {
			end = s.Steps[len(s.Steps)-1].At
		}
		end = end.Add(defaultRun)
	}

	var suite testsuite.WorkflowTestSuite
	suite.SetLogger(log.NewStructuredLogger(slog.New(slog.NewTextHandler(io.Discard, nil))))
	h.tenv = suite.NewTestWorkflowEnvironment()
	h.tenv.SetStartTime(s.Day1)
	h.tenv.SetDetachedChildWait(false)
	h.tenv.SetTestTimeout(5 * time.Minute)
	h.tenv.SetWorkerOptions(worker.Options{Interceptors: []interceptor.WorkerInterceptor{&counting{h: h}}})

	h.k = itemservice.NewWithOptions(env.pool, itemservice.Options{CacheTypes: true, Clock: h.now})
	conns := hooks.NewConnectors("http://crm.invalid", "http://erp.invalid")
	for _, f := range s.Fakes {
		connector, _, _ := strings.Cut(f.Action, ".")
		_, major, _ := strings.Cut(f.Action, "@")
		conns = conns.WithConnector(connector+"@"+major, &fake{h: h, connector: connector + "@" + major})
	}
	wf.Register(&registry{TestWorkflowEnvironment: h.tenv, h: h}, h.k, wf.WorkerOptions{Connectors: conns})
	h.tenv.RegisterWorkflowWithOptions(h.driver, workflow.RegisterOptions{Name: driverName})
	h.tenv.RegisterActivityWithOptions(h.step, activity.RegisterOptions{Name: activityStep})
	h.tenv.RegisterActivityWithOptions(h.settleAll, activity.RegisterOptions{Name: activitySet})
	h.relay = relay.New(env.pool, h, relay.Options{PollInterval: time.Millisecond, Backoff: time.Millisecond,
		MaxBackoff: 5 * time.Millisecond, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})

	h.tenv.SetOnChildWorkflowStartedListener(func(info *workflow.Info, _ workflow.Context, _ sdkconverter.EncodedValues) {
		id := info.WorkflowExecution.ID
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.running[id] {
			// Started again while it runs: the test environment's rerun of
			// a child that continued as new (it reports no completion).
			h.canned[id]++
		}
		h.running[id] = true
		h.runs[id]++
		if ch, ok := h.started[id]; ok {
			close(ch)
			delete(h.started, id)
		}
	})
	h.tenv.SetOnChildWorkflowCompletedListener(func(info *workflow.Info, _ sdkconverter.EncodedValue, err error) {
		id := info.WorkflowExecution.ID
		h.mu.Lock()
		defer h.mu.Unlock()
		delete(h.running, id)
		if err != nil {
			h.failLocked(0, "the workflow of %s failed: %v", h.nameLocked(id), err)
		}
	})
	h.tenv.SetOnActivityCanceledListener(func(info *activity.Info) {
		if !strings.HasPrefix(info.ActivityType.Name, "story.") {
			h.attemptBegins(info.WorkflowExecution.ID+"/"+info.ActivityID, info.ActivityType.Name)
		}
	})
	// Whatever an activity of the Item workflow did is delivered before
	// time moves on: its completion asks the driver to settle.
	h.tenv.SetOnActivityCompletedListener(func(info *activity.Info, _ sdkconverter.EncodedValue, _ error) {
		if !strings.HasPrefix(info.ActivityType.Name, "story.") {
			h.tenv.SignalWorkflow(signalSettle, nil)
		}
	})

	at := make([]time.Time, len(h.steps))
	for i, st := range h.steps {
		at[i] = st.At
		if i < len(s.Given) {
			at[i] = s.Day1
		}
	}
	h.tenv.ExecuteWorkflow(driverName, driverInput{Steps: at, Until: end})
	if err := h.tenv.GetWorkflowError(); err != nil {
		return nil, fmt.Errorf("story: the driver: %w", err)
	}

	res := &Result{Ended: end, Runs: map[string]int{}, ContinuedAsNew: map[string]int{}}
	h.mu.Lock()
	failed := len(h.failures) > 0
	h.mu.Unlock()
	if failed {
		res.Ended = h.now()
	}
	if err := h.timeline(ctx, res); err != nil {
		return nil, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	res.Failures = slices.Clone(h.failures)
	res.Calls = slices.Clone(h.calls)
	for id, n := range h.runs {
		res.Runs[h.nameLocked(id)] = n
	}
	for id, n := range h.canned {
		res.ContinuedAsNew[h.nameLocked(id)] = n
	}
	return res, nil
}

// setUp makes the run's namespace and gives its people their roles. What
// earlier runs left in the outbox is not this run's.
func (h *harness) setUp(ctx context.Context) error {
	k := itemservice.New(h.env.pool)
	if err := k.EnsureNamespace(ctx, h.ns); err != nil {
		return fmt.Errorf("story: the namespace: %w", err)
	}
	var roles []itemservice.Role
	for _, p := range h.story.People {
		for _, r := range p.Roles {
			roles = append(roles, itemservice.Role{Namespace: h.ns, Principal: p.Principal(), Role: r})
		}
	}
	if len(roles) > 0 {
		if _, err := k.EnsureRoles(ctx, []string{h.ns}, roles); err != nil {
			return fmt.Errorf("story: the roles: %w", err)
		}
	}
	if _, err := h.env.pool.Exec(ctx, `update outbox set delivered_at = now() where delivered_at is null`); err != nil {
		return fmt.Errorf("story: clear the outbox: %w", err)
	}
	return nil
}

// now is the story's time: the test environment's clock.
func (h *harness) now() time.Time { return h.tenv.Now().UTC() }

// id is the ID of the Item the story names name.
func (h *harness) id(name string) uuid.UUID {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.ids[name]
}

func (h *harness) nameOf(id uuid.UUID) string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nameLocked(id.String())
}

func (h *harness) nameLocked(id string) string {
	if u, err := uuid.Parse(id); err == nil {
		if n, ok := h.names[u]; ok {
			return n
		}
	}
	return id
}

func (h *harness) principal(person string) item.Principal {
	return item.Principal{Actor: h.conv.people[person], Namespace: h.ns}
}

// failAt records a failure at an instant: it belongs to the step that ran
// at that instant, if one did, and stops the run.
func (h *harness) failAt(at time.Time, format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	line := 0
	if h.current >= 0 && h.steps[h.current].At.Equal(at) {
		line = h.steps[h.current].Line
	}
	h.failLocked(line, format, args...)
}

func (h *harness) failLocked(line int, format string, args ...any) {
	h.failures = append(h.failures, Failure{Line: line, Message: fmt.Sprintf(format, args...)})
	if len(h.failures) == 1 {
		h.tenv.RegisterDelayedCallback(func() { h.tenv.SignalWorkflow(signalStop, nil) }, 0)
	}
}

func (h *harness) failed() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.failures) > 0
}

func (h *harness) recordCall(c Call, key string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.calls = append(h.calls, c)
	if c.SideEffect && !h.sideKeys[key] {
		h.sideKeys[key] = true
		h.sideLines = append(h.sideLines, c)
	}
}

// --- the driver ---

type driverInput struct {
	Steps []time.Time // when each step runs, given steps first
	Until time.Time
}

var harnessActivity = workflow.ActivityOptions{
	StartToCloseTimeout: 24 * time.Hour, // harness work takes real time only
	RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 1},
}

// driver runs the steps at their instants, settling after each, then
// waits until the end, or until a failure stops the run. It starts Items'
// workflows on demand and settles whenever an activity of theirs ends.
func (h *harness) driver(ctx workflow.Context, in driverInput) error {
	ctx = workflow.WithActivityOptions(ctx, harnessActivity)
	starts := workflow.GetSignalChannel(ctx, signalStart)
	settles := workflow.GetSignalChannel(ctx, signalSettle)
	stops := workflow.GetSignalChannel(ctx, signalStop)
	stopped := false
	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			var id string
			starts.Receive(ctx, &id)
			cctx := workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
				WorkflowID: id, ParentClosePolicy: enumspb.PARENT_CLOSE_POLICY_ABANDON,
			})
			f := workflow.ExecuteChildWorkflow(cctx, wf.Name, wf.Input{ItemID: id})
			_ = f.GetChildWorkflowExecution().Get(ctx, nil)
		}
	})
	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			settles.Receive(ctx, nil)
			for settles.ReceiveAsync(nil) {
			}
			_ = workflow.ExecuteActivity(ctx, activitySet).Get(ctx, nil)
		}
	})
	workflow.Go(ctx, func(ctx workflow.Context) {
		stops.Receive(ctx, nil)
		stopped = true
	})
	// sleep waits until an instant, or until a failure stops the run.
	sleep := func(until time.Time) {
		d := until.Sub(workflow.Now(ctx))
		if d <= 0 || stopped {
			return
		}
		tctx, cancel := workflow.WithCancel(ctx)
		timer := workflow.NewTimer(tctx, d)
		_ = workflow.Await(ctx, func() bool { return stopped || timer.IsReady() })
		cancel()
	}
	for i, at := range in.Steps {
		sleep(at)
		if stopped {
			return nil
		}
		if err := workflow.ExecuteActivity(ctx, activityStep, i).Get(ctx, nil); err != nil {
			return err
		}
		if err := workflow.ExecuteActivity(ctx, activitySet).Get(ctx, nil); err != nil {
			return err
		}
		if stopped {
			return nil
		}
	}
	sleep(in.Until)
	return nil
}

// --- the steps ---

// step runs step i: a write, or a read, as its person.
func (h *harness) step(ctx context.Context, i int) error {
	h.mu.Lock()
	h.current = i
	h.mu.Unlock()
	st := h.steps[i]
	// An Item the kernel created has a name (a name rule) only once the
	// harness has looked: look before a step names one, so the step
	// reaches it instead of no Item at all.
	if (st.Item != "" && h.id(st.Item) == uuid.Nil) || (st.Target != "" && h.id(st.Target) == uuid.Nil) ||
		(st.In != "" && h.id(st.In) == uuid.Nil) {
		h.ensureNamed(ctx)
	}
	given := i < len(h.story.Given)
	p := h.principal(st.Actor)
	key := "story-" + uuid.NewString()
	fields, err := h.fields(st)
	if err != nil {
		return err
	}
	var res itemservice.WriteResult
	var werr error
	write := true
	switch st.Verb {
	case Creates:
		req := itemservice.CreateRequest{IdempotencyKey: key, Principal: p, TypeID: st.Type, TypeVersion: st.Version, Fields: fields}
		if st.In != "" {
			in := h.id(st.In)
			req.ContainerID = &in
		}
		res, werr = h.k.Create(ctx, req)
		if werr == nil {
			h.mu.Lock()
			h.ids[st.Item], h.names[res.ItemID] = res.ItemID, st.Item
			h.mu.Unlock()
		}
	case Updates:
		res, werr = h.k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: key, Principal: p, ItemID: h.id(st.Item), Fields: fields})
	case Moves:
		var tr itemservice.TransitionResult
		tr, werr = h.k.Transition(ctx, itemservice.TransitionRequest{IdempotencyKey: key, Principal: p,
			ItemID: h.id(st.Item), ToState: st.To, Fields: fields})
		res = tr.WriteResult
	case Places:
		mv := &itemservice.Move{}
		if !st.Top {
			in := h.id(st.In)
			mv.To = &in
		}
		res, werr = h.k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: key, Principal: p, ItemID: h.id(st.Item), Move: mv})
	case Narrows:
		readers := make([]string, len(st.Readers))
		for i, r := range st.Readers {
			readers[i] = h.principal(r).Actor
		}
		res, werr = h.k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: key, Principal: p, ItemID: h.id(st.Item),
			Policy: &itemservice.PolicyPatch{Readers: readers}})
	case Links, Unlinks:
		req := itemservice.LinkRequest{IdempotencyKey: key, Principal: p, SourceID: h.id(st.Item), LinkType: st.Link, TargetID: h.id(st.Target)}
		if st.Verb == Links {
			res, werr = h.k.Link(ctx, req)
		} else {
			res, werr = h.k.Unlink(ctx, req)
		}
	default:
		write = false
		h.read(ctx, st)
	}
	if write {
		switch {
		case werr != nil && st.Refused == "":
			h.failStep(st, "%s was refused: %s", st.Text, api.Refusal(werr))
		case werr != nil && api.Refusal(werr) != st.Refused:
			h.failStep(st, "%s was refused %s, not %s", st.Text, api.Refusal(werr), st.Refused)
		case werr == nil && st.Refused != "":
			h.failStep(st, "%s was not refused (want %s)", st.Text, st.Refused)
		case werr == nil:
			h.mu.Lock()
			if given {
				if h.given[res.ItemID] == nil {
					h.given[res.ItemID] = map[int64]bool{}
				}
				h.given[res.ItemID][res.ItemSeq] = true
			} else {
				if h.own[res.ItemID] == nil {
					h.own[res.ItemID] = map[int64][]string{}
				}
				h.own[res.ItemID][res.ItemSeq] = sortedKeys(fields)
			}
			h.mu.Unlock()
		}
	}
	if h.opts.afterStep != nil && !given {
		if err := h.opts.afterStep(ctx, h, i-len(h.story.Given)); err != nil {
			return err
		}
	}
	return nil
}

func (h *harness) failStep(st Step, format string, args ...any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.failLocked(st.Line, format, args...)
}

// typeOf is the Type of the Item a step names (its current version).
func (h *harness) typeOf(ctx context.Context, name string) (*typedef.Type, error) {
	var typeID string
	var version int
	if err := h.env.pool.QueryRow(ctx, `select type_id, type_version from items where id = $1`, h.id(name)).Scan(&typeID, &version); err != nil {
		return nil, fmt.Errorf("story: the Type of %s: %w", name, err)
	}
	return h.env.byName[fmt.Sprintf("%s@%d", typeID, version)], nil
}

// fields converts a step's values to the kernel's.
func (h *harness) fields(st Step) (map[string]any, error) {
	if len(st.Values) == 0 || st.Verb == Sees {
		return nil, nil
	}
	var ty *typedef.Type
	if st.Verb == Creates {
		ty = h.env.byName[fmt.Sprintf("%s@%d", st.Type, st.Version)]
	} else {
		var err error
		if ty, err = h.typeOf(context.Background(), st.Item); err != nil {
			return nil, err
		}
	}
	out := map[string]any{}
	for _, a := range st.Values {
		f, _ := ty.Field(a.Field)
		v, err := h.conv.value(a.Value, fieldSlot(f))
		if err != nil {
			return nil, fmt.Errorf("story: line %d: %w", st.Line, err)
		}
		out[a.Field] = v
	}
	return out, nil
}

// read runs a read step through the item service's public read, as its
// person: what they see, through policy.Project.
func (h *harness) read(ctx context.Context, st Step) {
	if h.id(st.Item) == uuid.Nil {
		h.ensureNamed(ctx)
	}
	it, err := h.k.Get(ctx, h.principal(st.Actor), h.id(st.Item))
	if st.Verb == CannotSee {
		if err == nil {
			h.failStep(st, "%s can see %s", st.Actor, st.Item)
		} else if !errors.Is(err, itemservice.ErrNotFound) {
			h.failStep(st, "%s: %v", st.Text, err)
		}
		return
	}
	if err != nil {
		h.failStep(st, "%s cannot see %s: %s", st.Actor, st.Item, api.Refusal(err))
		return
	}
	ty := h.env.byName[fmt.Sprintf("%s@%d", it.TypeID, it.TypeVersion)]
	rd := h.renderer()
	switch st.Verb {
	case Sees:
		for _, a := range st.Values {
			if id, k, isRollup := strings.Cut(a.Field, "."); isRollup {
				if got := it.Rollups[id].Counts[k]; got != a.Value.Int {
					h.failStep(st, "%s sees %s: %s is %d, want %d", st.Actor, st.Item, a.Field, got, a.Value.Int)
					return
				}
				continue
			}
			f, _ := ty.Field(a.Field)
			sl := fieldSlot(f)
			want, err := h.conv.value(a.Value, sl)
			if err != nil {
				h.failStep(st, "%v", err)
				return
			}
			if got, w := rd.value(it.Fields[a.Field], sl), rd.value(want, sl); got != w {
				h.failStep(st, "%s sees %s: %s is %s, want %s", st.Actor, st.Item, a.Field, got, w)
				return
			}
		}
	case SeesFields:
		got := sortedKeys(it.Fields)
		want := slices.Sorted(slices.Values(st.Fields))
		if !slices.Equal(got, want) {
			h.failStep(st, "%s sees %s with fields %s, want %s", st.Actor, st.Item, strings.Join(got, ", "), strings.Join(want, ", "))
		}
	}
}

// --- settling: the real relay, run until the outbox is empty ---

// settleAll delivers everything in the outbox, through the real relay,
// to the Items' workflows. While it runs, time stands still.
func (h *harness) settleAll(ctx context.Context) error {
	h.settle.Lock()
	defer h.settle.Unlock()
	stuck := 0
	for {
		if err := h.relay.RunOnce(ctx); err != nil {
			return err
		}
		var pending int
		if err := h.env.pool.QueryRow(ctx, `select count(*) from outbox where delivered_at is null`).Scan(&pending); err != nil {
			return err
		}
		h.mu.Lock()
		busy := h.executing > 0
		for _, n := range h.fresh {
			busy = busy || n > 0
		}
		refused := len(h.lastErr) > 0
		h.mu.Unlock()
		if pending == 0 && !busy {
			// An update's answer can arrive while the main loop is still
			// in the workflow task that handled it, before that task has
			// scheduled what it starts. A round trip through the main loop
			// lets the task finish; then look again.
			h.onMain(func() {})
			if h.quiet(ctx) {
				return nil
			}
			continue
		}
		if pending > 0 && refused {
			stuck++
		} else {
			stuck = 0
		}
		if stuck >= 300 {
			h.mu.Lock()
			var msgs []string
			for id, err := range h.lastErr {
				msgs = append(msgs, fmt.Sprintf("%s: %v", h.nameLocked(id), err))
			}
			slices.Sort(msgs)
			h.mu.Unlock()
			h.failAt(h.now(), "the relay could not deliver an event: %s", strings.Join(msgs, "; "))
			return nil
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// quiet reports whether the outbox is empty and none of the kernel's
// activities is scheduled or running.
func (h *harness) quiet(ctx context.Context) bool {
	var pending int
	if err := h.env.pool.QueryRow(ctx, `select count(*) from outbox where delivered_at is null`).Scan(&pending); err != nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if pending > 0 || h.executing > 0 {
		return false
	}
	for _, n := range h.fresh {
		if n > 0 {
			return false
		}
	}
	return true
}

// Deliver is the relay's Workflows, in the test environment: it starts an
// Item's workflow (as the driver's child) when the relay asks for
// Update-With-Start and none runs, and sends the update to it.
func (h *harness) Deliver(ctx context.Context, d relay.Delivery) error {
	err := h.deliver(ctx, d)
	h.mu.Lock()
	if err != nil && !errors.Is(err, relay.ErrNothingListening) {
		h.lastErr[d.ItemID] = err
	} else {
		delete(h.lastErr, d.ItemID)
	}
	h.mu.Unlock()
	return err
}

func (h *harness) deliver(ctx context.Context, d relay.Delivery) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for attempt := 0; attempt < 3; attempt++ {
		if err := h.ensureRunning(ctx, d); err != nil {
			return err
		}
		name, arg := wf.UpdateApply, any(d.Event)
		if d.Child != nil {
			name, arg = wf.UpdateChild, any(*d.Child)
		}
		// Each attempt its own update ID: the test environment would
		// answer a refused ID's redelivery with nothing. The workflow's
		// own lastAppliedSeq still drops a duplicate.
		updateID := fmt.Sprintf("%s#%s", d.UpdateID, uuid.NewString()[:8])
		done := make(chan error, 1)
		var notFound bool
		h.onMain(func() {
			if err := h.tenv.UpdateWorkflowByID(d.ItemID, name, updateID, &updateDone{done: done}, arg); err != nil {
				notFound = true
				done <- err
			}
		})
		// An update sent to a workflow that completes before handling it
		// is never answered in the test environment (the server would
		// refuse it): if the workflow completes meanwhile, it was not
		// listening.
		tick := time.NewTicker(20 * time.Millisecond)
		completed := false
	wait:
		for {
			select {
			case err := <-done:
				tick.Stop()
				if !notFound {
					return err
				}
				completed = true
				break wait
			case <-tick.C:
				h.mu.Lock()
				gone := !h.running[d.ItemID]
				h.mu.Unlock()
				if gone {
					tick.Stop()
					completed = true
					break wait
				}
			case <-ctx.Done():
				tick.Stop()
				return fmt.Errorf("story: deliver %s: %w", d.UpdateID, ctx.Err())
			}
		}
		if completed {
			h.mu.Lock()
			delete(h.running, d.ItemID)
			h.mu.Unlock()
			if !d.Start {
				return relay.ErrNothingListening
			}
			// It completed meanwhile: start it again.
		}
	}
	return fmt.Errorf("story: deliver %s: its workflow kept completing", d.UpdateID)
}

// ensureRunning starts the Item's workflow when the delivery asks for
// Update-With-Start and none runs.
func (h *harness) ensureRunning(ctx context.Context, d relay.Delivery) error {
	h.mu.Lock()
	running := h.running[d.ItemID]
	ch, starting := h.started[d.ItemID]
	if !running && !starting && d.Start {
		ch = make(chan struct{})
		h.started[d.ItemID] = ch
	}
	h.mu.Unlock()
	if running || !d.Start {
		return nil
	}
	if !starting {
		h.onMain(func() { h.tenv.SignalWorkflow(signalStart, d.ItemID) })
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("story: start the workflow of %s: %w", d.ItemID, ctx.Err())
	}
}

// onMain runs f on the test environment's main loop and waits for it.
func (h *harness) onMain(f func()) {
	done := make(chan struct{})
	h.tenv.RegisterDelayedCallback(func() { f(); close(done) }, 0)
	<-done
}

// updateDone is an update's callbacks: its outcome, once.
type updateDone struct {
	done chan error
	once sync.Once
}

func (u *updateDone) Accept()          {}
func (u *updateDone) Reject(err error) { u.once.Do(func() { u.done <- err }) }
func (u *updateDone) Complete(_ any, err error) {
	u.once.Do(func() { u.done <- err })
}

// --- registering the kernel's workflow with the harness's faults ---

// registry registers the kernel's workflow and activities with the test
// environment, wrapping an activity a test makes fail (runOptions).
type registry struct {
	*testsuite.TestWorkflowEnvironment
	h *harness
}

func (r *registry) RegisterActivityWithOptions(a any, o activity.RegisterOptions) {
	if n := r.h.opts.failActivity[o.Name]; n > 0 {
		a = failFirst(a, n, o.Name)
	}
	r.TestWorkflowEnvironment.RegisterActivityWithOptions(r.h.counted(a, o.Name), o)
}

// counted wraps an activity function so the harness knows when each of
// its attempts runs.
func (h *harness) counted(fn any, name string) any {
	v := reflect.ValueOf(fn)
	return reflect.MakeFunc(v.Type(), func(args []reflect.Value) []reflect.Value {
		info := activity.GetInfo(args[0].Interface().(context.Context))
		h.attemptBegins(info.WorkflowExecution.ID+"/"+info.ActivityID, name)
		h.mu.Lock()
		h.executing++
		h.mu.Unlock()
		defer func() {
			h.mu.Lock()
			h.executing--
			h.mu.Unlock()
		}()
		return v.Call(args)
	}).Interface()
}

// attemptBegins records that an activity's first attempt started (or that
// it was canceled before one could): it is no longer fresh.
func (h *harness) attemptBegins(key, name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.begun[key] {
		h.begun[key] = true
		h.fresh[name]--
	}
}

func (h *harness) scheduled(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fresh[name]++
}

// counting is a worker interceptor: it counts every activity a workflow
// schedules, at the moment it is scheduled.
type counting struct {
	interceptor.WorkerInterceptorBase
	h *harness
}

func (c *counting) InterceptWorkflow(ctx workflow.Context, next interceptor.WorkflowInboundInterceptor) interceptor.WorkflowInboundInterceptor {
	in := &countingIn{h: c.h}
	in.Next = next
	return in
}

type countingIn struct {
	interceptor.WorkflowInboundInterceptorBase
	h *harness
}

func (i *countingIn) Init(outbound interceptor.WorkflowOutboundInterceptor) error {
	out := &countingOut{h: i.h}
	out.Next = outbound
	return i.Next.Init(out)
}

type countingOut struct {
	interceptor.WorkflowOutboundInterceptorBase
	h *harness
}

func (o *countingOut) ExecuteActivity(ctx workflow.Context, activityType string, args ...any) workflow.Future {
	if !strings.HasPrefix(activityType, "story.") {
		o.h.scheduled(activityType)
	}
	return o.Next.ExecuteActivity(ctx, activityType, args...)
}

// failFirst wraps an activity function so its first n calls fail with a
// retryable error and do nothing.
func failFirst(fn any, n int, name string) any {
	v := reflect.ValueOf(fn)
	t := v.Type()
	var mu sync.Mutex
	calls := 0
	return reflect.MakeFunc(t, func(args []reflect.Value) []reflect.Value {
		mu.Lock()
		calls++
		fail := calls <= n
		mu.Unlock()
		if !fail {
			return v.Call(args)
		}
		out := make([]reflect.Value, t.NumOut())
		for i := range out {
			out[i] = reflect.Zero(t.Out(i))
		}
		err := temporal.NewApplicationError(fmt.Sprintf("story: %s fails (injected)", name), "injected")
		out[len(out)-1] = reflect.ValueOf(&err).Elem()
		return out
	}).Interface()
}
