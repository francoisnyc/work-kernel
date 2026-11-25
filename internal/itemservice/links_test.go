// Tests for proof of concept Step 1: links (docs/design.md, "Links,
// containment and people").
//
// A link is added or removed through Link and Unlink: one event on the
// SOURCE Item only (link_added / link_removed, with item_seq, so its history
// and etag see it); the target gets no event and no new item_seq. The
// database enforces cardinality. The caller must see both ends. Get shows
// links both ways: an outgoing link to an Item the caller cannot see is
// restricted, an incoming link from one is left out. Links never change who
// can see anything.
package itemservice_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// --- the world ---

// linkTypeYAML is the Type these tests link: TYPE links to itself, and to
// OTHER through `about`. Its states make a small lifecycle in which
// "closing" (s_work -> s_done) and asking for a check (s_work -> s_check,
// held by erp_check) are refused while something not done or canceled
// blocks the Item. s_done -> s_work reopens.
const linkTypeYAML = `
id: TYPE
version: VERSION
initial_state: s_open
fields:
  - {id: f_title,       type: string}
  - {id: f_assignee,    type: principal}
  - {id: f_customer_id, type: string}
  - {id: f_amount,      type: int}
  - {id: f_cost_center, type: string}
links:
  - {name: blocks,       inverse: blocked_by,   targets: [TYPE]}
  - {name: duplicate_of, inverse: duplicates,   targets: [TYPE], max: one}
  - {name: claims,       inverse: claimed_by,   targets: [TYPE], inverse_max: one}
  - {name: about,        inverse: mentioned_by, targets: [TYPE, OTHER]}
states:
  - {id: s_open,     category: todo}
  - {id: s_work,     category: active}
  - {id: s_check,    category: active}
  - {id: s_done,     category: done}
  - {id: s_canceled, category: canceled}
transitions:
  - {from: s_open, to: s_work}
  - {from: s_open, to: s_canceled}
  - {from: s_work, to: s_canceled}
  - from: s_work
    to: s_done
    guard: 'links.blocked_by.all(b, b.category in ["done", "canceled"])'
  - from: s_work
    to: s_check
    guard: 'links.blocked_by.all(b, b.category in ["done", "canceled"])'
  - {from: s_done, to: s_work}
hooks:
  - id: erp_check
    on: transition
    from: s_work
    to: s_check
    kind: check
    action: erp.check_credit
    reads: [f_customer_id, f_amount]
    writes: [f_cost_center]
start: {older_versions: all}
`

// otherTypeYAML is a second Type that also declares `blocks` (to TYPE), so
// a link filter by name matches every Type that declares it.
const otherTypeYAML = `
id: OTHER
version: 1
fields:
  - {id: f_title, type: string}
links:
  - {name: blocks, inverse: blocked_by, targets: [TYPE]}
`

// linkWorld: a Type of its own (two versions) and another Type, both
// registered under fresh IDs, and a user in the default namespace (named:
// List and ListEvents refuse an empty one).
type linkWorld struct {
	k          *itemservice.Service
	typ, other string
	ana, bob   item.Principal
}

func newLinkWorld(t *testing.T) linkWorld {
	t.Helper()
	k := itemservice.New(pool)
	w := linkWorld{
		k:     k,
		typ:   "lnk-" + uuid.NewString()[:8],
		other: "lno-" + uuid.NewString()[:8],
		ana:   in(person("ana"), "default"),
		bob:   in(person("bob"), "default"),
	}
	register := func(yaml string) {
		t.Helper()
		yaml = strings.NewReplacer("TYPE", w.typ, "OTHER", w.other).Replace(yaml)
		ty, err := typedef.Parse([]byte(yaml))
		if err != nil {
			t.Fatalf("parse link test Type: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register link test Type: %v", err)
		}
	}
	for _, v := range []string{"1", "2"} {
		register(strings.Replace(linkTypeYAML, "VERSION", v, 1))
	}
	register(otherTypeYAML)
	return w
}

// paths walk an Item from s_open to a state.
var paths = map[string][]string{
	"s_open": nil, "s_work": {"s_work"}, "s_done": {"s_work", "s_done"},
	"s_canceled": {"s_canceled"},
}

// itemOpts are the optional parts of make.
type itemOpts struct {
	version   int
	narrow    []string // readers; nil: not narrowed
	container *uuid.UUID
	fields    map[string]any
}

