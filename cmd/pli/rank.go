package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"strings"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/structpb"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/rank"
)

// Rank (Jira proof of concept, part 1). Ordering a backlog by hand is an
// app convention, not a kernel feature: a string field (f_rank) on the
// Types that want it, scoped to the Item's container. pli computes the keys
// with the shared package internal/rank and writes them with ordinary
// UpdateItem calls; the kernel only sorts by the field.
//
//	rank ITEM before|after OTHER   one write: ITEM lands next to OTHER in the
//	                               container's order, so in every view of it
//	rank ITEM top|bottom
//	rank rebalance --in CONTAINER  rewrite the container's keys, short and even;
//	                               a quiet-time batch: not while people drag
//
// A move never rebalances. When its key comes out longer than
// rank.Threshold, pli says so on stderr and still succeeds.

// rankField is the default field a rank is kept in.
const rankField = "f_rank"

// rank dispatches the rank commands: "rebalance", or a move.
func (c *cli) rank(ctx context.Context, args []string) error {
	if len(args) > 0 && args[0] == "rebalance" {
		return c.rankRebalance(ctx, args[1:])
	}
	return c.rankMove(ctx, args)
}

// ranked reads one container's ranked cards for rank.Plan, through
// ListItems: a field condition and order_by, nothing else.
type ranked struct {
	c      *cli
	parent string // the container's name, or namespaces/{ns} for the top level
	field  string
}

// Scan implements rank.Source. A method with this name and signature is all
// it takes: Go interfaces are satisfied implicitly, with no "implements".
func (r ranked) Scan(ctx context.Context, q rank.Query) ([]rank.Card, error) {
	ff := &kernelv1.FieldFilter{Field: r.field}
	switch {
	case q.Eq != "":
		ff.Eq = structpb.NewStringValue(q.Eq)
	case q.Gt != "":
		ff.Gt = structpb.NewStringValue(q.Gt)
	case q.Lt != "":
		ff.Lt = structpb.NewStringValue(q.Lt)
	default:
		ff = nil
	}
	req := &kernelv1.ListItemsRequest{Parent: r.parent, OrderBy: r.field, PageSize: int32(q.Limit)}
	if q.Desc {
		req.OrderBy += " desc"
	}
	if ff != nil {
		req.Filter = &kernelv1.ItemFilter{Fields: []*kernelv1.FieldFilter{ff}}
	}
	res, err := r.c.c.ListItems(ctx, connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	var cards []rank.Card
	for _, it := range res.Msg.Items {
		// Items without a rank come last in either order; they are not cards.
		if key := keyOf(it, r.field); key != "" {
			cards = append(cards, rank.Card{ID: it.Name, Key: key})
		}
	}
	return cards, nil
}

// keyOf is an Item's rank key, "" when it has none.
func keyOf(it *kernelv1.Item, field string) string {
	return it.GetFields().GetFields()[field].GetStringValue()
}

func (c *cli) rankMove(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rank", flag.ContinueOnError)
	field := fs.String("field", rankField, "the string field the rank is kept in")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return usagef("want ITEM before|after OTHER, or ITEM top|bottom")
	}
	var where rank.Position
	switch pos[1] {
	case "before":
		where = rank.Before
	case "after":
		where = rank.After
	case "top":
		where = rank.Top
	case "bottom":
		where = rank.Bottom
	default:
		return usagef("the position is before, after, top or bottom, not %q", pos[1])
	}
	relative := where == rank.Before || where == rank.After
	if relative && len(pos) != 3 {
		return usagef("%s wants the other card: ITEM %s OTHER", pos[1], pos[1])
	}
	if !relative && len(pos) != 2 {
		return usagef("%s takes no other card", pos[1])
	}
	name := c.itemRef(pos[0])
	if relative && c.itemRef(pos[2]) == name {
		return usagef("a card cannot be ranked %s itself", pos[1])
	}

	moved, err := c.c.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: name}))
	if err != nil {
		return err
	}
	container := moved.Msg.Item.Container
	parent, where2 := container, containerID(container)
	if container == "" {
		parent, where2 = "namespaces/"+c.ns, "the top level"
	}

	// The plan's reads are not one snapshot: if someone moves a card in
	// between, read the other card again and plan again, a few times.
	var writes []rank.Write
	for attempt := 0; ; attempt++ {
		var other rank.Card
		if relative {
			o, err := c.c.GetItem(ctx, connect.NewRequest(&kernelv1.GetItemRequest{Name: c.itemRef(pos[2])}))
			if err != nil {
				return err
			}
			// A rank orders one container: a key of another says nothing here.
			if o.Msg.Item.Container != container {
				return fmt.Errorf("the two cards are not in the same container: a rank orders one container")
			}
			other = rank.Card{ID: o.Msg.Item.Name, Key: keyOf(o.Msg.Item, *field)}
		}
		writes, err = rank.Plan(ctx, ranked{c: c, parent: parent, field: *field}, name, where, other)
		if !errors.Is(err, rank.ErrChanged) || attempt == 3 {
			break
		}
	}
	switch {
	case errors.Is(err, rank.ErrChanged):
		return fmt.Errorf("the cards there kept moving while the move was planned; nothing was written: try again")
	case errors.Is(err, rank.ErrNoRank):
		return fmt.Errorf("the other card has no rank yet: rank it first (pli rank ITEM top|bottom)")
	case errors.Is(err, rank.ErrTiedRunTooLong):
		return fmt.Errorf("more than %d cards there share one rank key, too many to re-key in a move; nothing was written: "+
			"run `pli rank rebalance --in %s` first", rank.MaxTiedRun, where2)
	case err != nil:
		return err
	}
	// One write; several only when the neighbours shared a key, and then
	// the moved card's comes last, so its answer is the one printed.
	var answer *kernelv1.UpdateItemResponse
	long := 0
	for _, w := range moveLast(writes, name) {
		res, err := c.setRank(ctx, w.ID, *field, w.Key, "")
		if err != nil {
			return err
		}
		answer = res
		long = max(long, lengthPast(w.Key))
	}
	// Maintenance never runs inside a move: say it is due, and succeed.
	if long > 0 {
		fmt.Fprintf(c.stderr, "pli: rank keys in %s have grown to %d bytes (past %d): run `pli rank rebalance --in %s`\n",
			where2, long, rank.Threshold, where2)
	}
	return c.print(answer)
}

