package rank

import (
	"context"
	"errors"
	"slices"
)

// A move: the one algorithm pli and the bench share for putting a card
// next to another. It is written against Source, the few ordered reads it
// needs, so the same code runs over the public API (pli) and in process
// (the bench).

// Card is one ranked Item of a container: an opaque ID and its key ("" when
// it has no rank yet).
type Card struct {
	ID  string
	Key string
}

// Position says where a moved card goes.
type Position int

const (
	Before Position = iota + 1 // right before another card
	After                      // right after another card
	Top                        // above every ranked card
	Bottom                     // below every ranked card
)

// Query selects the cards of one container that have a key: all of them,
// or those whose key is Eq, above Gt or below Lt (at most one is set). They
// come in key order, ascending or descending, ties by ID ascending either
// way: the kernel's order_by.
type Query struct {
	Eq, Gt, Lt string
	Desc       bool
	Limit      int
}

// Source reads a container's ranked cards.
type Source interface {
	Scan(ctx context.Context, q Query) ([]Card, error)
}

// Write is one key to write: the outcome of a move is one of them, or
// several when the neighbours were tied.
type Write struct {
	ID  string
	Key string
}

var (
	// ErrNoRank: the card to move next to has no rank, so "next to it"
	// names no place.
	ErrNoRank = errors.New("rank: the other card has no rank")
	// ErrTiedRunTooLong: the neighbours share one key with more than
	// MaxTiedRun cards. The move writes nothing; the container needs a
	// rebalance.
	ErrTiedRunTooLong = errors.New("rank: too many cards share one key; the container needs a rebalance")
	// ErrChanged: the container changed between the reads of one plan (the
	// other card was moved, or a neighbour was). Nothing is written; the
	// caller reads the other card again and plans again.
	ErrChanged = errors.New("rank: the cards moved while the move was planned")
)

// Plan works out what a move writes. In the usual case that is one key, for
// the moved card, strictly between its new neighbours in the container's
// order: the card then sits next to the other one in every view that shows
// both. When the two neighbours hold the same key no key fits between
// them, so the cards that share it are given fresh, evenly spaced keys
// with the moved card in its place: one write per tied card, never more
// than MaxTiedRun of them (ErrTiedRunTooLong beyond). other is ignored for
// Top and Bottom. The reads are not one snapshot: if the cards moved in
// between, Plan says ErrChanged rather than guess.
func Plan(ctx context.Context, src Source, moved string, pos Position, other Card) ([]Write, error) {
	// neighbour returns the key of the first card of q that is not the
	// moved card itself, "" when there is none.
	neighbour := func(q Query) (string, error) {
		q.Limit = 2
		cards, err := src.Scan(ctx, q)
		if err != nil {
			return "", err
		}
		for _, c := range cards {
			if c.ID != moved && c.Key != "" {
				return c.Key, nil
			}
		}
		return "", nil
	}
	one := func(lo, hi string) ([]Write, error) {
		key, err := Between(lo, hi)
		if errors.Is(err, ErrNoRoom) {
			// Each bound was read as strictly beyond the other card's key:
			// they can only meet if someone moved a card in between.
			return nil, ErrChanged
		}
		if err != nil {
			return nil, err
		}
		return []Write{{ID: moved, Key: key}}, nil
	}

	switch pos {
	case Top:
		first, err := neighbour(Query{})
		if err != nil {
			return nil, err
		}
		return one("", first)
	case Bottom:
		last, err := neighbour(Query{Desc: true})
		if err != nil {
			return nil, err
		}
		return one(last, "")
	}
	if other.Key == "" {
		return nil, ErrNoRank
	}

	// The cards that share the other card's key, in order. One more than
	// the cap (and the moved card, which may be among them) tells a run at
	// the cap from a longer one.
	limit := MaxTiedRun + 2
	run, err := src.Scan(ctx, Query{Eq: other.Key, Limit: limit})
	if err != nil {
		return nil, err
	}
	complete := len(run) < limit
	run = slices.DeleteFunc(run, func(c Card) bool { return c.ID == moved })
	at := slices.IndexFunc(run, func(c Card) bool { return c.ID == other.ID })

	below, err := neighbour(Query{Lt: other.Key, Desc: true})
	if err != nil {
		return nil, err
	}
	above, err := neighbour(Query{Gt: other.Key})
	if err != nil {
		return nil, err
	}
	// The other card is not among the cards with its key, and they were
	// all read: it was moved since the caller read it.
	if at < 0 && complete {
		return nil, ErrChanged
	}
	// The usual case: the other card is at the edge of its run on the side
	// the moved card goes, so the neighbour there has another key.
	switch {
	case pos == Before && at == 0:
		return one(below, other.Key)
	case pos == After && complete && at == len(run)-1:
		return one(other.Key, above)
	}
	// Tied neighbours: re-key the run, the moved card in its place.
	if !complete || len(run) > MaxTiedRun {
		return nil, ErrTiedRunTooLong
	}
	if pos == After {
		at++
	}
	order := slices.Insert(slices.Clone(run), at, Card{ID: moved})
	keys, err := Evenly(below, above, len(order))
	if errors.Is(err, ErrNoRoom) {
		return nil, ErrChanged
	}
	if err != nil {
		return nil, err
	}
	writes := make([]Write, len(order))
	for i, c := range order {
		writes[i] = Write{ID: c.ID, Key: keys[i]}
	}
	return writes, nil
}

// RewriteOrder returns the order in which a rebalance writes its cards so
// that the container never looks out of order in between: old[i] is card
// i's key now, target[i] the key it gets, both in the container's order.
// Cards whose key moves down are written first, from the top; then those
// whose key moves up, from the bottom. A card that goes down then lands
// below everything still above it, and one that goes up above everything
// still below it, whatever mix of old and new keys its neighbours hold. So
// a reader, or a move planned meanwhile, always sees the true order. Cards
// whose key does not change are left out.
func RewriteOrder(old, target []string) []int {
	var down, up []int
	for i := range old {
		switch c := Compare(target[i], old[i]); {
		case c < 0:
			down = append(down, i)
		case c > 0:
			up = append(up, i)
		}
	}
	slices.Reverse(up)
	return append(down, up...)
}
