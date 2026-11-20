// End to end, with a real worker: the measurement of Jira 2.3b as a
// regression test (the etag step). A person reads an incident right after
// its create; its goals are created and write their due instants on it;
// the person's save, made with what they read, is accepted: 10 times in
// 10 (it was refused 10 times in 10 when the etag was item_seq).
//
// The save carries ExpectedContentSeq, the if-match the API derives the
// etag from. No workflow code is involved in the check: the workflow's own
// writes keep ExpectedItemSeq, with the meaning it had.
package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// seqs reads an Item's two counters as p sees it.
func (s *incStack) seqs(p item.Principal, id uuid.UUID) (itemSeq, contentSeq int64) {
	s.t.Helper()
	it, err := s.k.Get(serverCtx(s.t), p, id)
	if err != nil {
		s.t.Fatalf("Get: %v", err)
	}
	return it.ItemSeq, it.ContentSeq
}

// kernelWritesLanded: both goals were created (two outcome records on the
// incident) and both wrote their due instant on it.
func (s *incStack) kernelWritesLanded(inc uuid.UUID) func() bool {
	return func() bool {
		if s.f(inc, "f_due_first_response") == nil || s.f(inc, "f_due_resolution") == nil {
			return false
		}
		records := 0
		for _, ev := range allEvents(s.t, inc) {
			if ev.op == "reaction_applied" {
				records++
			}
		}
		return records >= 2
	}
}

func TestE2E_Incident_TheSaveWithWhatWasReadAtTheCreate_IsAccepted_10In10(t *testing.T) {
	s := newIncStack(t, 0)
	const n = 10
	accepted := 0
	for i := range n {
		inc := s.makeIn(s.ana, "incident", &s.desk, map[string]any{"f_title": "regression", "f_priority": "P4"})
		// ana opens the incident's page as soon as it exists.
		_, read := s.seqs(s.ana, inc)
		if read != 1 {
			t.Fatalf("incident %d: content_seq %d right after the create, want 1", i, read)
		}
		// The kernel's writes land (322 ms after the create, p50, in 2.3b).
		eventually(t, 20*time.Second, "both goals created and both due instants on the incident", s.kernelWritesLanded(inc))
		itemSeq, contentSeq := s.seqs(s.ana, inc)
		if itemSeq < 5 || contentSeq != 1 {
			t.Fatalf("incident %d after the kernel's writes: item_seq %d, content_seq %d; want item_seq >= 5 (create, two records, two due instants) and content_seq 1",
				i, itemSeq, contentSeq)
		}
		// She saves with what she read.
		_, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
			IdempotencyKey: "key-" + uuid.NewString(), Principal: s.ana, ItemID: inc, ExpectedContentSeq: read,
			Fields: map[string]any{"f_impact": "minor"},
		})
		switch {
		case err == nil:
			accepted++
		case errors.Is(err, itemservice.ErrStale):
			t.Errorf("incident %d: the save was refused as stale: %v", i, err)
		default:
			t.Fatalf("incident %d: save: %v", i, err)
		}
	}
	if accepted != n {
		t.Errorf("%d of %d saves accepted, want %d of %d", accepted, n, n, n)
	}
}

// One whole cycle of an incident (create, answer, wait, customer reply,
// resolve) is 20 events on it, 15 of them the kernel's own. Only the five
// that people made (the create and four moves) change what a caller could
// overwrite. And a real conflict is still refused at the end of it.
func TestE2E_Incident_ACycle_OnlyThePeoplesWritesChangeTheContent(t *testing.T) {
	s := newIncStack(t, 0)
	inc, _, res := s.started("P4", map[string]any{"f_assignee": s.sam.Actor})
	eventually(t, 20*time.Second, "shown", s.kernelWritesLanded(inc))
	s.act(s.sam, inc, "in_progress", nil)
	s.comment(s.sam, inc, true)
	eventually(t, 20*time.Second, "answered", func() bool { return s.f(inc, "f_due_first_response") == nil })
	s.act(s.sam, inc, "waiting_for_customer", nil)
	eventually(t, 20*time.Second, "paused", func() bool { return s.f(inc, "f_due_resolution") == nil })
	s.comment(s.ana, inc, true)
	eventually(t, 20*time.Second, "resumed", func() bool { return s.f(inc, "f_due_resolution") != nil })
	_, read := s.seqs(s.ana, inc) // ana and sam both read the incident here
	s.act(s.sam, inc, "resolved", map[string]any{"f_resolution": "fixed"})
	eventually(t, 20*time.Second, "resolved", func() bool { return s.is(res, "completed")() && s.f(inc, "f_due_resolution") == nil })

	// However many of the kernel's records have landed by now, none of
	// them counts.
	itemSeq, contentSeq := s.seqs(s.ana, inc)
	if contentSeq != 5 {
		t.Errorf("content_seq %d after the cycle, want 5: the create and sam's four moves", contentSeq)
	}
	if itemSeq <= 10 {
		t.Errorf("item_seq %d after the cycle: the goals' writes and the reactions' records are events too (20 in Jira 2.3b)", itemSeq)
	}
	// ana's save with what she read before sam resolved: a real conflict.
	_, err := s.k.Update(serverCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: s.ana, ItemID: inc, ExpectedContentSeq: read,
		Fields: map[string]any{"f_impact": "minor"},
	})
	if !errors.Is(err, itemservice.ErrStale) {
		t.Errorf("ana's save with what she read before sam resolved the incident: %v, want ErrStale", err)
	}
}