// make creates an Item of the world's Type as p and walks it to state.
func (w linkWorld) make(t *testing.T, p item.Principal, state string, o itemOpts) uuid.UUID {
	t.Helper()
	if o.version == 0 {
		o.version = 1
	}
	fields := map[string]any{"f_title": "linked " + word()}
	for k, v := range o.fields {
		fields[k] = v
	}
	req := itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: p, TypeID: w.typ, TypeVersion: o.version,
		Fields: fields, ContainerID: o.container,
	}
	if o.narrow != nil {
		req.Narrow = &itemservice.Narrow{Readers: o.narrow}
	}
	res, err := w.k.Create(testCtx(t), req)
	if err != nil {
		t.Fatalf("create %s: %v", w.typ, err)
	}
	for _, to := range paths[state] {
		w.mustMove(t, p, res.ItemID, to)
	}
	return res.ItemID
}

// open makes an Item in s_open that everyone in the namespace sees.
func (w linkWorld) open(t *testing.T) uuid.UUID { return w.make(t, w.ana, "s_open", itemOpts{}) }

func (w linkWorld) moveItem(ctx context.Context, p item.Principal, id uuid.UUID, to string) (itemservice.TransitionResult, error) {
	return w.k.Transition(ctx, itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to,
	})
}

func (w linkWorld) mustMove(t *testing.T, p item.Principal, id uuid.UUID, to string) itemservice.TransitionResult {
	t.Helper()
	res, err := w.moveItem(testCtx(t), p, id, to)
	if err != nil {
		t.Fatalf("transition to %s: %v", to, err)
	}
	return res
}

func (w linkWorld) req(p item.Principal, src uuid.UUID, name string, dst uuid.UUID) itemservice.LinkRequest {
	return itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: p, SourceID: src, LinkType: name, TargetID: dst}
}

func (w linkWorld) link(t *testing.T, p item.Principal, src uuid.UUID, name string, dst uuid.UUID) (itemservice.WriteResult, error) {
	t.Helper()
	return w.k.Link(testCtx(t), w.req(p, src, name, dst))
}

func (w linkWorld) unlink(t *testing.T, p item.Principal, src uuid.UUID, name string, dst uuid.UUID) (itemservice.WriteResult, error) {
	t.Helper()
	return w.k.Unlink(testCtx(t), w.req(p, src, name, dst))
}

func (w linkWorld) mustLink(t *testing.T, p item.Principal, src uuid.UUID, name string, dst uuid.UUID) itemservice.WriteResult {
	t.Helper()
	res, err := w.link(t, p, src, name, dst)
	if err != nil {
		t.Fatalf("Link %s %s %s as %s: %v", src, name, dst, p.Actor, err)
	}
	return res
}

// linksOf returns the Item's links as p sees them, and whether more exist.
func (w linkWorld) linksOf(t *testing.T, p item.Principal, id uuid.UUID) ([]item.Link, bool) {
	t.Helper()
	it, err := w.k.Get(testCtx(t), p, id)
	if err != nil {
		t.Fatalf("Get as %s: %v", p.Actor, err)
	}
	return it.Links, it.LinksTruncated
}

func out(name string, other uuid.UUID) item.Link {
	return item.Link{LinkType: name, Direction: item.LinkOutgoing, Other: other}
}

func inc(name string, other uuid.UUID) item.Link {
	return item.Link{LinkType: name, Direction: item.LinkIncoming, Other: other}
}

func restrictedOut(name string) item.Link {
	return item.Link{LinkType: name, Direction: item.LinkOutgoing, Restricted: true}
}

// assertLinks checks the links: outgoing first, then incoming; within a
// direction the order is not specified.
func assertLinks(t *testing.T, what string, got []item.Link, want ...item.Link) {
	t.Helper()
	key := func(l item.Link) string {
		return fmt.Sprintf("%s %s %s %v", l.Direction, l.LinkType, l.Other, l.Restricted)
	}
	split := func(ls []item.Link) (outgoing, incoming []string) {
		for _, l := range ls {
			if l.Direction == item.LinkOutgoing {
				outgoing = append(outgoing, key(l))
			} else {
				incoming = append(incoming, key(l))
			}
		}
		slices.Sort(outgoing)
		slices.Sort(incoming)
		return outgoing, incoming
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].Direction == item.LinkIncoming && got[i].Direction == item.LinkOutgoing {
			t.Errorf("%s: an outgoing link comes after an incoming one: %v", what, got)
			break
		}
	}
	go1, gi := split(got)
	wo, wi := split(want)
	if !slices.Equal(go1, wo) || !slices.Equal(gi, wi) {
		t.Errorf("%s: links = %v\nwant %v", what, got, want)
	}
	for _, l := range got {
		if l.Restricted && l.Other != uuid.Nil {
			t.Errorf("%s: a restricted link names %s", what, l.Other)
		}
	}
}