// lengthPast is a key's length when it is past the threshold, else 0.
func lengthPast(key string) int {
	if rank.NeedsRebalance(key) {
		return len(key)
	}
	return 0
}

// moveLast puts the moved card's write after the others.
func moveLast(writes []rank.Write, moved string) []rank.Write {
	var out []rank.Write
	var last []rank.Write
	for _, w := range writes {
		if w.ID == moved {
			last = append(last, w)
		} else {
			out = append(out, w)
		}
	}
	return append(out, last...)
}

// containerID is an Item name's bare ID, for messages.
func containerID(name string) string { return name[strings.LastIndex(name, "/")+1:] }

// setRank writes one card's key: an ordinary update, with an etag when the
// write must not overwrite a newer one.
func (c *cli) setRank(ctx context.Context, name, field, key, etag string) (*kernelv1.UpdateItemResponse, error) {
	fields, err := structpb.NewStruct(map[string]any{field: key})
	if err != nil {
		return nil, err
	}
	res, err := c.c.UpdateItem(ctx, connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newRequestID(), Fields: fields, Etag: etag,
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// rankRebalance rewrites one container's keys short and evenly spaced, in
// the order they sort in now. It is a batch of ordinary updates, one per
// card whose key changes, each with the card's etag: a card someone moved
// meanwhile is skipped, their key stands. Cards without a rank, and other
// containers, are not touched. The container stays in order after every
// write, but a move made while it runs is placed between keys the batch
// may still change: run it when the board is quiet (the bench measures
// what happens otherwise).
func (c *cli) rankRebalance(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("rank rebalance", flag.ContinueOnError)
	in := fs.String("in", "", "the container whose keys to rewrite")
	field := fs.String("field", rankField, "the string field the rank is kept in")
	pos, err := parse(fs, args)
	if err != nil {
		return err
	}
	if *in == "" || len(pos) != 0 {
		return usagef("want --in CONTAINER")
	}
	parent := c.itemRef(*in)
	// Every ranked card, in order. Items without a rank come last: the
	// first one ends the read.
	var cards []*kernelv1.Item
	token := ""
read:
	for {
		res, err := c.c.ListItems(ctx, connect.NewRequest(&kernelv1.ListItemsRequest{
			Parent: parent, OrderBy: *field, PageSize: 500, PageToken: token,
		}))
		if err != nil {
			return err
		}
		for _, it := range res.Msg.Items {
			if keyOf(it, *field) == "" {
				break read // a labelled break leaves the outer loop
			}
			cards = append(cards, it)
		}
		if token = res.Msg.NextPageToken; token == "" {
			break
		}
	}
	keys, err := rank.Evenly("", "", len(cards))
	if err != nil {
		return err
	}
	// A quiet-time batch: say so before the first write, every time (the
	// rank bench: two drags in three made during a rebalance land out of
	// place).
	fmt.Fprintf(c.stderr, "pli: rebalancing rewrites the rank keys in %s: run it when nobody is dragging cards there "+
		"(a card moved while it runs can land out of place)\n", containerID(parent))
	report := struct {
		Container string `json:"container"`
		Rewritten int    `json:"rewritten"`
		Skipped   int    `json:"skipped"`
	}{Container: parent}
	old := make([]string, len(cards))
	for i, it := range cards {
		old[i] = keyOf(it, *field)
	}
	// In the order that keeps the container in order after every write
	// (rank.RewriteOrder): a reader, or a move made meanwhile, never sees
	// old and new keys out of step.
	for _, i := range rank.RewriteOrder(old, keys) {
		it := cards[i]
		_, err := c.setRank(ctx, it.Name, *field, keys[i], it.Etag)
		var ce *connect.Error
		switch {
		case err == nil:
			report.Rewritten++
		case errors.As(err, &ce) && ce.Code() == connect.CodeAborted:
			report.Skipped++ // moved meanwhile: its new key stands
		default:
			return err
		}
	}
	out, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(c.stdout, string(out))
	return err
}
