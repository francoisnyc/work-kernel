// Tests for the fixes of the 2026-11-21 review of Jira part 2, step 4a
// (docs/reviews/2026-11-21-customers.md), beside the review's own five
// TestReview_ tests. Numbered as the decisions of the fix's Phase A:
//
//  1. Link conditions and field filters (and order_by) for a customer
//     hold per row: a name or field counts only on Items whose own Type
//     version lists it for them; an unlisted name is refused as an unknown
//     one, in the same words.
//  3. A customer, or an agent acting for one, never sees commit_seq (0 in
//     every write's answer, replays included, and in history), and may not
//     send min_seq.
//  4. requester: the customer must see the Item as written, checked in the
//     write's transaction.
//  6. Check-then-write: the customer rule is checked again after the row
//     lock. A move or a narrowing between the check and the write refuses
//     the write as if the Item were hidden, and writes nothing.
//  7. An agent acting for a customer whose write would need an approval is
//     refused at once: ErrApprovalUnavailable, never pending.
package itemservice_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

// vkiosk admits vtickets; vticket@1 lists only its title for customers,
// vticket@2 also f_code and the link rel. The same field and link in both.
const vkioskSrc = `
id: vkiosk
version: 1
fields:
  - {id: f_name, type: string}
own: [vticket]
customer_fields: [f_name]
`

const vticket1Src = `
id: vticket
version: 1
fields:
  - {id: f_title, type: string}
  - {id: f_code,  type: string}
links:
  - {name: rel, inverse: rel_by, targets: [vticket]}
customer_fields: [f_title]
`

const vticket2Src = `
id: vticket
version: 2
fields:
  - {id: f_title, type: string}
  - {id: f_code,  type: string}
links:
  - {name: rel, inverse: rel_by, targets: [vticket]}
customer_fields: [f_title, f_code, rel]
start: {older_versions: all}
`

type vworld struct {
	custWorld
	kiosk uuid.UUID
}

func newVWorld(t *testing.T) vworld {
	t.Helper()
	w := newCustWorld(t)
	registerSrc(t, w.k, vticket1Src)
	registerSrc(t, w.k, vticket2Src)
	registerSrc(t, w.k, vkioskSrc)
	return vworld{custWorld: w, kiosk: w.mk(t, w.lea, "vkiosk", 1, nil, map[string]any{"f_name": "v"})}
}

// ticket is kim's vticket of a version, whose f_code staff then set.
func (w vworld) ticket(t *testing.T, version int, code string) uuid.UUID {
	t.Helper()
	id := w.mk(t, w.kim, "vticket", version, &w.kiosk, map[string]any{"f_title": "t"})
	if code != "" {
		if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: id,
			Fields: map[string]any{"f_code": code}}); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

func (w vworld) search(t *testing.T, p item.Principal, f itemservice.Filter, order *itemservice.OrderBy) []uuid.UUID {
	t.Helper()
	res, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Filter: f, OrderBy: order, Limit: 500})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	var out []uuid.UUID
	for _, it := range res.Items {
		out = append(out, it.ID)
	}
	return out
}

// ============================================================ 1. per row

// A name listed in v2 does not match a v1 Item's unlisted link, either way.
func TestFix_LinkCondition_HoldsPerVersion(t *testing.T) {
	w := newVWorld(t)
	v1a, v1b, v2a := w.ticket(t, 1, ""), w.ticket(t, 1, ""), w.ticket(t, 2, "")
	w.link(t, w.sam, v1a, "rel", v1b) // source v1: unlisted
	w.link(t, w.sam, v2a, "rel", v1b) // source v2: listed
	only := itemservice.Filter{TypeIDs: []string{"vticket"}}
	cond := func(name string, dir item.LinkDirection, to uuid.UUID) itemservice.Filter {
		f := only
		f.Links = []itemservice.LinkFilter{{LinkType: name, Direction: dir, ItemID: to}}
		return f
	}
	if got := w.search(t, w.kim, cond("rel", item.LinkOutgoing, v1b), nil); !sameIDs(got, []uuid.UUID{v2a}) {
		t.Errorf("kim: Items that rel v1b = %v; want v2a only (v1a's link is not listed in v1)", got)
	}
	// Incoming: the Items v1a rel-links to. v1a's version does not list
	// rel, so none for kim; v2a's does.
	if got := w.search(t, w.kim, cond("rel_by", item.LinkIncoming, v1a), nil); len(got) != 0 {
		t.Errorf("kim: Items rel'd by v1a = %v; want none (the source's version does not list rel)", got)
	}
	if got := w.search(t, w.kim, cond("rel_by", item.LinkIncoming, v2a), nil); !sameIDs(got, []uuid.UUID{v1b}) {
		t.Errorf("kim: Items rel'd by v2a = %v; want v1b", got)
	}
	// Staff: both links, as before.
	if got := w.search(t, w.sam, cond("rel", item.LinkOutgoing, v1b), nil); !sameIDs(got, []uuid.UUID{v1a, v2a}) {
		t.Errorf("sam: Items that rel v1b = %v; want v1a and v2a", got)
	}
}

