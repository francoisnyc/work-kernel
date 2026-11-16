// Tests for proof of concept Step 1: links and principal fields over the
// API. LinkItems and UnlinkItems write on the source (its etag, its
// history); GetItem and every write's answer show links both ways; a link
// filter and "me" on a principal field narrow SearchItems and ListItems;
// GetType shows link types and principal fields.
package api_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/api"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
)

const ticketV5 = "namespaces/acme/types/ticket/versions/5"

// The link writes reach the kernel with the token's namespace, like every
// other call (kernel_calls_test.go).
func (w *watchedKernel) Link(ctx context.Context, r itemservice.LinkRequest) (itemservice.WriteResult, error) {
	w.record("Link", r.Principal)
	return w.Kernel.Link(ctx, r)
}

func (w *watchedKernel) Unlink(ctx context.Context, r itemservice.LinkRequest) (itemservice.WriteResult, error) {
	w.record("Unlink", r.Principal)
	return w.Kernel.Unlink(ctx, r)
}

func linkItems(who, src, linkType, dst, etag string) (*kernelv1.LinkItemsResponse, error) {
	res, err := as(who).LinkItems(context.Background(), connect.NewRequest(&kernelv1.LinkItemsRequest{
		Name: src, RequestId: newID(), LinkType: linkType, Target: dst, Etag: etag,
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func unlinkItems(who, src, linkType, dst string) (*kernelv1.UnlinkItemsResponse, error) {
	res, err := as(who).UnlinkItems(context.Background(), connect.NewRequest(&kernelv1.UnlinkItemsRequest{
		Name: src, RequestId: newID(), LinkType: linkType, Target: dst,
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func mustLinkItems(t *testing.T, who, src, linkType, dst string) *kernelv1.LinkItemsResponse {
	t.Helper()
	res, err := linkItems(who, src, linkType, dst, "")
	if err != nil {
		t.Fatalf("LinkItems %s %s %s as %s: %v", src, linkType, dst, who, err)
	}
	return res
}

// linkStrings renders links as "OUTGOING blocks <name>" (restricted:
// "OUTGOING blocks restricted"), in their order.
func linkStrings(ls []*kernelv1.Link) []string {
	var out []string
	for _, l := range ls {
		other := l.GetItem().GetName()
		if l.GetItem().GetRestricted() {
			other = "restricted"
		}
		out = append(out, strings.TrimPrefix(l.Direction.String(), "LINK_DIRECTION_")+" "+l.LinkType+" "+other)
	}
	return out
}

func assertWireLinks(t *testing.T, what string, got []*kernelv1.Link, want ...string) {
	t.Helper()
	g := linkStrings(got)
	// Outgoing first; within a direction, any order.
	for i := 1; i < len(g); i++ {
		if strings.HasPrefix(g[i-1], "INCOMING") && strings.HasPrefix(g[i], "OUTGOING") {
			t.Errorf("%s: outgoing after incoming: %v", what, g)
		}
	}
	slices.Sort(g)
	w := slices.Clone(want)
	slices.Sort(w)
	if !slices.Equal(g, w) {
		t.Errorf("%s: links = %v\nwant %v", what, g, want)
	}
}

func ticket5(t *testing.T, who string) string {
	t.Helper()
	return create(t, who, ticketV5, map[string]any{"f_title": "linked " + word()}).Item.Name
}

// Link, see it from both ends, unlink, read the history.
func TestLinkItems_AddAndRemove(t *testing.T) {
	t1, t2 := ticket5(t, "ana"), ticket5(t, "ana")

	res := mustLinkItems(t, "ana", t1, "blocks", t2)
	if res.Item.GetName() != t1 || res.ItemSeq != 2 || res.CommitSeq == 0 || res.Item.Etag != "2" {
		t.Errorf("LinkItems = item %s at %d (etag %q), commit %d; want the source at 2",
			res.Item.GetName(), res.ItemSeq, res.Item.GetEtag(), res.CommitSeq)
	}
	assertWireLinks(t, "the answer", res.Item.Links, "OUTGOING blocks "+t2)
	assertWireLinks(t, "t2", get(t, "bob", t2).Links, "INCOMING blocked_by "+t1)
	if e := get(t, "ana", t2).Etag; e != "1" {
		t.Errorf("target etag = %q, want 1: a link writes nothing on its target", e)
	}

	un, err := unlinkItems("ana", t1, "blocks", t2)
	if err != nil {
		t.Fatalf("UnlinkItems: %v", err)
	}
	if un.ItemSeq != 3 || len(un.Item.Links) != 0 {
		t.Errorf("UnlinkItems = %d with links %v, want 3 and none", un.ItemSeq, linkStrings(un.Item.Links))
	}
	assertWireLinks(t, "t2 after unlink", get(t, "ana", t2).Links)

	hist, err := as("bob").ListItemEvents(testCtx(t), connect.NewRequest(&kernelv1.ListItemEventsRequest{Parent: t1}))
	if err != nil {
		t.Fatalf("ListItemEvents: %v", err)
	}
	evs := hist.Msg.Events
	if len(evs) != 3 {
		t.Fatalf("%d events, want 3", len(evs))
	}
	for i, kind := range []kernelv1.ItemEventKind{
		kernelv1.ItemEventKind_ITEM_EVENT_KIND_LINK_ADDED, kernelv1.ItemEventKind_ITEM_EVENT_KIND_LINK_REMOVED,
	} {
		ev := evs[i+1]
		if ev.Kind != kind || ev.LinkType != "blocks" || ev.GetLinkTarget().GetName() != t2 {
			t.Errorf("event %d = %v %q %v, want %v blocks %s", ev.ItemSeq, ev.Kind, ev.LinkType, ev.LinkTarget, kind, t2)
		}
	}
}

// Every refusal a client can cause, with its code, reason and metadata.
func TestLinkItems_Refusals(t *testing.T) {
	t1, t2, t3 := ticket5(t, "ana"), ticket5(t, "ana"), ticket5(t, "ana")
	room := create(t, "ana", channel, map[string]any{"f_name": "room " + word()}).Item.Name
	secret, err := as("ana").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV5, Narrow: &kernelv1.Readers{Principals: []string{"user:ana"}},
	}))
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	mustLinkItems(t, "ana", t1, "blocks", t2)
	mustLinkItems(t, "ana", t1, "duplicate_of", t2)
	before := countEvents(t, t1)

	_, err = as("ana").LinkItems(testCtx(t), connect.NewRequest(&kernelv1.LinkItemsRequest{Name: t1, LinkType: "blocks", Target: t3}))
	invalid(t, "no request_id", err, "request_id")
	_, err = linkItems("ana", "items/1", "blocks", t3, "")
	invalid(t, "a malformed name", err, "name")
	_, err = linkItems("ana", t1, "blocks", "nope", "")
	invalid(t, "a malformed target", err, "target")
	_, err = linkItems("ana", t1, "", t3, "")
	invalid(t, "no link_type", err, "link_type")
	_, err = linkItems("ana", t1, "nope", t3, "")
	invalid(t, "an unknown link type", err, "link_type")
	_, err = linkItems("ana", t1, "blocked_by", t3, "")
	invalid(t, "an inverse name", err, "link_type")
	_, err = linkItems("ana", t1, "blocks", t1, "")
	invalid(t, "a link to itself", err, "target")

	_, err = linkItems("ana", t1, "blocks", t2, "")
	apiError(t, "the same link again", err, connect.CodeFailedPrecondition, "ALREADY_LINKED")
	_, err = unlinkItems("ana", t2, "blocks", t1)
	apiError(t, "unlink what is not linked", err, connect.CodeFailedPrecondition, "NOT_LINKED")
	_, err = linkItems("ana", t3, "blocks", room, "")
	apiError(t, "a channel is not a ticket", err, connect.CodeFailedPrecondition, "LINK_TARGET_TYPE")
	_, err = linkItems("ana", t1, "duplicate_of", t3, "")
	ce := apiError(t, "a second duplicate_of", err, connect.CodeFailedPrecondition, "LINK_CARDINALITY")
	if info := errorInfo(t, ce); info != nil &&
		(info.Metadata["link_type"] != "duplicate_of" || info.Metadata["side"] != "source") {
		t.Errorf("LINK_CARDINALITY metadata = %v, want link_type duplicate_of, side source", info.Metadata)
	}
	_, err = linkItems("ana", t1, "blocks", t3, "1")
	apiError(t, "a stale etag", err, connect.CodeAborted, "STALE")
	_, err = linkItems("bob", t1, "blocks", secret.Msg.Item.Name, "")
	notFound(t, "a target bob cannot see", err)
	_, err = linkItems("bob", secret.Msg.Item.Name, "blocks", t1, "")
	notFound(t, "a source bob cannot see", err)
	_, err = linkItems("ana", t1, "blocks", strings.Replace(t3, "namespaces/acme", "namespaces/globex", 1), "")
	notFound(t, "a target in another namespace", err)
	_, err = linkItems("bea", t1, "blocks", t3, "")
	notFound(t, "a source in another namespace", err)

	// The same request_id with another request.
	id := newID()
	link := func(dst string) error {
		_, err := as("ana").LinkItems(testCtx(t), connect.NewRequest(&kernelv1.LinkItemsRequest{
			Name: t3, RequestId: id, LinkType: "blocks", Target: dst,
		}))
		return err
	}
	if err := link(t1); err != nil {
		t.Fatalf("link with a request_id: %v", err)
	}
	if err := link(t1); err != nil {
		t.Errorf("the same request again: %v", err)
	}
	apiError(t, "the request_id with another target", link(t2), connect.CodeAlreadyExists, "IDEMPOTENCY_KEY_REUSED")

	if after := countEvents(t, t1); after != before {
		t.Errorf("refusals wrote %d events on t1", after-before)
	}
	// Each new reason has its one fixed message.
	for _, r := range []string{"LINK_CARDINALITY", "ALREADY_LINKED", "NOT_LINKED", "LINK_TARGET_TYPE"} {
		if _, ok := api.ReasonMessage(r); !ok {
			t.Errorf("reason %s has no message", r)
		}
	}
}

// GetItem shows links as the caller sees them: an outgoing link to a
// hidden Item is restricted (no name); an incoming one from a hidden Item
// is left out.
func TestGetItem_LinksAsTheCallerSeesThem(t *testing.T) {
	x, y := ticket5(t, "ana"), ticket5(t, "ana")
	narrowed := func() string {
		res, err := as("ana").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
			Parent: "namespaces/acme", RequestId: newID(), Type: ticketV5,
			Narrow: &kernelv1.Readers{Principals: []string{"user:ana"}},
		}))
		if err != nil {
			t.Fatalf("create narrowed: %v", err)
		}
		return res.Msg.Item.Name
	}
	secret, hidden := narrowed(), narrowed()
	mustLinkItems(t, "ana", x, "blocks", y)
	mustLinkItems(t, "ana", x, "blocks", secret)
	mustLinkItems(t, "ana", hidden, "blocks", x)

	assertWireLinks(t, "x as ana", get(t, "ana", x).Links,
		"OUTGOING blocks "+y, "OUTGOING blocks "+secret, "INCOMING blocked_by "+hidden)
	bobs := get(t, "bob", x)
	assertWireLinks(t, "x as bob", bobs.Links, "OUTGOING blocks "+y, "OUTGOING blocks restricted")
	for _, l := range bobs.Links {
		if l.GetItem().GetRestricted() && l.GetItem().GetName() != "" {
			t.Errorf("a restricted link names %q", l.GetItem().GetName())
		}
	}
	if bobs.LinksTruncated {
		t.Error("links_truncated with three links")
	}
	// Search results carry no links.
	res, err := as("ana").SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
		Parent: "namespaces/acme", Filter: &kernelv1.ItemFilter{Links: []*kernelv1.LinkFilter{
			{LinkType: "blocked_by", Direction: kernelv1.LinkDirection_LINK_DIRECTION_INCOMING, Item: x},
		}},
	}))
	if err != nil {
		t.Fatalf("SearchItems: %v", err)
	}
	if len(res.Msg.Items) != 2 {
		t.Fatalf("blocked_by x: %d Items, want 2 (y and secret)", len(res.Msg.Items))
	}
	for _, it := range res.Msg.Items {
		if len(it.Links) != 0 {
			t.Errorf("search result %s carries links", it.Name)
		}
	}
}

