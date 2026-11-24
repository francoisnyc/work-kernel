package access

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/testsuite"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/acts"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/auth"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/purchase"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/store"
)

var (
	ana  = auth.Principal{ID: "user:ana"}
	olaf = auth.Principal{ID: "user:olaf"}                            // owns payroll
	fay  = auth.Principal{ID: "user:fay", Roles: []string{"finance"}} // owns ledger
	bot  = auth.Principal{ID: "agent:prep-bot", Roles: []string{"preparer"}}
)

// step is one update sent to the workflow, and whether it must be refused.
type step struct {
	name    string
	arg     any
	refused bool
}

// played is what a run left behind: the workflow's state, the last picture
// it wrote for the inbox, and how many times the requester was told.
type played struct {
	State
	row  store.Row
	told int
}

// play runs requester's request for access to system through steps, one
// per simulated second.
func play(t *testing.T, requester auth.Principal, system string, steps ...step) played {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a *acts.Activities
	var out played
	env.OnActivity(a.Record, mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, row store.Row, _ *store.Event) error {
			out.row = row
			return nil
		})
	env.OnActivity(a.Notify, mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, key, to, template, request string) error {
			if key != "rejected-access-1" || to != requester.ID || template != "notify_rejected" || request != "access-1" {
				t.Errorf("told %s %s about %s (key %s)", to, template, request, key)
			}
			out.told++
			return nil
		})

	for i, s := range steps {
		env.RegisterDelayedCallback(func() {
			env.UpdateWorkflow(s.name, "", &testsuite.TestUpdateCallback{
				OnAccept: func() {
					if s.refused {
						t.Errorf("step %d (%s): accepted, want refused", i, s.name)
					}
				},
				OnReject: func(err error) {
					if !s.refused {
						t.Errorf("step %d (%s): refused: %v", i, s.name, err)
					}
				},
				OnComplete: func(any, error) {},
			}, s.arg)
		}, time.Duration(i+1)*time.Second)
	}
	env.SetTestTimeout(5 * time.Second)
	env.ExecuteWorkflow(Workflow, Input{ID: "access-1", Requester: requester.ID, System: system, Role: "viewer", Justification: "month-end close"})
	// Ask the workflow where it stands: one still waiting has no result to read.
	v, err := env.QueryWorkflow(purchase.QueryState)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Get(&out.State); err != nil {
		t.Fatal(err)
	}
	return out
}

func approve(p auth.Principal) step {
	return step{purchase.UpdateDecide, purchase.DecideArgs{Actor: p, Approve: true}, false}
}
func denied(s step) step { s.refused = true; return s }

func TestApproval_TheSystemsOwnerDecidesAndNothingElseHappens(t *testing.T) {
	got := play(t, ana, "payroll", approve(olaf))
	if got.State.State != StateApproved || got.Owner != "user:olaf" || got.told != 0 {
		t.Fatalf("state %s, owner %s, told %d times", got.State.State, got.Owner, got.told)
	}
	if got.row.State != StateApproved || len(got.row.WaitingOn) != 0 || got.row.PrepareOpen {
		t.Fatalf("inbox row %+v", got.row)
	}
}

func TestEachSystemHasItsOwnOwner(t *testing.T) {
	got := play(t, ana, "ledger",
		denied(approve(olaf)), // owns payroll, not the ledger
		approve(fay))
	if got.State.State != StateApproved || got.Owner != "user:fay" {
		t.Fatalf("state %s, owner %s", got.State.State, got.Owner)
	}
}

func TestNobodyButTheOwnerDecides(t *testing.T) {
	got := play(t, ana, "payroll",
		denied(approve(ana)), // the requester
		denied(approve(fay)), // owns another system
		denied(approve(bot)), // an agent
		denied(approve(auth.Principal{ID: "agent:prep-bot", Roles: []string{"preparer", "owner"}})), // an agent, whatever its roles
	)
	if got.State.State != StateOwnerReview {
		t.Fatalf("state %s", got.State.State)
	}
	// While it waits, it is in the owner's inbox and nobody else's.
	if got.row.Kind != Kind || !slices.Equal(got.row.WaitingOn, []string{"user:olaf"}) || !got.row.PrepareOpen {
		t.Fatalf("inbox row %+v", got.row)
	}
	if !slices.Contains(got.row.Readers, "user:ana") || !slices.Contains(got.row.Readers, "user:olaf") || slices.Contains(got.row.Readers, "user:fay") {
		t.Fatalf("readers %v", got.row.Readers)
	}
}