// An unlisted name is refused in the very words of an unknown one.
func TestFix_LinkCondition_AnUnlistedName_TheSameRefusalAsAnUnknownName(t *testing.T) {
	w := newCustWorld(t)
	kiosk := w.mk(t, w.lea, "kiosk", 1, nil, map[string]any{"f_name": "k"})
	t1 := w.mk(t, w.kim, "kticket", 1, &kiosk, map[string]any{"f_title": "one"})
	ask := func(name string, dir item.LinkDirection) error {
		_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.kim, Filter: itemservice.Filter{
			TypeIDs: []string{"kticket"}, Links: []itemservice.LinkFilter{{LinkType: name, Direction: dir, ItemID: t1}}}})
		return err
	}
	for _, c := range []struct {
		hidden, unknown string
		dir             item.LinkDirection
	}{{"relates", "nope", item.LinkOutgoing}, {"related_to", "nope_by", item.LinkIncoming}} {
		e1, e2 := ask(c.hidden, c.dir), ask(c.unknown, c.dir)
		var a, b *itemservice.InvalidError
		if !errors.As(e1, &a) || !errors.As(e2, &b) || e1.Error() != e2.Error() || a.Path != b.Path {
			t.Errorf("%s: %v; unknown %s: %v; want the same InvalidError", c.hidden, e1, c.unknown, e2)
		}
	}
	// A listed name is accepted.
	if err := ask("blocks", item.LinkOutgoing); err != nil {
		t.Errorf("blocks (listed): %v", err)
	}
}

// Field filters and order_by hold per row too: f_code is listed in v2 only.
func TestFix_FieldFilterAndOrder_HoldPerVersion(t *testing.T) {
	w := newVWorld(t)
	v1a, v1z, v2m := w.ticket(t, 1, "A"), w.ticket(t, 1, "Z"), w.ticket(t, 2, "M")
	all := itemservice.Filter{TypeIDs: []string{"vticket"}}
	for code, want := range map[string][]uuid.UUID{"A": nil, "Z": nil, "M": {v2m}} {
		f := all
		f.Fields = []itemservice.FieldFilter{{Field: "f_code", Eq: code}}
		if got := w.search(t, w.kim, f, nil); !sameIDs(got, want) {
			t.Errorf("kim: f_code = %s: %v; want %v (a v1 ticket's f_code is hidden)", code, got, want)
		}
		f.Fields = []itemservice.FieldFilter{{Field: "f_code", Gte: "A"}}
		if got := w.search(t, w.kim, f, nil); !sameIDs(got, []uuid.UUID{v2m}) {
			t.Errorf("kim: f_code >= A: %v; want v2m only", got)
		}
	}
	// In order, kim sees the v1 tickets as having no code: after v2m, by ID.
	got := w.search(t, w.kim, all, &itemservice.OrderBy{Field: "f_code"})
	if len(got) != 3 || got[0] != v2m {
		t.Errorf("kim ordered by f_code: %v; want v2m first, the v1 tickets after it as unset", got)
	}
	ids := map[uuid.UUID]string{v1a: "A", v1z: "Z"}
	if len(got) == 3 && ids[got[1]] == "A" && ids[got[2]] == "Z" && got[1].String() > got[2].String() {
		t.Errorf("kim ordered by f_code: v1 tickets in their hidden codes' order, not by ID")
	}
	// Staff: as before.
	if got := w.search(t, w.sam, all, &itemservice.OrderBy{Field: "f_code"}); len(got) != 3 || got[0] != v1a || got[1] != v2m || got[2] != v1z {
		t.Errorf("sam ordered by f_code: %v; want A, M, Z", got)
	}
}

// ============================================================ 3. commit_seq