func itemSeqOf(t *testing.T, id uuid.UUID) int64 {
	t.Helper()
	var seq int64
	if err := pool.QueryRow(testCtx(t), `select item_seq from items where id = $1`, id).Scan(&seq); err != nil {
		t.Fatalf("read item_seq: %v", err)
	}
	return seq
}

func eventCount(t *testing.T, id uuid.UUID) int {
	t.Helper()
	return count(t, `select count(*) from item_events where item_id = $1`, id)
}

// assertRefusedMeta checks a RejectedError's code and metadata.
func assertRefusedMeta(t *testing.T, what string, err error, code string, md map[string]string) {
	t.Helper()
	assertRejectedCode(t, what, err, code)
	var rej *itemservice.RejectedError
	if errors.As(err, &rej) {
		for k, v := range md {
			if rej.Metadata[k] != v {
				t.Errorf("%s: metadata[%s] = %q, want %q (metadata %v)", what, k, rej.Metadata[k], v, rej.Metadata)
			}
		}
	}
}

// --- adding and removing ---

// One event on the source, with the link's shape and no values; the target
// gets no event and keeps its item_seq.
func TestLink_OneEventOnTheSourceOnly(t *testing.T) {
	w := newLinkWorld(t)
	x, y := w.open(t), w.open(t)

	res := w.mustLink(t, w.ana, x, "blocks", y)
	if res.ItemID != x || res.ItemSeq != 2 || res.CommitSeq == 0 {
		t.Errorf("Link = %+v, want the source %s at item_seq 2 with a commit_seq", res, x)
	}
	op, payload := event(t, x, 2)
	if op != "link_added" {
		t.Errorf("event op = %q, want link_added", op)
	}
	if len(payload) != 2 || payload["link_type"] != "blocks" || payload["target"] != y.String() {
		t.Errorf("payload = %v, want exactly {link_type: blocks, target: %s}", payload, y)
	}
	if n := count(t, `select count(*) from item_event_values where item_id = $1 and item_seq = 2`, x); n != 0 {
		t.Errorf("the link event has %d values rows, want none", n)
	}
	if seq, n := itemSeqOf(t, y), eventCount(t, y); seq != 1 || n != 1 {
		t.Errorf("target: item_seq %d with %d events, want 1 and 1: a link writes no event on its target", seq, n)
	}
	// The row snapshots the cardinality of the source's Type.
	var srcSingle, dstSingle bool
	err := pool.QueryRow(testCtx(t),
		`select src_single, dst_single from links where source_id = $1 and link_type = 'blocks' and target_id = $2`,
		x, y).Scan(&srcSingle, &dstSingle)
	if err != nil || srcSingle || dstSingle {
		t.Errorf("links row: src_single %v, dst_single %v, err %v; want false, false", srcSingle, dstSingle, err)
	}
}

// Get shows links both ways, as each end sees them: the declared name from
// the source, the inverse name from the target.
func TestLink_GetShowsBothDirections(t *testing.T) {
	w := newLinkWorld(t)
	x, y, z := w.open(t), w.open(t), w.open(t)
	w.mustLink(t, w.ana, x, "blocks", y)
	w.mustLink(t, w.ana, x, "duplicate_of", z)
	w.mustLink(t, w.ana, z, "blocks", x)

	got, more := w.linksOf(t, w.ana, x)
	assertLinks(t, "x", got, out("blocks", y), out("duplicate_of", z), inc("blocked_by", z))
	if more {
		t.Error("x: LinksTruncated with three links")
	}
	got, _ = w.linksOf(t, w.ana, y)
	assertLinks(t, "y", got, inc("blocked_by", x))
	got, _ = w.linksOf(t, w.ana, z)
	assertLinks(t, "z", got, out("blocks", x), inc("duplicates", x))
}

// assertLinksInOrder checks the links and their exact order.
func assertLinksInOrder(t *testing.T, what string, got []item.Link, want ...item.Link) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: links in order = %v\nwant %v", what, got, want)
	}
}

// byID returns the two IDs in ascending order.
func byID(a, b uuid.UUID) (uuid.UUID, uuid.UUID) {
	if strings.Compare(a.String(), b.String()) > 0 {
		return b, a
	}
	return a, b
}