// The link filter and "me", over the wire.
func TestSearchItems_LinkAndMeFilters(t *testing.T) {
	x, y := ticket5(t, "ana"), ticket5(t, "ana")
	mustLinkItems(t, "ana", x, "blocks", y)
	search := func(who string, f *kernelv1.ItemFilter) ([]string, error) {
		res, err := as(who).SearchItems(testCtx(t), connect.NewRequest(&kernelv1.SearchItemsRequest{
			Parent: "namespaces/acme", Filter: f, PageSize: 500,
		}))
		if err != nil {
			return nil, err
		}
		var names []string
		for _, it := range res.Msg.Items {
			names = append(names, it.Name)
		}
		return names, nil
	}
	lf := func(name string, dir kernelv1.LinkDirection, item string) *kernelv1.ItemFilter {
		return &kernelv1.ItemFilter{Links: []*kernelv1.LinkFilter{{LinkType: name, Direction: dir, Item: item}}}
	}
	got, err := search("bob", lf("blocks", kernelv1.LinkDirection_LINK_DIRECTION_OUTGOING, y))
	if err != nil || !slices.Equal(got, []string{x}) {
		t.Errorf("blocks y = %v, %v; want [%s]", got, err, x)
	}
	got, err = search("bob", lf("blocked_by", kernelv1.LinkDirection_LINK_DIRECTION_INCOMING, x))
	if err != nil || !slices.Equal(got, []string{y}) {
		t.Errorf("blocked_by x = %v, %v; want [%s]", got, err, y)
	}
	_, err = search("bob", lf("blocks", kernelv1.LinkDirection_LINK_DIRECTION_UNSPECIFIED, y))
	invalid(t, "no direction", err, "filter.links[0]")
	_, err = search("bob", lf("blokcs", kernelv1.LinkDirection_LINK_DIRECTION_OUTGOING, y))
	invalid(t, "a name no Type declares", err, "filter.links[0]")
	_, err = search("bob", lf("blocks", kernelv1.LinkDirection_LINK_DIRECTION_INCOMING, y))
	invalid(t, "a declared name, incoming", err, "filter.links[0]")
	_, err = search("bob", lf("blocks", kernelv1.LinkDirection_LINK_DIRECTION_OUTGOING, "items/1"))
	invalid(t, "a malformed item", err, "filter.links[0].item")
	_, err = search("bea", lf("blocks", kernelv1.LinkDirection_LINK_DIRECTION_OUTGOING,
		strings.Replace(y, "namespaces/acme", "namespaces/globex", 1)))
	notFound(t, "an item of another namespace", err)

	w := word()
	mine := create(t, "ana", ticketV5, map[string]any{"f_title": w, "f_assignee": "user:bob"}).Item.Name
	create(t, "ana", ticketV5, map[string]any{"f_title": w, "f_assignee": "user:ana"})
	me := &kernelv1.ItemFilter{
		Types:  []string{"namespaces/acme/types/ticket"},
		Fields: []*kernelv1.FieldFilter{{Field: "f_title", Eq: str(w)}, {Field: "f_assignee", Eq: str("me")}},
	}
	got, err = search("bob", me)
	if err != nil || !slices.Equal(got, []string{mine}) {
		t.Errorf("assigned to bob (me) = %v, %v; want [%s]", got, err, mine)
	}
	me.Fields[1].Eq = str("bob")
	_, err = search("bob", me)
	invalid(t, "a bare name on a principal field", err, "filter.fields[1]")

	_, err = as("ana").CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: ticketV5, Fields: fields(t, map[string]any{"f_assignee": "bob"}),
	}))
	invalid(t, "an untyped principal", err, "fields.f_assignee")
}