func TestFix_CommitSeq_ZeroForACustomer_AndMinSeqRefused(t *testing.T) {
	w := newCustWorld(t)
	inc := w.incident(t, w.kim, w.desk)
	bot := w.portalBot(t, "agent:portal-"+word(), w.kim)
	key := newKey()
	create := func(p item.Principal, k string) itemservice.WriteResult {
		t.Helper()
		res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: k, Principal: p, TypeID: "reply", TypeVersion: 1,
			ContainerID: &inc, Fields: map[string]any{"f_body": "x"}})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	for name, res := range map[string]itemservice.WriteResult{
		"kim's reply": create(w.kim, key), "kim's replay": create(w.kim, key), "the bot's reply": create(bot, newKey()),
	} {
		if res.CommitSeq != 0 || res.ItemSeq == 0 {
			t.Errorf("%s: commit_seq %d, item_seq %d; want 0 and the item_seq", name, res.CommitSeq, res.ItemSeq)
		}
	}
	tr, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: w.kim, ItemID: inc, ToState: "canceled"})
	if err != nil || tr.CommitSeq != 0 {
		t.Errorf("kim cancels: %+v, %v; want commit_seq 0", tr, err)
	}
	if s := create(w.sam, newKey()); s.CommitSeq == 0 {
		t.Error("sam's reply answers commit_seq 0; staff keep it")
	}
	for _, e := range allEvents(t, w.k, w.kim, inc) {
		if e.CommitSeq != 0 {
			t.Errorf("kim's history: event %d has commit_seq %d", e.ItemSeq, e.CommitSeq)
		}
	}
	if es := allEvents(t, w.k, w.sam, inc); es[0].CommitSeq == 0 {
		t.Error("sam's history lost commit_seq")
	}
	for _, p := range []item.Principal{w.kim, bot} {
		_, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: p, Filter: itemservice.Filter{TypeIDs: []string{"incident"}}, MinSeq: 1})
		var inv *itemservice.InvalidError
		if !errors.As(err, &inv) || inv.Path != "min_seq" {
			t.Errorf("%s sends min_seq: %v; want InvalidError on min_seq", p.Actor, err)
		}
	}
	if _, err := w.k.Search(testCtx(t), itemservice.SearchRequest{Principal: w.sam, Filter: itemservice.Filter{TypeIDs: []string{"incident"}}, MinSeq: 1}); err != nil {
		t.Errorf("sam sends min_seq: %v", err)
	}
}

// ============================================================ 4 and 6. in the transaction