// The order is fixed: outgoing before incoming; within a direction by the
// link's created_at, then its declared name (link_type), then the other
// end's ID (the target for outgoing, the source for incoming).
func TestLink_GetOrder(t *testing.T) {
	w := newLinkWorld(t)
	x := w.open(t)
	y, q, z, v, m := w.open(t), w.open(t), w.open(t), w.open(t), w.open(t)
	// Created in this order: by created_at alone.
	w.mustLink(t, w.ana, x, "duplicate_of", z)
	w.mustLink(t, w.ana, x, "blocks", y)
	w.mustLink(t, w.ana, x, "about", m)
	w.mustLink(t, w.ana, x, "blocks", q)
	w.mustLink(t, w.ana, z, "duplicate_of", x)
	w.mustLink(t, w.ana, v, "blocks", x)
	w.mustLink(t, w.ana, m, "about", x)
	w.mustLink(t, w.ana, z, "blocks", x)

	got, _ := w.linksOf(t, w.ana, x)
	assertLinksInOrder(t, "by created_at", got,
		out("duplicate_of", z), out("blocks", y), out("about", m), out("blocks", q),
		inc("duplicates", z), inc("blocked_by", v), inc("mentioned_by", m), inc("blocked_by", z))

	// A tie on created_at: then by declared name, then by the other end.
	if _, err := pool.Exec(testCtx(t),
		`update links set created_at = '2026-01-01T00:00:00Z' where source_id = $1 or target_id = $1`, x); err != nil {
		t.Fatalf("tie the links: %v", err)
	}
	b1, b2 := byID(y, q)
	i1, i2 := byID(v, z)
	got, _ = w.linksOf(t, w.ana, x)
	assertLinksInOrder(t, "tied on created_at", got,
		out("about", m), out("blocks", b1), out("blocks", b2), out("duplicate_of", z),
		// Incoming: by the declared name (about < blocks < duplicate_of),
		// shown as its inverse.
		inc("mentioned_by", m), inc("blocked_by", i1), inc("blocked_by", i2), inc("duplicates", z))
}

// A link name keeps its inverse across the versions of a Type: a version
// that declares it with another inverse is refused when it is registered,
// with an error naming the Type, the link and both inverses.
func TestRegisterType_LinkInverseAgreesAcrossVersions(t *testing.T) {
	k := itemservice.New(pool)
	id := "inv-" + uuid.NewString()[:8]
	register := func(version int, inverse string) error {
		ty, err := typedef.Parse(fmt.Appendf(nil,
			"id: %s\nversion: %d\nlinks:\n  - {name: blocks, inverse: %s, targets: [%s]}\n", id, version, inverse, id))
		if err != nil {
			t.Fatalf("parse v%d: %v", version, err)
		}
		return k.RegisterType(testCtx(t), ty)
	}
	if err := register(1, "blocked_by"); err != nil {
		t.Fatalf("register v1: %v", err)
	}
	if err := register(2, "blocked_by"); err != nil {
		t.Errorf("register v2 with the same inverse: %v", err)
	}
	err := register(3, "held_up_by")
	if err == nil {
		t.Fatal("v3 with another inverse was registered")
	}
	for _, s := range []string{id, "blocks", "blocked_by", "held_up_by"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not name %s", err, s)
		}
	}
	if _, err := k.Type(testCtx(t), id, 3); err == nil {
		t.Error("the refused v3 is registered anyway")
	}
}

// Unlink records link_removed on the source and the link is gone from both
// ends; the same link may be added again later.
func TestUnlink_RemovesTheLinkWithAnEvent(t *testing.T) {
	w := newLinkWorld(t)
	x, y := w.open(t), w.open(t)
	w.mustLink(t, w.ana, x, "blocks", y)

	res, err := w.unlink(t, w.ana, x, "blocks", y)
	if err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	if res.ItemID != x || res.ItemSeq != 3 {
		t.Errorf("Unlink = %+v, want the source at item_seq 3", res)
	}
	op, payload := event(t, x, 3)
	if op != "link_removed" || len(payload) != 2 || payload["link_type"] != "blocks" || payload["target"] != y.String() {
		t.Errorf("event 3 = %s %v, want link_removed {link_type: blocks, target: %s}", op, payload, y)
	}
	if n := count(t, `select count(*) from links where source_id = $1`, x); n != 0 {
		t.Errorf("%d links rows left after Unlink", n)
	}
	got, _ := w.linksOf(t, w.ana, x)
	assertLinks(t, "x after Unlink", got)
	got, _ = w.linksOf(t, w.ana, y)
	assertLinks(t, "y after Unlink", got)
	if seq := itemSeqOf(t, y); seq != 1 {
		t.Errorf("target item_seq = %d after Unlink, want 1", seq)
	}

	if res := w.mustLink(t, w.ana, x, "blocks", y); res.ItemSeq != 4 {
		t.Errorf("Link again = %+v, want item_seq 4", res)
	}
}