// GetType shows the link types and the principal field.
func TestGetType_LinkTypesAndPrincipalFields(t *testing.T) {
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: ticketV5}))
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	ty := res.Msg.Type
	var lts []string
	for _, l := range ty.LinkTypes {
		lts = append(lts, strings.Join([]string{l.Name, l.Inverse, strings.Join(l.Targets, ","),
			boolWord(l.MaxOne), boolWord(l.InverseMaxOne)}, " "))
	}
	want := []string{
		"blocks blocked_by namespaces/acme/types/ticket many many",
		"duplicate_of duplicates namespaces/acme/types/ticket one many",
	}
	if !slices.Equal(lts, want) {
		t.Errorf("link types = %v\nwant %v", lts, want)
	}
	for _, f := range ty.Fields {
		if f.Id == "f_assignee" && f.Type != kernelv1.FieldType_FIELD_TYPE_PRINCIPAL {
			t.Errorf("f_assignee is %v, want FIELD_TYPE_PRINCIPAL", f.Type)
		}
	}
}

func boolWord(one bool) string {
	if one {
		return "one"
	}
	return "many"
}

// An agent needs a write power to link; the bot acting for bob only reads.
func TestLinkItems_ReadOnlyAgent(t *testing.T) {
	t1, t2 := ticket5(t, "bob"), ticket5(t, "bob")
	session := startSession(t, "bot", "user:bob", []string{"namespaces/acme"}, "read")
	_, err := asAgent("bot", session).LinkItems(testCtx(t), connect.NewRequest(&kernelv1.LinkItemsRequest{
		Name: t1, RequestId: newID(), LinkType: "blocks", Target: t2,
	}))
	apiError(t, "a read-only session links", err, connect.CodePermissionDenied, "NOT_ALLOWED")
}

