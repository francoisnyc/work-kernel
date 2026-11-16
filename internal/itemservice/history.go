package itemservice

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/policy"
)

// ListEventsRequest asks for one page of an Item's history.
type ListEventsRequest struct {
	Principal item.Principal
	ItemID    uuid.UUID
	Limit     int    // as SearchRequest.Limit
	PageToken string // as SearchRequest.PageToken
}

// ListEventsResult is one page, oldest first by item_seq.
type ListEventsResult struct {
	Events        []Event
	NextPageToken string
	// The Item's Type version, which reads its field values.
	TypeID      string
	TypeVersion int
}

// Event is one accepted operation as a principal may see it: the event's
// shape, its values from item_event_values, and every other Item it names
// passed through visibility.
type Event struct {
	ItemID    uuid.UUID
	ItemSeq   int64
	CommitSeq int64
	// Op: create, update, transition, transition_requested,
	// transition_denied, transition_failed, hook_failed, link_added or
	// link_removed.
	Op        string
	CreatedAt time.Time

	// Attribution; the agent session is never shown.
	Actor, OnBehalfOf, OriginatingHook, CausationID, CorrelationID string
	Hop                                                            int

	TypeID      string // create
	TypeVersion int    // create
	From        string // the state left (transition) or held in
	To          string // the state entered (transition; create: the first state)
	RequestedTo string // a held transition's requested state
	HoldID      *uuid.UUID
	Hook        string // hook_failed
	Reason      string // a code: transition_denied, transition_failed, hook_failed

	// Values are the fields this event set, with their values; a cleared
	// field is present with a nil value. A field whose value was erased
	// from item_event_values is not here but in Erased.
	Values map[string]any
	Erased []string

	Readers []string // a narrowing: the new readers
	Inherit bool     // the Item dropped its own readers

	Container     *ItemRef // create in a container
	Moved         bool
	ContainerFrom *ItemRef // a move; nil is the top level
	ContainerTo   *ItemRef

	LinkType   string   // link_added, link_removed: the declared name
	LinkTarget *ItemRef // link_added, link_removed: the target
}

// ItemRef is another Item named by an event: its ID, or Restricted (and no
// ID) when the principal cannot see it.
type ItemRef struct {
	ID         uuid.UUID
	Restricted bool
}

// ListEvents returns one page of an Item's history, projected for the
// principal. An Item the principal cannot see (or outside an agent
// session's scopes) is ErrNotFound. Anyone who sees the Item sees its whole
// history, reader lists included; a container named by an event that the
// principal cannot see now is Restricted.
func (k *Service) ListEvents(ctx context.Context, req ListEventsRequest) (ListEventsResult, error) {
	if err := requireNamespace(req.Principal); err != nil {
		return ListEventsResult{}, err
	}
	limit, err := pageSize(req.Limit)
	if err != nil {
		return ListEventsResult{}, err
	}
	key := requestKey("events", req.Principal, req.ItemID)
	after, err := decodeToken(req.PageToken, key)
	if err != nil {
		return ListEventsResult{}, err
	}
	// The Item is read like Get reads it (visibility, session scopes,
	// namespace); an agent's read of it is recorded there.
	it, err := k.Get(ctx, req.Principal, req.ItemID)
	if err != nil {
		return ListEventsResult{}, err
	}
	// The one door out: the history belongs to an Item the principal may
	// see, as Project says.
	if _, ok := policy.Project(req.Principal, []uuid.UUID{*it.ScopeID}, it); !ok {
		return ListEventsResult{}, ErrNotFound
	}

	var afterSeq int64
	if after != nil {
		afterSeq = after.Seq
	}
	rows, err := k.pool.Query(ctx,
		`select e.item_seq, e.commit_seq, e.op, e.created_at, e.actor, coalesce(e.on_behalf_of, ''),
		        coalesce(e.originating_hook, ''), coalesce(e.causation_id, ''), coalesce(e.correlation_id, ''),
		        e.hop, coalesce(e.payload, '{}'), v.field_values
		 from item_events e
		 left join item_event_values v on v.item_id = e.item_id and v.item_seq = e.item_seq
		 where e.item_id = $1 and e.item_seq > $2
		 order by e.item_seq
		 limit $3`, req.ItemID, afterSeq, limit+1)
	if err != nil {
		return ListEventsResult{}, fmt.Errorf("read history: %w", err)
	}
	type raw struct {
		e       Event
		payload []byte
		values  []byte
	}
	found, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (raw, error) {
		var r raw
		r.e.ItemID = req.ItemID
		err := row.Scan(&r.e.ItemSeq, &r.e.CommitSeq, &r.e.Op, &r.e.CreatedAt, &r.e.Actor, &r.e.OnBehalfOf,
			&r.e.OriginatingHook, &r.e.CausationID, &r.e.CorrelationID, &r.e.Hop, &r.payload, &r.values)
		return r, err
	})
	if err != nil {
		return ListEventsResult{}, fmt.Errorf("read history: %w", err)
	}
	res := ListEventsResult{TypeID: it.TypeID, TypeVersion: it.TypeVersion}
	if len(found) > limit {
		found = found[:limit]
		res.NextPageToken = encodeToken(pageToken{Request: key, Seq: found[len(found)-1].e.ItemSeq})
	}

	refs := refResolver{k: k, p: req.Principal, seen: map[uuid.UUID]*ItemRef{}}
	for _, r := range found {
		e, err := r.e.withPayload(ctx, r.payload, r.values, &refs)
		if err != nil {
			return ListEventsResult{}, err
		}
		res.Events = append(res.Events, e)
	}
	return res, nil
}