// Each refusal names its reason and writes nothing.
func TestLink_Refusals(t *testing.T) {
	w := newLinkWorld(t)
	x, y := w.open(t), w.open(t)
	w.mustLink(t, w.ana, x, "blocks", y)
	ticket := createAs(t, w.k, w.ana, "a ticket", nil, nil).ItemID // ticket v1: not a target of any link here
	before := itemSeqOf(t, x)

	invalidAt := func(what string, err error, path string) {
		t.Helper()
		assertInvalidAt(t, what, err, path)
	}
	_, err := w.link(t, w.ana, x, "nope", y)
	invalidAt("unknown link type", err, "link_type")
	_, err = w.link(t, w.ana, x, "blocked_by", y)
	invalidAt("an inverse name", err, "link_type")
	_, err = w.link(t, w.ana, x, "", y)
	invalidAt("no link type", err, "link_type")
	_, err = w.link(t, w.ana, x, "blocks", x)
	invalidAt("a link to itself", err, "target")
	_, err = w.unlink(t, w.ana, x, "nope", y)
	invalidAt("unlink, unknown link type", err, "link_type")

	_, err = w.link(t, w.ana, x, "blocks", y)
	assertRejectedCode(t, "the same link again", err, itemservice.CodeAlreadyLinked)
	_, err = w.link(t, w.ana, x, "about", ticket)
	assertRejectedCode(t, "a target of a Type the link does not allow", err, itemservice.CodeLinkTargetType)
	_, err = w.unlink(t, w.ana, y, "blocks", x)
	assertRejectedCode(t, "unlink what is not linked", err, itemservice.CodeNotLinked)

	for name, req := range map[string]itemservice.LinkRequest{
		"no key":   {Principal: w.ana, SourceID: x, LinkType: "blocks", TargetID: w.open(t)},
		"no actor": {IdempotencyKey: newKey(), Principal: item.Principal{Namespace: "default"}, SourceID: x, LinkType: "blocks", TargetID: y},
		"untyped":  {IdempotencyKey: newKey(), Principal: item.Principal{Actor: "ana", Namespace: "default"}, SourceID: x, LinkType: "blocks", TargetID: y},
		"a kernel actor": {IdempotencyKey: newKey(), Principal: item.Principal{Actor: "system:timer", Namespace: "default"},
			SourceID: x, LinkType: "blocks", TargetID: y},
		"kernel attribution": {IdempotencyKey: newKey(), Principal: w.ana, SourceID: x, LinkType: "blocks", TargetID: y,
			Attribution: item.Attribution{Hop: 1}},
	} {
		if _, err := w.k.Link(testCtx(t), req); !errors.Is(err, itemservice.ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
	if after := itemSeqOf(t, x); after != before {
		t.Errorf("refusals moved the source from item_seq %d to %d", before, after)
	}
}

// A max-one source side: a second link of the type from the same source is
// LINK_CARDINALITY, side "source"; not a replacement.
func TestLink_Cardinality_SourceSide(t *testing.T) {
	w := newLinkWorld(t)
	x, y, z := w.open(t), w.open(t), w.open(t)
	w.mustLink(t, w.ana, x, "duplicate_of", y)

	_, err := w.link(t, w.ana, x, "duplicate_of", z)
	assertRefusedMeta(t, "a second duplicate_of", err, itemservice.CodeLinkCardinality,
		map[string]string{"link_type": "duplicate_of", "side": "source"})
	got, _ := w.linksOf(t, w.ana, x)
	assertLinks(t, "x after the refusal", got, out("duplicate_of", y))

	// The target side is many: another source may be a duplicate of y.
	w.mustLink(t, w.ana, z, "duplicate_of", y)
	// After an unlink the source's slot is free again.
	if _, err := w.unlink(t, w.ana, x, "duplicate_of", y); err != nil {
		t.Fatalf("Unlink: %v", err)
	}
	w.mustLink(t, w.ana, x, "duplicate_of", z)
}

// A max-one target side: a second source is LINK_CARDINALITY, side
// "target"; the metadata never names the source that holds the slot.
func TestLink_Cardinality_TargetSide(t *testing.T) {
	w := newLinkWorld(t)
	a, b, target, other := w.open(t), w.open(t), w.open(t), w.open(t)
	w.mustLink(t, w.ana, a, "claims", target)

	_, err := w.link(t, w.ana, b, "claims", target)
	assertRefusedMeta(t, "a second claim on the target", err, itemservice.CodeLinkCardinality,
		map[string]string{"link_type": "claims", "side": "target"})
	var rej *itemservice.RejectedError
	if errors.As(err, &rej) {
		for k, v := range rej.Metadata {
			if strings.Contains(v, a.String()) {
				t.Errorf("metadata %s = %q names the source holding the slot", k, v)
			}
		}
		if strings.Contains(rej.Error(), a.String()) {
			t.Errorf("error %q names the source holding the slot", rej)
		}
	}
	// The source side is many.
	w.mustLink(t, w.ana, a, "claims", other)
}

// Cardinality is enforced by the database, not check-then-write: sources
// racing for one target's single slot (no lock in common but the target's
// key-share) end with exactly one link.
func TestLink_Cardinality_HoldsUnderConcurrency(t *testing.T) {
	w := newLinkWorld(t)
	target := w.open(t)
	const n = 16
	sources := make([]uuid.UUID, n)
	for i := range sources {
		sources[i] = w.open(t)
	}
	errs := make([]error, n)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range sources {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, errs[i] = w.k.Link(context.Background(), w.req(w.ana, sources[i], "claims", target))
		}()
	}
	close(start)
	wg.Wait()
	won := 0
	for i, err := range errs {
		switch {
		case err == nil:
			won++
		default:
			var rej *itemservice.RejectedError
			if !errors.As(err, &rej) || rej.Code != itemservice.CodeLinkCardinality {
				t.Errorf("source %d: error = %v, want nil or link_cardinality", i, err)
			}
		}
	}
	if won != 1 {
		t.Errorf("%d sources claimed the target, want exactly 1", won)
	}
	if rows := count(t, `select count(*) from links where target_id = $1 and link_type = 'claims'`, target); rows != 1 {
		t.Errorf("%d claims rows on the target, want 1", rows)
	}

	// And one source racing to many targets for its single slot.
	src := w.open(t)
	targets := make([]uuid.UUID, n)
	for i := range targets {
		targets[i] = w.open(t)
	}
	won = 0
	var mu sync.Mutex
	for i := range targets {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := w.k.Link(context.Background(), w.req(w.ana, src, "duplicate_of", targets[i]))
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				won++
			} else if !errors.Is(err, itemservice.ErrRejected) {
				t.Errorf("duplicate_of %d: error = %v", i, err)
			}
		}()
	}
	wg.Wait()
	if won != 1 {
		t.Errorf("%d duplicate_of links from one source, want exactly 1", won)
	}
}

