// A worker that dies while a reaction's activity is in flight (added with
// the fix of a flaky step 3a test in PR #33, which had been probing this
// by accident).
//
// Temporal learns nothing from a dead worker: the attempt it handed out
// stays "started" until its StartToClose timeout runs out, counted from
// the attempt's start; only then is the activity retried, after the retry
// policy's first interval. So a reaction caught by a crash completes
//
//	at most StartToCloseTimeout + InitialInterval after its lost attempt started
//
// (plus the time to hand the retry to a worker and run it), exactly once:
// the retry runs under the reaction's own idempotency keys. That delays
// what the reaction does (a goal's pause, the due instant shown on its
// host, and behind it in the queue the next reactions) and never what it
// computes: every number comes from the cause's time.
package workflow_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/client"

	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

// handOver is the allowance, beyond the timeouts, for the server to hand
// the retry to the restarted worker and for the activity to run. It is the
// one number here that is not derived from the activity's settings.
const handOver = 5 * time.Second

func TestE2E_Step3_AWorkerStoppedMidReaction_TheReactionCompletesOnce_WithinItsTimeout(t *testing.T) {
	opts := wf.ReactionActivityOptions(0) // the stack's worker runs with the default budget
	bound := opts.StartToCloseTimeout + opts.RetryPolicy.InitialInterval + handOver
	t.Logf("a lost attempt comes back within StartToCloseTimeout %v + InitialInterval %v (+ %v to hand it over): %v",
		opts.StartToCloseTimeout, opts.RetryPolicy.InitialInterval, handOver, bound)

	s := newJobStack(t)
	// This test's worker has a connection of its own, so that it can be
	// cut: a worker that is only stopped still reports the attempt it was
	// running as failed, and Temporal retries it a second later. A worker
	// that crashes reports nothing.
	doomed, err := client.Dial(client.Options{HostPort: temporalHostPort})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer doomed.Close()
	s.stopWorker()
	s.worker = wf.NewWorker(doomed, s.queue, s.k, wf.WorkerOptions{})
	if err := s.worker.Start(); err != nil {
		t.Fatalf("start worker: %v", err)
	}
	job, main, _ := s.started(600)
	s.move(job, "working")
	eventually(t, 15*time.Second, "the job's run settled", func() bool { _, seq := s.state(job); return seq == 5 })
	s.waitDelivered(job)
	s.waitDelivered(main)

	// Hold the meter's row: the job's pause reaction, which moves the
	// meter, will block on it, in flight.
	tx, err := pool.Begin(serverCtx(t))
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(serverCtx(t)) //nolint:errcheck // released below; this is for a failing test
	if _, err := tx.Exec(serverCtx(t), `select 1 from items where id = $1 for update`, main); err != nil {
		t.Fatalf("lock the meter: %v", err)
	}
	waiting := s.move(job, "waiting")

	// The attempt is started, and not finished.
	var started time.Time
	eventually(t, 20*time.Second, "the pause reaction's activity in flight", func() bool {
		desc, err := tc.DescribeWorkflowExecution(serverCtx(t), job.String(), "")
		if err != nil {
			return false
		}
		for _, pa := range desc.GetPendingActivities() {
			if pa.GetActivityType().GetName() == wf.ActivityRunAfterReaction &&
				pa.GetState() == enumspb.PENDING_ACTIVITY_STATE_STARTED && pa.GetAttempt() == 1 {
				started = pa.GetLastStartedTime().AsTime()
				return true
			}
		}
		return false
	})
	if st, _ := s.state(main); st != "running" {
		t.Fatalf("the meter is %s while its row is held, want running", st)
	}

	// The worker dies with the attempt in its hands: its connection goes
	// first, so nothing it says on the way down reaches the server. Then
	// the row is free, and a new worker starts (on the shared connection).
	doomed.Close()
	s.stopWorker()
	if err := tx.Rollback(serverCtx(t)); err != nil {
		t.Fatalf("release the meter: %v", err)
	}
	staysFor(t, 2*time.Second, "nothing happens while no worker runs", s.stateIs(main, "running"))
	s.startWorker()

	left := time.Until(started.Add(bound))
	if left <= 0 {
		t.Fatalf("the test itself took longer than the bound %v before the worker was back", bound)
	}
	eventually(t, left, fmt.Sprintf("the meter paused within %v of the lost attempt's start", bound), s.stateIs(main, "paused"))
	_, pausedSeq := s.state(main)
	took := eventAt(t, main, pausedSeq).Sub(started)
	t.Logf("the reaction's write landed %v after its lost attempt started (bound %v)", took.Round(100*time.Millisecond), bound)
	if took > bound {
		t.Errorf("the pause was written %v after the lost attempt started, want within %v", took, bound)
	}

	// Exactly once: one move of the meter, one record on the job, and
	// nothing more once everything has settled.
	eventually(t, 15*time.Second, "the reaction recorded on the job", func() bool {
		return slices.Contains(records(t, job), "reaction_applied wf3job:pause")
	})
	s.waitDelivered(job)
	s.waitDelivered(main)
	staysFor(t, 3*time.Second, "one pause, one record", func() bool {
		var moves, recs int
		_, seq := s.state(main)
		for i := int64(1); i <= seq; i++ {
			if ev := readEvent(t, main, i); ev.Op == "transition" && ev.Actor == "reaction:wf3job:pause" {
				moves++
			}
		}
		for _, r := range records(t, job) {
			if r == "reaction_applied wf3job:pause" {
				recs++
			}
		}
		return moves == 1 && recs == 1
	})

	// Late, and right: the elapsed time is counted up to the job's event,
	// not to when the reaction finally ran.
	want := int64(eventAt(t, job, waiting.ItemSeq).Sub(eventAt(t, job, 1)) / time.Second)
	if got := s.seconds(main, "f_elapsed_s"); got != want {
		t.Errorf("f_elapsed_s = %d, want %d: up to the event that entered waiting", got, want)
	}
	// And the queue behind it moved on: the meter's own reaction cleared
	// the job's copy of the due instant.
	eventually(t, 15*time.Second, "the job's f_due cleared", func() bool { return s.field(job, "f_due") == nil })
}
