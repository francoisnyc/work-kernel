// A measurement, not a test (MEASURE=1): how often a person's save of an
// incident is ABORTED STALE because a goal's reaction wrote a due instant
// on it in between (Later, from step 3a: "a kernel-written field set by a
// reaction bumps its Item's etag"). It prints numbers for the journal and
// asserts nothing about them.
package workflow_test

import (
	"errors"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

func TestMeasure_Incident_StaleSaves(t *testing.T) {
	if os.Getenv("MEASURE") != "1" {
		t.Skip("a measurement; set MEASURE=1")
	}
	s := newIncStack(t, 0)
	const n = 40
	delays := []time.Duration{0, 100 * time.Millisecond, 500 * time.Millisecond, 2 * time.Second}
	stale := make([]int, len(delays))
	var lags []time.Duration
	for i := range n {
		// The person opens the incident's page as soon as it exists (its
		// etag is its item_seq), thinks for a while, and saves.
		inc := s.makeIn(s.ana, "incident", &s.desk, map[string]any{"f_title": "measure", "f_priority": "P4"})
		_, read := s.state(inc)
		d := delays[i%len(delays)]
		time.Sleep(d)
		_, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: "key-" + uuid.NewString(), Principal: s.ana, ItemID: inc, ExpectedItemSeq: read,
			Fields: map[string]any{"f_impact": "minor"},
		})
		if errors.Is(err, itemservice.ErrStale) {
			stale[i%len(delays)]++
		} else if err != nil {
			t.Fatalf("save: %v", err)
		}
		// How long until both due instants were on the incident.
		eventually(t, 20*time.Second, "both due instants", func() bool {
			return s.f(inc, "f_due_first_response") != nil && s.f(inc, "f_due_resolution") != nil
		})
		created := eventAt(t, inc, 1)
		var last time.Time
		for _, ev := range allEvents(t, inc) {
			if ev.op == "update" && len(ev.actor) > 9 && ev.actor[:9] == "reaction:" {
				last = ev.at
			}
		}
		lags = append(lags, last.Sub(created))
	}
	slices.Sort(lags)
	t.Logf("%d incidents; a save made with the etag read right after the create, after a delay:", n)
	for i, d := range delays {
		t.Logf("  after %-6v: %d of %d STALE", d, stale[i], n/len(delays))
	}
	t.Logf("from the incident's create to the second due instant written on it: p50 %v, p90 %v, max %v",
		lags[len(lags)/2].Round(time.Millisecond), lags[len(lags)*9/10].Round(time.Millisecond), lags[len(lags)-1].Round(time.Millisecond))

	// One whole cycle: how many times the goals write the incident.
	inc, _, res := s.started("P4", map[string]any{"f_assignee": s.sam.Actor})
	eventually(t, 20*time.Second, "shown", func() bool { return s.f(inc, "f_due_resolution") != nil && s.f(inc, "f_due_first_response") != nil })
	s.act(s.sam, inc, "in_progress", nil)
	s.comment(s.sam, inc, true)
	eventually(t, 20*time.Second, "answered", func() bool { return s.f(inc, "f_due_first_response") == nil })
	s.act(s.sam, inc, "waiting_for_customer", nil)
	eventually(t, 20*time.Second, "paused", func() bool { return s.f(inc, "f_due_resolution") == nil })
	s.comment(s.ana, inc, true)
	eventually(t, 20*time.Second, "resumed", func() bool { return s.f(inc, "f_due_resolution") != nil })
	s.act(s.sam, inc, "resolved", map[string]any{"f_resolution": "fixed"})
	eventually(t, 20*time.Second, "resolved", func() bool { return s.is(res, "completed")() && s.f(inc, "f_due_resolution") == nil })
	time.Sleep(2 * time.Second)
	byOp := map[string]int{}
	writes := 0
	for _, ev := range allEvents(t, inc) {
		byOp[ev.op]++
		if ev.op == "update" && len(ev.actor) > 9 && ev.actor[:9] == "reaction:" {
			writes++
		}
	}
	t.Logf("one cycle (create, answer, wait, customer reply, resolve): %d events on the incident, of which %d are due-instant writes by its goals (each bumps the etag); by op: %v",
		len(allEvents(t, inc)), writes, byOp)
}