// The same key again returns the stored result and writes nothing; with a
// different request it is a reuse.
func TestLink_Idempotent(t *testing.T) {
	w := newLinkWorld(t)
	x, y, z := w.open(t), w.open(t), w.open(t)
	req := w.req(w.ana, x, "blocks", y)
	first, err := w.k.Link(testCtx(t), req)
	if err != nil {
		t.Fatalf("Link: %v", err)
	}
	again, err := w.k.Link(testCtx(t), req)
	if err != nil || again != first {
		t.Errorf("Link again = %+v, %v; want %+v", again, err, first)
	}
	if n := eventCount(t, x); n != 2 {
		t.Errorf("%d events on the source, want 2", n)
	}
	req.TargetID = z
	if _, err := w.k.Link(testCtx(t), req); !errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		t.Errorf("same key, another target: error = %v, want ErrIdempotencyKeyReuse", err)
	}
	// Unlink with Link's key is another request.
	req.TargetID = y
	if _, err := w.k.Unlink(testCtx(t), req); !errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		t.Errorf("Unlink with Link's key: error = %v, want ErrIdempotencyKeyReuse", err)
	}
}

// The etag (ExpectedItemSeq) is the source's.
func TestLink_ExpectedItemSeqIsTheSources(t *testing.T) {
	w := newLinkWorld(t)
	x, y := w.open(t), w.open(t)
	w.mustMove(t, w.ana, y, "s_work") // the target is at item_seq 2, the source at 1

	req := w.req(w.ana, x, "blocks", y)
	req.ExpectedItemSeq = 2
	_, err := w.k.Link(testCtx(t), req)
	assertStale(t, err)
	req.ExpectedItemSeq = 1
	if _, err := w.k.Link(testCtx(t), req); err != nil {
		t.Fatalf("Link with the source's item_seq: %v", err)
	}
	un := w.req(w.ana, x, "blocks", y)
	un.ExpectedItemSeq = 1
	_, err = w.k.Unlink(testCtx(t), un)
	assertStale(t, err)
	un.ExpectedItemSeq = 2
	if _, err := w.k.Unlink(testCtx(t), un); err != nil {
		t.Fatalf("Unlink with the source's item_seq: %v", err)
	}
}

