// Replay tests: the workflow's code may change only in ways that replay the
// histories of workflows already running. Step 2, part 2 adds link
// reactions to the workflow without workflow.GetVersion, on the claim that
// the new branches run only on reaction events, so a history recorded
// before the change replays the same. These tests hold the claim to account.
//
// testdata/histories/*.json were recorded from main before part 2 (commit
// 9cccd7f) by TestRecordHistories below. Do not re-record them after
// changing the workflow: then they would prove nothing.
package workflow_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/temporalproto"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	wf "github.com/francoisnyc/work-kernel/internal/workflow"
)

const historiesDir = "testdata/histories"

// The histories recorded from main. Each must exist: a missing file is a
// failure, not a skip.
var recordedHistories = []string{
	"ticket_v3_hold_and_timer.json",     // enrich hooks, a 24h timer armed, an approve held by erp_check, allowed
	"vacation_request_v1_hr_check.json", // submit held by hr_check, corrected with f_approver
	"job_timer_fires.json",              // a 2s timer fires and moves the Item back
}

// Every history recorded from main replays on today's workflow code without
// a non-determinism error.
func TestReplay_HistoriesFromMain(t *testing.T) {
	for _, name := range recordedHistories {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join(historiesDir, name))
			if err != nil {
				t.Fatalf("open recorded history: %v", err)
			}
			defer f.Close()
			history, err := client.HistoryFromJSON(f, client.HistoryJSONOptions{})
			if err != nil {
				t.Fatalf("read history: %v", err)
			}
			replayer := worker.NewWorkflowReplayer()
			replayer.RegisterWorkflowWithOptions(wf.ItemWorkflow, workflow.RegisterOptions{Name: wf.Name})
			if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
				t.Fatalf("replay %s: %v", name, err)
			}
		})
	}
}

// TestRecordHistories records the histories above. It runs only with
// RECORD_HISTORIES=1, and only on main's workflow code (see the package
// comment).
func TestRecordHistories(t *testing.T) {
	if os.Getenv("RECORD_HISTORIES") != "1" {
		t.Skip("records replay testdata; set RECORD_HISTORIES=1 (on main's workflow code only)")
	}
	if err := os.MkdirAll(historiesDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Run("ticket", func(t *testing.T) {
		h := newHookStack(t)
		ana := approver(t)
		id := h.newTicket(ana)
		h.prefetched(ana, id)
		h.toInProgress(ana, id) // in_progress arms its 24h timer
		res, _ := h.approve(ana, id, 3*time.Second)
		if res.Outcome != itemservice.OutcomeApplied {
			t.Fatalf("approve = %+v, want applied", res)
		}
		h.waitCompleted(id)
		saveHistory(t, id, "ticket_v3_hold_and_timer.json")
	})

	t.Run("vacation", func(t *testing.T) {
		h := newHRStack(t)
		ana, bob := user("ana"), user("bob")
		h.hr.SetManager(ana.Actor, bob.Actor)
		id := h.request(ana)
		if res := h.submit(ana, id); res.Outcome != itemservice.OutcomeApplied {
			t.Fatalf("submit = %+v, want applied", res)
		}
		h.waitCompleted(id)
		saveHistory(t, id, "vacation_request_v1_hr_check.json")
	})

	t.Run("job", func(t *testing.T) {
		s := newStack(t)
		id := s.createJob(map[string]any{"f_note": "replay"})
		s.move(id, "running")
		s.waitState(id, "queued", 20*time.Second) // the timer fired
		s.waitCompleted(id)
		saveHistory(t, id, "job_timer_fires.json")
	})
}

// saveHistory writes the workflow's (only) run's history as JSON, in the
// format client.HistoryFromJSON reads.
func saveHistory(t *testing.T, id uuid.UUID, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	it := tc.GetWorkflowHistory(ctx, id.String(), "", false, enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT)
	var history historypb.History
	for it.HasNext() {
		ev, err := it.Next()
		if err != nil {
			t.Fatalf("read history: %v", err)
		}
		history.Events = append(history.Events, ev)
	}
	data, err := temporalproto.CustomJSONMarshalOptions{Indent: "  "}.Marshal(&history)
	if err != nil {
		t.Fatalf("encode history: %v", err)
	}
	if err := os.WriteFile(filepath.Join(historiesDir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("recorded %d events to %s", len(history.Events), name)
}