// eventPayload is the stored shape of every kind of event (the keys the
// write paths use). Container keys are raw so that a key present with null
// (a move from the top level) differs from a key absent.
type eventPayload struct {
	TypeID         string          `json:"type_id"`
	TypeVersion    int             `json:"type_version"`
	State          string          `json:"state"`
	From           string          `json:"from"`
	To             string          `json:"to"`
	RequestedTo    string          `json:"requested_to"`
	HoldID         string          `json:"hold_id"`
	CanceledHoldID string          `json:"canceled_hold_id"`
	Hook           string          `json:"hook"`
	Reason         string          `json:"reason"`
	FieldIDs       []string        `json:"field_ids"`
	ClearedIDs     []string        `json:"cleared_field_ids"`
	Readers        []string        `json:"readers"`
	Inherit        bool            `json:"inherit"`
	ContainerID    *uuid.UUID      `json:"container_id"`
	ContainerFrom  json.RawMessage `json:"container_from"`
	ContainerTo    json.RawMessage `json:"container_to"`
}

// withPayload fills the event from its stored payload and values.
func (e Event) withPayload(ctx context.Context, payload, values []byte, refs *refResolver) (Event, error) {
	var pl eventPayload
	if err := json.Unmarshal(payload, &pl); err != nil {
		return e, fmt.Errorf("read event payload: %w", err)
	}
	var vals map[string]any
	if values != nil {
		if err := json.Unmarshal(values, &vals); err != nil {
			return e, fmt.Errorf("read event values: %w", err)
		}
	}

	e.TypeID, e.TypeVersion = pl.TypeID, pl.TypeVersion
	e.From, e.To, e.RequestedTo = pl.From, pl.To, pl.RequestedTo
	if e.Op == "create" {
		e.To = pl.State
	}
	e.Hook, e.Reason = pl.Hook, pl.Reason
	for _, h := range []string{pl.HoldID, pl.CanceledHoldID} {
		if id, err := uuid.Parse(h); err == nil {
			e.HoldID = &id
		}
	}
	e.Readers, e.Inherit = pl.Readers, pl.Inherit

	// Values: what the event set, from item_event_values; a field it set
	// whose value is gone was erased; a field it cleared is nil.
	for _, id := range pl.FieldIDs {
		if v, ok := vals[id]; ok {
			if e.Values == nil {
				e.Values = map[string]any{}
			}
			e.Values[id] = v
		} else {
			e.Erased = append(e.Erased, id)
		}
	}
	for _, id := range pl.ClearedIDs {
		if e.Values == nil {
			e.Values = map[string]any{}
		}
		e.Values[id] = nil
	}
	slices.Sort(e.Erased)

	var err error
	if pl.ContainerID != nil {
		if e.Container, err = refs.ref(ctx, *pl.ContainerID); err != nil {
			return e, err
		}
	}
	if pl.ContainerFrom != nil || pl.ContainerTo != nil {
		e.Moved = true
		if e.ContainerFrom, err = refs.raw(ctx, pl.ContainerFrom); err != nil {
			return e, err
		}
		if e.ContainerTo, err = refs.raw(ctx, pl.ContainerTo); err != nil {
			return e, err
		}
	}
	return e, nil
}

// refResolver passes the Items an event names through visibility, once per
// Item per call: one the principal cannot see now (or outside an agent
// session's scopes) is Restricted, with no ID.
type refResolver struct {
	k    *Service
	p    item.Principal
	seen map[uuid.UUID]*ItemRef
}

func (r *refResolver) ref(ctx context.Context, id uuid.UUID) (*ItemRef, error) {
	if ref, ok := r.seen[id]; ok {
		return ref, nil
	}
	ref := &ItemRef{ID: id}
	// Naming an Item in a history is not reading it: visibility decides
	// whether its ID is shown (namespace, readers, an agent session's
	// scopes), but no read is recorded, so a history never makes an agent's
	// later write need approval. (Review of Step 0, part 2: this used Get,
	// which recorded the read.)
	_, _, err := r.k.visibleItem(ctx, r.p, id)
	switch {
	case errors.Is(err, ErrNotFound):
		ref = &ItemRef{Restricted: true}
	case err != nil:
		return nil, err
	}
	r.seen[id] = ref
	return ref, nil
}

// raw resolves a container key that may be absent or null (the top level).
func (r *refResolver) raw(ctx context.Context, b json.RawMessage) (*ItemRef, error) {
	var id *uuid.UUID
	if len(b) > 0 {
		if err := json.Unmarshal(b, &id); err != nil {
			return nil, fmt.Errorf("read event container: %w", err)
		}
	}
	if id == nil {
		return nil, nil
	}
	return r.ref(ctx, *id)
}