// A target may be of any version of an allowed Type, and of any Type the
// link type lists.
func TestLink_AnyVersionOfAnAllowedTarget(t *testing.T) {
	w := newLinkWorld(t)
	x := w.open(t)
	v2 := w.make(t, w.ana, "s_open", itemOpts{version: 2})
	w.mustLink(t, w.ana, x, "blocks", v2)

	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: w.ana, TypeID: w.other, TypeVersion: 1,
		Fields: map[string]any{"f_title": "other"},
	})
	if err != nil {
		t.Fatalf("create other: %v", err)
	}
	w.mustLink(t, w.ana, x, "about", res.ItemID)
	_, err = w.link(t, w.ana, x, "blocks", res.ItemID) // blocks targets the world's Type only
	assertRejectedCode(t, "blocks to the other Type", err, itemservice.CodeLinkTargetType)
}

// A link is added or removed only by someone who sees both ends. An end
// they cannot see is NOT_FOUND, like a missing one; nothing is written.
func TestLink_BothEndsMustBeVisible(t *testing.T) {
	w := newLinkWorld(t)
	open := w.open(t)
	secret := w.make(t, w.ana, "s_open", itemOpts{narrow: readers(w.ana)})
	w.mustLink(t, w.ana, open, "blocks", secret)

	for name, err := range map[string]error{
		"bob links to a hidden target": func() error { _, err := w.link(t, w.bob, open, "about", secret); return err }(),
		"bob links from a hidden source": func() error {
			_, err := w.link(t, w.bob, secret, "about", open)
			return err
		}(),
		"bob unlinks a link to a hidden target": func() error {
			_, err := w.unlink(t, w.bob, open, "blocks", secret)
			return err
		}(),
		"a missing target": func() error { _, err := w.link(t, w.ana, open, "about", uuid.New()); return err }(),
		"a missing source": func() error { _, err := w.link(t, w.ana, uuid.New(), "about", open); return err }(),
	} {
		assertNotFound(t, name, err)
	}
	if seq := itemSeqOf(t, open); seq != 2 {
		t.Errorf("open is at item_seq %d, want 2 (only ana's link)", seq)
	}
}

// Outgoing links to an Item the caller cannot see are restricted (no ID);
// incoming links from one are left out. Linking never makes anything
// visible.
func TestLink_HiddenEnds(t *testing.T) {
	w := newLinkWorld(t)
	x, y := w.open(t), w.open(t)
	secret := w.make(t, w.ana, "s_open", itemOpts{narrow: readers(w.ana)})
	hidden := w.make(t, w.ana, "s_open", itemOpts{narrow: readers(w.ana)})
	w.mustLink(t, w.ana, x, "blocks", secret)
	w.mustLink(t, w.ana, x, "blocks", y)
	w.mustLink(t, w.ana, hidden, "blocks", x)

	got, _ := w.linksOf(t, w.ana, x)
	assertLinks(t, "x as ana", got, out("blocks", secret), out("blocks", y), inc("blocked_by", hidden))
	got, _ = w.linksOf(t, w.bob, x)
	assertLinks(t, "x as bob", got, restrictedOut("blocks"), out("blocks", y))
	for _, id := range []uuid.UUID{secret, hidden} {
		assertHidden(t, w.k, w.bob, id)
	}
}

// Another namespace's Item is NOT_FOUND at either end.
func TestLink_StaysInItsNamespace(t *testing.T) {
	w := newLinkWorld(t)
	x := w.open(t)
	ns := newNamespace(t, w.k)
	there := in(person("there"), ns)
	theirs := w.make(t, there, "s_open", itemOpts{})
	_, err := w.link(t, w.ana, x, "blocks", theirs)
	assertNotFound(t, "a target in another namespace", err)
	_, err = w.link(t, there, theirs, "blocks", x)
	assertNotFound(t, "from another namespace to here", err)
}