// onWrite runs f once, at the first "write" point (inside the next write's
// transaction, after its idempotency key is claimed and before it locks
// any row), on its own context: as if another person wrote at that moment.
func onWrite(t *testing.T, f func(ctx context.Context) error) {
	t.Helper()
	var done atomic.Bool
	restore := itemservice.SetTestHook(func(_ context.Context, _ pgx.Tx, point string) error {
		if point != "write" || !done.CompareAndSwap(false, true) {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return f(ctx)
	})
	t.Cleanup(restore)
}

func childrenOf(t *testing.T, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(testCtx(t), `select count(*) from items where container_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// kim's reply and her cancel, while sam moves her incident to the old desk
// between the customer rule's check and the write: refused as hidden,
// nothing written.
func TestFix_CheckThenWrite_TheIncidentLeavesThePortal(t *testing.T) {
	w := newCustWorld(t)
	for _, c := range []struct {
		name string
		act  func(inc uuid.UUID) error
	}{
		{"a reply", func(inc uuid.UUID) error {
			_, err := w.tryCreate(t, w.kim, "reply", 1, &inc, map[string]any{"f_body": "still broken"})
			return err
		}},
		{"a cancel", func(inc uuid.UUID) error { return w.move(t, w.kim, inc, "canceled", nil) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			inc := w.incident(t, w.kim, w.desk)
			onWrite(t, func(ctx context.Context) error {
				_, err := w.k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.sam, ItemID: inc,
					Move: &itemservice.Move{To: &w.oldDesk}})
				return err
			})
			err := c.act(inc)
			if !errors.Is(err, itemservice.ErrNotFound) {
				t.Errorf("kim's %s after her incident left the portal: %v; want ErrNotFound", c.name, err)
			}
			it, gerr := w.k.Get(testCtx(t), w.sam, inc)
			if gerr != nil || it.State != "new" || it.ItemSeq != 2 || childrenOf(t, inc) != 0 {
				t.Errorf("the incident: %+v, %v, %d children; want new, at item_seq 2 (create, move), no reply",
					it, gerr, childrenOf(t, inc))
			}
		})
	}
}

// kim opens an incident while lea narrows the portal away from her: refused
// as hidden, nothing written.
func TestFix_CheckThenWrite_ThePortalIsNarrowedAway(t *testing.T) {
	w := newCustWorld(t)
	before := childrenOf(t, w.desk2)
	onWrite(t, func(ctx context.Context) error {
		_, err := w.k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: w.desk2,
			Policy: &itemservice.PolicyPatch{Readers: []string{w.lea.Actor, w.sam.Actor}}})
		return err
	})
	_, err := w.tryCreate(t, w.kim, "incident", 2, &w.desk2, map[string]any{"f_title": "x", "f_priority": "P3"})
	if !errors.Is(err, itemservice.ErrNotFound) || childrenOf(t, w.desk2) != before {
		t.Errorf("kim's incident in a portal narrowed away meanwhile: %v, %d children; want ErrNotFound, %d", err, childrenOf(t, w.desk2), before)
	}
}

// sam opens an incident for kim while lea narrows the portal away from her:
// the requester refusal, nothing written.
func TestFix_Requester_CheckedOnTheItemAsWritten(t *testing.T) {
	w := newCustWorld(t)
	before := childrenOf(t, w.desk2)
	onWrite(t, func(ctx context.Context) error {
		_, err := w.k.Update(ctx, itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: w.desk2,
			Policy: &itemservice.PolicyPatch{Readers: []string{w.lea.Actor, w.sam.Actor}}})
		return err
	})
	_, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.sam, TypeID: "incident", TypeVersion: 2,
		ContainerID: &w.desk2, Fields: map[string]any{"f_title": "phoned", "f_priority": "P3"}, Requester: "customer:kim"})
	var inv *itemservice.InvalidError
	if !errors.As(err, &inv) || inv.Path != "requester" || childrenOf(t, w.desk2) != before {
		t.Errorf("an incident for kim in a portal narrowed away meanwhile: %v; want InvalidError on requester, nothing written", err)
	}
	// The same refusal as a requester the portal hid from the start.
	_, err2 := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.sam, TypeID: "incident", TypeVersion: 2,
		ContainerID: &w.desk2, Fields: map[string]any{"f_title": "phoned", "f_priority": "P3"}, Requester: "customer:kim"})
	if err == nil || err2 == nil || err.Error() != err2.Error() {
		t.Errorf("refusals differ: %v / %v", err, err2)
	}
}

// ============================================================ 7. approvals

// A portal bot that read a narrowed incident writes into a wider one: the
// write would need kim's approval, which a customer cannot give. Refused at
// once, with nothing written and nothing pending.
func TestFix_APortalBot_ApprovalUnavailable(t *testing.T) {
	w := newCustWorld(t)
	agent := "agent:portal-" + word()
	narrowed, open := w.incident(t, w.kim, w.desk), w.incident(t, w.kim, w.desk)
	if _, err := w.k.Update(testCtx(t), itemservice.UpdateRequest{IdempotencyKey: newKey(), Principal: w.lea, ItemID: narrowed,
		Policy: &itemservice.PolicyPatch{Readers: []string{w.lea.Actor, w.sam.Actor, "customer:kim", agent}}}); err != nil {
		t.Fatal(err)
	}
	bot := w.portalBot(t, agent, w.kim)
	if _, err := w.k.Get(testCtx(t), bot, narrowed); err != nil {
		t.Fatalf("the bot reads the narrowed incident: %v", err)
	}
	_, err := w.tryCreate(t, bot, "reply", 1, &open, map[string]any{"f_body": "from the portal"})
	if !errors.Is(err, itemservice.ErrApprovalUnavailable) || errors.Is(err, itemservice.ErrNeedsApproval) {
		t.Errorf("the bot writes into a wider scope: %v; want ErrApprovalUnavailable, and not ErrNeedsApproval", err)
	}
	if childrenOf(t, open) != 0 {
		t.Error("the refused reply was written")
	}
	// Staff agents are as before: needs approval.
	staffBot := withSession(t, w.k, item.Principal{Actor: agent, OnBehalfOf: w.lea.Actor, Namespace: w.ns})
	if _, err := w.k.Get(testCtx(t), staffBot, narrowed); err != nil {
		t.Fatal(err)
	}
	// An empty note: note@1 opens no field to agents (closed by default).
	_, err = w.tryCreate(t, staffBot, "note", 1, &open, nil)
	if !errors.Is(err, itemservice.ErrNeedsApproval) {
		t.Errorf("an agent for lea: %v; want ErrNeedsApproval, as before", err)
	}
}

// Words hold per row too: a word only in f_code matches kim's v2 ticket
// (f_code listed) and never her v1 ticket (f_code hidden).
func TestFix_Words_HoldPerVersion(t *testing.T) {
	w := newVWorld(t)
	secret, shown := "xray"+word()[1:9], "yankee"+word()[1:9]
	v1 := w.ticket(t, 1, secret)
	v2 := w.ticket(t, 2, shown)
	if got := w.searchAll(t, w.kim, secret, itemservice.Filter{}, 50); len(got) != 0 {
		t.Errorf("kim searches a word only in a v1 ticket's hidden f_code: %v; want nothing", got)
	}
	if got := w.searchAll(t, w.kim, shown, itemservice.Filter{}, 50); !sameIDs(got, []uuid.UUID{v2}) {
		t.Errorf("kim searches a word in a v2 ticket's f_code: %v; want v2", got)
	}
	if got := w.searchAll(t, w.sam, secret, itemservice.Filter{}, 50); !sameIDs(got, []uuid.UUID{v1}) {
		t.Errorf("sam searches the v1 ticket's f_code: %v; want v1", got)
	}
}