// The order of links is fixed: outgoing before incoming; within a
// direction by created_at, then the declared name, then the other end's
// name. A tie on created_at shows the rest of the order.
func TestGetItem_LinkOrder(t *testing.T) {
	x := ticket5(t, "ana")
	a, b, c := ticket5(t, "ana"), ticket5(t, "ana"), ticket5(t, "ana")
	mustLinkItems(t, "ana", x, "duplicate_of", c)
	mustLinkItems(t, "ana", x, "blocks", b)
	mustLinkItems(t, "ana", x, "blocks", a)
	mustLinkItems(t, "ana", b, "blocks", x)
	mustLinkItems(t, "ana", a, "duplicate_of", x)
	mustLinkItems(t, "ana", a, "blocks", x)

	inOrder := func(what string, want ...string) {
		t.Helper()
		if got := linkStrings(get(t, "ana", x).Links); !slices.Equal(got, want) {
			t.Errorf("%s: links = %v\nwant %v", what, got, want)
		}
	}
	inOrder("by created_at",
		"OUTGOING duplicate_of "+c, "OUTGOING blocks "+b, "OUTGOING blocks "+a,
		"INCOMING blocked_by "+b, "INCOMING duplicates "+a, "INCOMING blocked_by "+a)

	id := x[len(x)-36:]
	if _, err := pool.Exec(testCtx(t),
		`update links set created_at = '2026-01-01T00:00:00Z' where source_id = $1 or target_id = $1`, id); err != nil {
		t.Fatalf("tie the links: %v", err)
	}
	lo, hi := a, b
	if lo > hi { // names differ only in their IDs, so this is ID order
		lo, hi = hi, lo
	}
	inOrder("tied on created_at",
		"OUTGOING blocks "+lo, "OUTGOING blocks "+hi, "OUTGOING duplicate_of "+c,
		"INCOMING blocked_by "+lo, "INCOMING blocked_by "+hi, "INCOMING duplicates "+a)
}
