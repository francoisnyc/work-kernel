package story

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

// renderer is the run's Renderer, with the names given so far.
func (h *harness) renderer() Renderer {
	people := map[string]string{}
	for name, p := range h.conv.people {
		people[p] = name
	}
	h.mu.Lock()
	items := make(map[uuid.UUID]string, len(h.names))
	for id, n := range h.names {
		items[id] = n
	}
	h.mu.Unlock()
	return Renderer{Day1: h.story.Day1, People: people, Items: items, Hidden: h.story.HideValues, Catalog: h.env.catalog}
}

// observer is the person whose reads make the timeline.
func (h *harness) observer() string {
	if h.story.Observer != "" {
		return h.story.Observer
	}
	return DefaultObserver
}

type storedItem struct {
	id          uuid.UUID
	typeID      string
	typeVersion int
	container   *uuid.UUID
}

// items lists the run's Items in the order they were created.
func (h *harness) items(ctx context.Context) ([]storedItem, error) {
	rows, err := h.env.pool.Query(ctx, `select i.id, i.type_id, i.type_version, i.container_id
		from items i join item_events e on e.item_id = i.id and e.item_seq = 1
		where i.namespace_id = $1 order by e.created_at, e.commit_seq`, h.ns)
	if err != nil {
		return nil, fmt.Errorf("story: list the Items: %w", err)
	}
	defer rows.Close()
	var out []storedItem
	for rows.Next() {
		var it storedItem
		if err := rows.Scan(&it.id, &it.typeID, &it.typeVersion, &it.container); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// ensureNamed names every Item of the run that has no name yet: by the
// first name rule it matches (name, name#2, ...), else TYPE#N, in the
// order the Items were created. A name, once given, stays.
func (h *harness) ensureNamed(ctx context.Context) {
	items, err := h.items(ctx)
	if err != nil {
		return
	}
	rd := h.renderer()
	h.mu.Lock()
	taken := map[string]bool{}
	for _, n := range h.names {
		taken[n] = true
	}
	h.mu.Unlock()
	next := func(base string, first bool) string {
		if first && !taken[base] {
			return base
		}
		for n := 2; ; n++ {
			if name := fmt.Sprintf("%s#%d", base, n); !taken[name] {
				return name
			}
		}
	}
	for _, it := range items {
		h.mu.Lock()
		_, named := h.names[it.id]
		var container string
		if it.container != nil {
			container = h.names[*it.container]
		}
		h.mu.Unlock()
		if named {
			continue
		}
		name := ""
		for _, r := range h.story.Names {
			if r.Type != it.typeID || (r.In != "" && r.In != container) {
				continue
			}
			if r.Where != nil && !h.createdWith(ctx, rd, it, *r.Where) {
				continue
			}
			name = next(r.Name, true)
			break
		}
		if name == "" {
			n := 1
			for taken[fmt.Sprintf("%s#%d", it.typeID, n)] {
				n++
			}
			name = fmt.Sprintf("%s#%d", it.typeID, n)
		}
		taken[name] = true
		h.mu.Lock()
		h.names[it.id] = name
		h.mu.Unlock()
	}
}

// createdWith reports whether the Item was created with field = value.
func (h *harness) createdWith(ctx context.Context, rd Renderer, it storedItem, a Assignment) bool {
	ty := h.env.byName[fmt.Sprintf("%s@%d", it.typeID, it.typeVersion)]
	f, ok := ty.Field(a.Field)
	if !ok {
		return false
	}
	var got any
	err := h.env.pool.QueryRow(ctx, `select v.field_values -> $2 from item_event_values v
		where v.item_id = $1 and v.item_seq = 1`, it.id, a.Field).Scan(&got)
	if err != nil {
		return false
	}
	want, err := h.conv.value(a.Value, fieldSlot(f))
	return err == nil && rd.value(got, fieldSlot(f)) == rd.value(want, fieldSlot(f))
}

// timeline reads every Item's history as the observer (the item service's
// public read, so through policy.Project) and writes it as lines, with
// the side effects the fakes made.
func (h *harness) timeline(ctx context.Context, res *Result) error {
	h.ensureNamed(ctx)
	items, err := h.items(ctx)
	if err != nil {
		return err
	}
	rd := h.renderer()
	observer := h.principal(h.observer())
	var lines []Line
	for _, it := range items {
		ty := h.env.byName[fmt.Sprintf("%s@%d", it.typeID, it.typeVersion)]
		token := ""
		for {
			page, err := h.k.ListEvents(ctx, itemservice.ListEventsRequest{Principal: observer, ItemID: it.id, Limit: 200, PageToken: token})
			if errors.Is(err, itemservice.ErrNotFound) {
				h.mu.Lock()
				h.failLocked(0, "the observer %s cannot see %s (%s@%d): name an observer who sees every Item of the story",
					h.observer(), h.names[it.id], it.typeID, it.typeVersion)
				h.mu.Unlock()
				break
			}
			if err != nil {
				return fmt.Errorf("story: the history of %s: %w", h.names[it.id], err)
			}
			for _, ev := range page.Events {
				h.mu.Lock()
				given := h.given[it.id][ev.ItemSeq]
				own := h.own[it.id][ev.ItemSeq]
				h.mu.Unlock()
				if given || (ev.Op == lifecycle.OpReactionSkipped && !h.story.ShowSkipped) {
					continue
				}
				lines = append(lines, rd.Event(ty, ev, own))
			}
			if page.NextPageToken == "" {
				break
			}
			token = page.NextPageToken
		}
	}
	h.mu.Lock()
	for _, c := range h.sideLines {
		lines = append(lines, rd.Call(c))
	}
	h.mu.Unlock()
	for i := range lines {
		lines[i].At = lines[i].At.UTC().Truncate(time.Microsecond)
	}
	slices.SortStableFunc(lines, func(a, b Line) int { return a.At.Compare(b.At) })
	res.Timeline = lines
	return nil
}