// The API and the agent read these names from the request's data.
func TestTheData_CarriesWhatWasAskedUnderTheAgreedNames(t *testing.T) {
	got := play(t, ana, "payroll")
	var data map[string]any
	if err := json.Unmarshal(got.row.Data, &data); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"kind": "access", "system": "payroll", "role": "viewer", "justification": "month-end close", "preparation": nil}
	for k, v := range want {
		if have, ok := data[k]; !ok || have != v {
			t.Errorf("data[%q] = %v (present: %v), want %v", k, have, ok, v)
		}
	}
}

func TestRejection_NeedsAReasonAndTellsTheRequester(t *testing.T) {
	got := play(t, ana, "payroll",
		denied(step{purchase.UpdateDecide, purchase.DecideArgs{Actor: olaf, Approve: false}, false}),
		denied(step{purchase.UpdateDecide, purchase.DecideArgs{Actor: olaf, Approve: false, Reason: "  "}, false}),
		step{purchase.UpdateDecide, purchase.DecideArgs{Actor: olaf, Approve: false, Reason: "not for contractors"}, false})
	if got.State.State != StateRejected || got.Reason != "not for contractors" || got.told != 1 {
		t.Fatalf("state %s, reason %q, told %d times", got.State.State, got.Reason, got.told)
	}
}

func TestPreparation_OnlyThePreparerAndItDecidesNothing(t *testing.T) {
	prep := func(p auth.Principal, summary, policy string) step {
		return step{purchase.UpdatePrepare, purchase.PrepareArgs{Actor: p, Summary: summary, Policy: policy, Flags: "none"}, false}
	}
	got := play(t, ana, "payroll",
		denied(prep(olaf, "by the owner", "ok")),
		denied(prep(bot, "", "ok")),               // no summary
		denied(prep(bot, "a verdict", "approve")), // policy is ok, check or fail
		prep(bot, "first", "ok"),
		prep(bot, "second", "check"))
	if got.Preparation == nil || got.Preparation.Summary != "second" || got.Preparation.Policy != "check" || got.Preparation.By != "agent:prep-bot" {
		t.Fatalf("preparation %+v", got.Preparation)
	}
	if got.State.State != StateOwnerReview || !got.row.PrepareOpen {
		t.Fatalf("state %s, prepare open %v", got.State.State, got.row.PrepareOpen)
	}
}

func TestARequestNobodyCouldDecide_Fails(t *testing.T) {
	for name, tc := range map[string]struct {
		requester auth.Principal
		system    string
	}{
		"the owner asks for their own system": {olaf, "payroll"},
		"a system nobody owns":                {ana, "warehouse"},
	} {
		t.Run(name, func(t *testing.T) {
			got := play(t, tc.requester, tc.system)
			if got.State.State != StateFailed || got.Failure == "" || len(got.row.WaitingOn) != 0 || got.row.PrepareOpen {
				t.Fatalf("state %s, failure %q, inbox row %+v", got.State.State, got.Failure, got.row)
			}
		})
	}
}

// Added after the review: the agent's rewrites are capped, as for a purchase.
func TestPreparation_IsCappedSoTheDecisionCanAlwaysArrive(t *testing.T) {
	steps := []step{}
	for i := 0; i < purchase.MaxPreparations; i++ {
		steps = append(steps, step{purchase.UpdatePrepare, purchase.PrepareArgs{Actor: bot, Summary: "again", Policy: "ok"}, false})
	}
	steps = append(steps,
		denied(step{purchase.UpdatePrepare, purchase.PrepareArgs{Actor: bot, Summary: "once more", Policy: "ok"}, false}),
		step{purchase.UpdateDecide, purchase.DecideArgs{Actor: olaf, Approve: true}, false})
	got := play(t, ana, "payroll", steps...)
	if got.State.State != StateApproved {
		t.Fatalf("state %s", got.State.State)
	}
}
