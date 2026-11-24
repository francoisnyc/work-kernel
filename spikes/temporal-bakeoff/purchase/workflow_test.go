package purchase

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/acts"
	"github.com/francoisnyc/work-kernel/spikes/temporal-bakeoff/auth"
)

var (
	ana  = auth.Principal{ID: "user:ana"}
	bob  = auth.Principal{ID: "user:bob"}
	fay  = auth.Principal{ID: "user:fay", Roles: []string{"finance"}}
	gus  = auth.Principal{ID: "user:gus", Roles: []string{"finance"}}
	cora = auth.Principal{ID: "user:cora", Roles: []string{"cfo"}}
	bot  = auth.Principal{ID: "agent:prep-bot", Roles: []string{"preparer"}}
	evil = auth.Principal{ID: "agent:prep-bot", Roles: []string{"preparer", "finance"}} // an agent wrongly given finance
)

// step is one update sent to the workflow, and whether it must be refused.
type step struct {
	name    string
	arg     any
	refused bool
}

// play runs a purchase of amount through steps, one per simulated second,
// and returns the final state and the order placed (empty if none).
func play(t *testing.T, amount int64, steps ...step) (State, string) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a *acts.Activities
	env.OnActivity(a.ManagerOf, mock.Anything, "user:ana").Return("user:bob", nil)
	env.OnActivity(a.Record, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	ordered := ""
	env.OnActivity(a.PlaceOrder, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(
		func(_ context.Context, key, vendor string, amt int64) (string, error) {
			if vendor != "officeco" || amt != amount {
				t.Errorf("order for %s %d, want officeco %d", vendor, amt, amount)
			}
			ordered = "ORD-1"
			return ordered, nil
		})
	env.OnActivity(a.Notify, mock.Anything, mock.Anything, "user:ana", "notify_rejected", mock.Anything).Return(nil)

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
	env.ExecuteWorkflow(Workflow, Input{ID: "purchase-1", Requester: "user:ana", Amount: amount, Vendor: "officeco", CostCentre: "CC-100", Justification: "x"})
	// Ask the workflow where it stands: one still waiting has no result to read.
	var st State
	v, err := env.QueryWorkflow(QueryState)
	if err != nil {
		t.Fatal(err)
	}
	if err := v.Get(&st); err != nil {
		t.Fatal(err)
	}
	return st, ordered
}

func approve(p auth.Principal) step {
	return step{UpdateDecide, DecideArgs{Actor: p, Approve: true}, false}
}
func denied(s step) step { s.refused = true; return s }

func TestSmallPurchase_TheManagerAloneDecides(t *testing.T) {
	st, order := play(t, 300, approve(bob))
	if st.State != StateApproved || order == "" {
		t.Fatalf("state %s, order %q", st.State, order)
	}
}

func TestLargePurchase_ManagerThenAClaimingFinanceMember(t *testing.T) {
	st, order := play(t, 2500,
		approve(bob),
		denied(approve(fay)), // not claimed yet
		step{UpdateClaim, ActorArgs{Actor: fay}, false},
		denied(approve(gus)), // fay claimed it
		approve(fay))
	if st.State != StateApproved || st.ClaimedBy != "user:fay" || order == "" {
		t.Fatalf("state %s, claimed by %s, order %q", st.State, st.ClaimedBy, order)
	}
}

func TestNobodyButTheRightPersonDecides(t *testing.T) {
	st, order := play(t, 2500,
		denied(approve(ana)),  // the requester
		denied(approve(fay)),  // not the manager
		denied(approve(bot)),  // an agent
		denied(approve(evil)), // an agent, even with a person's role
		denied(step{UpdateClaim, ActorArgs{Actor: fay}, false}), // not in the queue yet
		approve(bob),
		denied(step{UpdateClaim, ActorArgs{Actor: bob}, false}),  // not finance
		denied(step{UpdateClaim, ActorArgs{Actor: evil}, false}), // an agent with the finance role
		denied(approve(bob)), // the manager's step is over
	)
	if st.State != StateFinanceQueue || order != "" {
		t.Fatalf("state %s, order %q", st.State, order)
	}
}

func TestRejection_NeedsAReasonAndTellsTheRequester(t *testing.T) {
	st, order := play(t, 300,
		denied(step{UpdateDecide, DecideArgs{Actor: bob, Approve: false}, false}),
		step{UpdateDecide, DecideArgs{Actor: bob, Approve: false, Reason: "not this quarter"}, false})
	if st.State != StateRejected || st.Reason != "not this quarter" || order != "" {
		t.Fatalf("state %s, reason %q, order %q", st.State, st.Reason, order)
	}
}

func TestPreparation_OnlyThePreparerAndOnlyBeforeTheManagerDecides(t *testing.T) {
	prep := func(p auth.Principal, summary string) step {
		return step{UpdatePrepare, PrepareArgs{Actor: p, Summary: summary, Policy: "ok", Flags: "none"}, false}
	}
	st, _ := play(t, 2500,
		denied(prep(bob, "by the manager")),
		prep(bot, "first"),
		prep(bot, "second"),
		approve(bob),
		denied(prep(bot, "after the manager decided")))
	if st.Preparation == nil || st.Preparation.Summary != "second" || st.Preparation.By != "agent:prep-bot" {
		t.Fatalf("preparation %+v", st.Preparation)
	}
}

// Changed after the review: any finance member may hand a claimed purchase
// back (it was the claimer only, so one absent member held the queue).
func TestRelease_AnyFinanceMemberPutsItBackInTheQueue(t *testing.T) {
	st, _ := play(t, 2500, approve(bob),
		step{UpdateClaim, ActorArgs{Actor: fay}, false},
		denied(step{UpdateRelease, ActorArgs{Actor: bob}, false}), // not finance
		denied(step{UpdateRelease, ActorArgs{Actor: bot}, false}), // an agent
		step{UpdateRelease, ActorArgs{Actor: gus}, false},         // fay is away
		step{UpdateClaim, ActorArgs{Actor: gus}, false})
	if st.State != StateFinanceReview || st.ClaimedBy != "user:gus" {
		t.Fatalf("state %s, claimed by %s", st.State, st.ClaimedBy)
	}
}

func TestOverFiveThousand_TheCFODecidesAfterFinance(t *testing.T) {
	st, order := play(t, 6000,
		approve(bob),
		step{UpdateClaim, ActorArgs{Actor: fay}, false},
		approve(fay),
		denied(approve(fay)), // finance has decided; this step is the CFO's
		denied(approve(bot)),
		approve(cora))
	if st.State != StateApproved || order == "" {
		t.Fatalf("state %s, order %q", st.State, order)
	}
}

func TestFiveThousandExactly_NeedsNoCFO(t *testing.T) {
	st, order := play(t, 5000, approve(bob), step{UpdateClaim, ActorArgs{Actor: fay}, false}, approve(fay))
	if st.State != StateApproved || order == "" {
		t.Fatalf("state %s, order %q", st.State, order)
	}
}

func TestPreparation_IsCappedSoTheDecisionCanAlwaysArrive(t *testing.T) {
	steps := []step{}
	for i := 0; i < MaxPreparations; i++ {
		steps = append(steps, step{UpdatePrepare, PrepareArgs{Actor: bot, Summary: "again", Policy: "ok"}, false})
	}
	steps = append(steps, denied(step{UpdatePrepare, PrepareArgs{Actor: bot, Summary: "once more", Policy: "ok"}, false}), approve(bob))
	st, order := play(t, 500, steps...)
	if st.State != StateApproved || order == "" {
		t.Fatalf("state %s, order %q", st.State, order)
	}
}

func TestOrderRefused_LeavesTheRequestWithFinanceNotApproved(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	var a *acts.Activities
	env.OnActivity(a.ManagerOf, mock.Anything, "user:ana").Return("user:bob", nil)
	env.OnActivity(a.Record, mock.Anything, mock.Anything, mock.Anything).Return(nil)
	env.OnActivity(a.PlaceOrder, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return("",
		temporal.NewNonRetryableApplicationError("no such vendor", "HTTP_404", nil))
	env.RegisterDelayedCallback(func() {
		env.UpdateWorkflow(UpdateDecide, "", &testsuite.TestUpdateCallback{OnAccept: func() {}, OnReject: func(err error) { t.Error(err) }, OnComplete: func(any, error) {}},
			DecideArgs{Actor: bob, Approve: true})
	}, time.Second)
	env.ExecuteWorkflow(Workflow, Input{ID: "purchase-1", Requester: "user:ana", Amount: 500, Vendor: "ghost", CostCentre: "CC-100", Justification: "x"})
	var st State
	if err := env.GetWorkflowResult(&st); err != nil {
		t.Fatal(err)
	}
	if st.State != StateOrderFailed || st.Failure == "" {
		t.Fatalf("state %s, failure %q", st.State, st.Failure)
	}
}