// Get shows at most MaxLinksShown links per direction, restricted ones
// included, and says when there are more.
func TestLink_GetShowsAtMostMaxLinksShown(t *testing.T) {
	w := newLinkWorld(t)
	x := w.open(t)
	for i := range itemservice.MaxLinksShown {
		o := itemOpts{}
		if i < 5 {
			o.narrow = readers(w.ana) // restricted for bob
		}
		w.mustLink(t, w.ana, x, "blocks", w.make(t, w.ana, "s_open", o))
	}
	for _, p := range []item.Principal{w.ana, w.bob} {
		got, more := w.linksOf(t, p, x)
		if len(got) != itemservice.MaxLinksShown || more {
			t.Errorf("as %s with %d links: %d shown, truncated %v; want all, not truncated",
				p.Actor, itemservice.MaxLinksShown, len(got), more)
		}
	}
	w.mustLink(t, w.ana, x, "blocks", w.open(t))
	for _, p := range []item.Principal{w.ana, w.bob} {
		got, more := w.linksOf(t, p, x)
		if len(got) != itemservice.MaxLinksShown || !more {
			t.Errorf("as %s with one more: %d shown, truncated %v; want %d, truncated",
				p.Actor, len(got), more, itemservice.MaxLinksShown)
		}
	}
}

// History shows link events, with the target restricted for a reader who
// cannot see it.
func TestLink_HistoryShowsLinkEvents(t *testing.T) {
	w := newLinkWorld(t)
	x, y := w.open(t), w.open(t)
	secret := w.make(t, w.ana, "s_open", itemOpts{narrow: readers(w.ana)})
	w.mustLink(t, w.ana, x, "blocks", y)
	w.mustLink(t, w.ana, x, "blocks", secret)
	if _, err := w.unlink(t, w.ana, x, "blocks", y); err != nil {
		t.Fatalf("Unlink: %v", err)
	}

	for _, c := range []struct {
		p          item.Principal
		secretSeen bool
	}{{w.ana, true}, {w.bob, false}} {
		evs := history(t, w.k, c.p, x)
		if len(evs) != 4 {
			t.Fatalf("as %s: %d events, want 4", c.p.Actor, len(evs))
		}
		want := []struct {
			op     string
			target uuid.UUID
		}{{"link_added", y}, {"link_added", secret}, {"link_removed", y}}
		for i, wv := range want {
			ev := evs[i+1]
			if ev.Op != wv.op || ev.LinkType != "blocks" || ev.LinkTarget == nil {
				t.Errorf("as %s, event %d = %s %q target %v; want %s blocks", c.p.Actor, ev.ItemSeq, ev.Op, ev.LinkType, ev.LinkTarget, wv.op)
				continue
			}
			hidden := wv.target == secret && !c.secretSeen
			if hidden && (!ev.LinkTarget.Restricted || ev.LinkTarget.ID != uuid.Nil) {
				t.Errorf("as %s, event %d: target %+v, want restricted with no ID", c.p.Actor, ev.ItemSeq, ev.LinkTarget)
			}
			if !hidden && (ev.LinkTarget.Restricted || ev.LinkTarget.ID != wv.target) {
				t.Errorf("as %s, event %d: target %+v, want %s", c.p.Actor, ev.ItemSeq, ev.LinkTarget, wv.target)
			}
		}
	}
}

// Checks read fields, not links: a link may change while a hold is open.
func TestLink_WhileAHoldIsOpen(t *testing.T) {
	w := newLinkWorld(t)
	x := w.make(t, w.ana, "s_work", itemOpts{fields: map[string]any{"f_customer_id": "c1", "f_amount": 10}})
	res := w.mustMove(t, w.ana, x, "s_check")
	if res.Outcome != itemservice.OutcomePending {
		t.Fatalf("asking for s_check = %+v, want a pending hold", res)
	}
	y := w.open(t)
	w.mustLink(t, w.ana, x, "blocks", y)
	if _, err := w.unlink(t, w.ana, x, "blocks", y); err != nil {
		t.Errorf("Unlink during the hold: %v", err)
	}
}

// The source's Type decides the outbox, as for any event; the target
// gets nothing.
func TestLink_OutboxIsTheSources(t *testing.T) {
	w := newLinkWorld(t) // the world's Type has a check hook: it has listeners
	x, y := w.open(t), w.open(t)
	w.mustLink(t, w.ana, x, "blocks", y)
	if n := count(t, `select count(*) from outbox where item_id = $1 and item_seq = 2`, x); n != 1 {
		t.Errorf("%d outbox rows for the link event, want 1", n)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, y); n != 1 { // its create only
		t.Errorf("%d outbox rows on the target, want 1 (its create)", n)
	}
}
