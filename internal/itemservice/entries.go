package itemservice

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// entryVersions reads an Item's history as what it was (track 1, item 6,
// step 3a): each event is judged by the version of its Type the Item was
// on when the event was written, read from its migrated events. An action,
// an enrich hook, a reaction or an undo is owed for an entry only if that
// entry's own version declares it, and it is finished by that version's
// records.
//
// It is the one place the readers of pending work learn versions from:
// pendingActions, pendingReactions, pendingUndos and owedEnrich. The
// workflow's loader (StateEntry) and the migrate's quiet check (notQuiet)
// both go through them, so they read history the same way.
type entryVersions struct {
	k     *Service
	q     rowsQuerier
	cur   *typedef.Type
	ms    []lifecycle.Migration
	times []time.Time // when each migrate was written, for causes outside the Item
	types map[int]*typedef.Type
}

// readEntryVersions reads the Item's migrates. cur is the version the
// Item is on now. An Item never migrated costs one query and loads nothing.
func (k *Service) readEntryVersions(ctx context.Context, q rowsQuerier, id uuid.UUID, cur *typedef.Type) (*entryVersions, error) {
	rows, err := q.Query(ctx,
		`select item_seq, (payload ->> 'from_version')::int, (payload ->> 'to_version')::int, created_at
		 from item_events where item_id = $1 and op = $2 order by item_seq`, id, lifecycle.OpMigrated)
	if err != nil {
		return nil, fmt.Errorf("read migrates: %w", err)
	}
	type row struct {
		m  lifecycle.Migration
		at time.Time
	}
	// CollectRows calls the function once per row; a Go closure here plays
	// the part a lambda would in Java.
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
		var x row
		return x, r.Scan(&x.m.Seq, &x.m.From, &x.m.To, &x.at)
	})
	if err != nil {
		return nil, fmt.Errorf("read migrates: %w", err)
	}
	ev := &entryVersions{k: k, q: q, cur: cur, types: map[int]*typedef.Type{cur.Version: cur}}
	for _, x := range got {
		ev.ms = append(ev.ms, x.m)
		ev.times = append(ev.times, x.at)
	}
	return ev, nil
}

// load returns version v of the Item's Type.
func (ev *entryVersions) load(ctx context.Context, v int) (*typedef.Type, error) {
	if ty, ok := ev.types[v]; ok {
		return ty, nil
	}
	ty, err := ev.k.loadType(ctx, ev.q, ev.cur.ID, v)
	if err != nil {
		return nil, err
	}
	ev.types[v] = ty
	return ty, nil
}

// at is the version the Item was on when its event seq was written.
func (ev *entryVersions) at(ctx context.Context, seq int64) (*typedef.Type, error) {
	return ev.load(ctx, lifecycle.VersionAt(ev.ms, ev.cur.Version, seq))
}

// atTime is the version the Item was on at instant t: for a cause that is
// not one of its own events (an Item created inside it).
func (ev *entryVersions) atTime(ctx context.Context, t time.Time) (*typedef.Type, error) {
	v := ev.cur.Version
	if len(ev.ms) > 0 {
		v = ev.ms[0].From
		for i, m := range ev.ms {
			if ev.times[i].After(t) {
				break
			}
			v = m.To
		}
	}
	return ev.load(ctx, v)
}

// all is every version the Item has been on, oldest first.
func (ev *entryVersions) all(ctx context.Context) ([]*typedef.Type, error) {
	vs := []int{ev.cur.Version}
	for _, m := range ev.ms {
		vs = append(vs, m.From, m.To)
	}
	slices.Sort(vs)
	var out []*typedef.Type
	for _, v := range slices.Compact(vs) {
		ty, err := ev.load(ctx, v)
		if err != nil {
			return nil, err
		}
		out = append(out, ty)
	}
	return out, nil
}

// owedEnrich returns the enrich hooks owed for the entry into state at
// event entered, by the version that entry was made under, that have not
// written for it (ran). A hook that ran and wrote nothing (no fallback) is
// owed again; its key makes any second write a no-op.
func (k *Service) owedEnrich(ctx context.Context, q rowsQuerier, id uuid.UUID, cur *typedef.Type,
	state string, entered int64, ran []string) ([]string, error) {
	ev, err := k.readEntryVersions(ctx, q, id, cur)
	if err != nil {
		return nil, err
	}
	ty, err := ev.at(ctx, entered)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, h := range lifecycle.EnrichHooks(ty, state) {
		if !slices.Contains(ran, h.ID) {
			out = append(out, h.ID)
		}
	}
	return out, nil
}
