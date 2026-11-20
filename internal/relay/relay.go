// Package relay moves events from the outbox to Temporal. It delivers each
// Item's events in item_seq order, one at a time, and marks an outbox row
// delivered once the workflow has dealt with the event, or once it is clear
// that no workflow is listening.
//
// The relay reads events and Types, and writes only outbox.delivered_at. It
// never touches Item state.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
	"github.com/francoisnyc/work-kernel/internal/workflow"
)

// ErrNothingListening is what Workflows.Deliver returns when a plain Update
// finds no running workflow: there never was one, or it has completed. For
// most Items that is the normal case.
var ErrNothingListening = errors.New("relay: no workflow is listening")

// Delivery is one event on its way to an Item's workflow.
type Delivery struct {
	ItemID string // also the workflow ID
	// UpdateID is "item:seq". A redelivery carries the same ID, so Temporal
	// can recognize it as a duplicate.
	UpdateID string
	Event    workflow.Event
	// Child, when set, makes this a different delivery: not an event of
	// ItemID, but "an Item was created inside ItemID" (workflow.UpdateChild),
	// sent to a container whose Type reacts to it. Event is then empty.
	Child *workflow.ChildEvent
	// Start asks for Update-With-Start: start the workflow if none runs.
	// Without it the update only reaches a workflow that already runs.
	Start bool
}

// Workflows is the one thing the relay needs from Temporal. Keeping it this
// small lets the tests put a fake in its place.
type Workflows interface {
	// Deliver returns nil only when the workflow has completed the update;
	// "accepted" is not enough, because an accepted update can still fail.
	// It returns ErrNothingListening when Start is false and no workflow runs.
	Deliver(ctx context.Context, d Delivery) error
}

// Options tune a Relay. The zero value of each field means "use the default".
type Options struct {
	PollInterval time.Duration    // pause between passes; default 200ms
	Backoff      time.Duration    // wait after an Item's first failure; default 1s
	MaxBackoff   time.Duration    // the wait doubles up to this; default 1m
	Now          func() time.Time // the clock; default time.Now
	Logger       *slog.Logger     // default slog.Default()
}

// Relay is a single-instance relay: run one at a time (see "Later" in the
// journal for what several would need).
type Relay struct {
	types     typedef.Cache // versions never change, so this never goes stale
	pool      *pgxpool.Pool
	workflows Workflows
	opts      Options

	// failing remembers the Items whose last delivery failed. It is only
	// touched by the goroutine running the passes, so it needs no lock. It is
	// lost on restart, which just means one early retry.
	failing map[uuid.UUID]backoff
}

type backoff struct {
	wait  time.Duration // how long the Item is waiting this time
	until time.Time     // no attempt before this
}

// New returns a Relay that reads the outbox through pool and delivers to workflows.
func New(pool *pgxpool.Pool, workflows Workflows, opts Options) *Relay {
	if opts.PollInterval <= 0 {
		opts.PollInterval = 200 * time.Millisecond
	}
	if opts.Backoff <= 0 {
		opts.Backoff = time.Second
	}
	if opts.MaxBackoff <= 0 {
		opts.MaxBackoff = time.Minute
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Relay{pool: pool, workflows: workflows, opts: opts, failing: map[uuid.UUID]backoff{}}
}

// Run makes passes until ctx is canceled, then returns nil. A pass that fails
// (the database is away, say) is logged and tried again after the interval.
func (r *Relay) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.opts.PollInterval)
	defer ticker.Stop()
	for {
		if err := r.RunOnce(ctx); err != nil && ctx.Err() == nil {
			r.opts.Logger.Error("relay pass failed", "error", err)
		}
		// select waits for whichever channel is ready first.
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// RunOnce makes one pass: for every Item with undelivered events, it
// delivers them in order until one fails. A failing Item does not stop the
// pass; RunOnce returns an error only when the pass itself cannot go on.
func (r *Relay) RunOnce(ctx context.Context) error {
	items, err := r.itemsWithPending(ctx)
	if err != nil {
		return err
	}
	for _, id := range items {
		if b, ok := r.failing[id]; ok && r.opts.Now().Before(b.until) {
			continue // still backing off
		}
		if err := r.deliverItem(ctx, id); err != nil {
			if ctx.Err() != nil {
				return ctx.Err() // we are being stopped; not the Item's fault
			}
			r.backOff(id, err)
			continue
		}
		delete(r.failing, id)
	}
	return nil
}

func (r *Relay) backOff(id uuid.UUID, cause error) {
	wait := r.opts.Backoff
	if b, ok := r.failing[id]; ok {
		wait = min(b.wait*2, r.opts.MaxBackoff)
	}
	r.failing[id] = backoff{wait: wait, until: r.opts.Now().Add(wait)}
	r.opts.Logger.Warn("delivery failed; the item backs off",
		"item", id, "retry_in", wait, "error", cause)
}

func (r *Relay) itemsWithPending(ctx context.Context) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx,
		`select distinct item_id from outbox where delivered_at is null order by item_id`)
	if err != nil {
		return nil, fmt.Errorf("list items with undelivered events: %w", err)
	}
	// CollectRows reads every row and closes the result set.
	ids, err := pgx.CollectRows(rows, pgx.RowTo[uuid.UUID])
	if err != nil {
		return nil, fmt.Errorf("list items with undelivered events: %w", err)
	}
	return ids, nil
}

// pendingEvent is one undelivered outbox row with the event it points to.
type pendingEvent struct {
	Seq           int64
	Op            string
	Payload       []byte
	At            time.Time
	CorrelationID string
	Hop           int
	TypeID        string
	TypeVersion   int
}

// deliverItem sends one Item's undelivered events, lowest item_seq first,
// and stops at the first that fails, so nothing is ever delivered out of order.
func (r *Relay) deliverItem(ctx context.Context, id uuid.UUID) error {
	rows, err := r.pool.Query(ctx,
		`select o.item_seq, e.op, e.payload, e.created_at,
		        coalesce(e.correlation_id, ''), e.hop, i.type_id, i.type_version
		 from outbox o
		 join item_events e on e.item_id = o.item_id and e.item_seq = o.item_seq
		 join items i on i.id = o.item_id
		 where o.item_id = $1 and o.delivered_at is null
		 order by o.item_seq`, id)
	if err != nil {
		return fmt.Errorf("read undelivered events: %w", err)
	}
	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[pendingEvent])
	if err != nil {
		return fmt.Errorf("read undelivered events: %w", err)
	}

	for _, ev := range events {
		d, err := r.delivery(ctx, id, ev)
		if err != nil {
			return err
		}
		err = r.workflows.Deliver(ctx, d)
		switch {
		case err == nil:
		case errors.Is(err, ErrNothingListening) && !d.Start:
			r.opts.Logger.Debug("no workflow is listening; marking delivered", "update", d.UpdateID)
		default:
			return fmt.Errorf("deliver %s: %w", d.UpdateID, err)
		}
		_, err = r.pool.Exec(ctx,
			`update outbox set delivered_at = now() where item_id = $1 and item_seq = $2`, id, ev.Seq)
		if err != nil {
			// The event was delivered but not marked. It will be sent again
			// under the same update ID, and recognized as a duplicate.
			return fmt.Errorf("mark %s delivered: %w", d.UpdateID, err)
		}
	}
	return nil
}

// delivery turns an event row into what is sent: its shape, never a field
// value, plus the Type's answer to "does this start a workflow?".
func (r *Relay) delivery(ctx context.Context, id uuid.UUID, ev pendingEvent) (Delivery, error) {
	// Only the keys that say which state the event enters, and which hold
	// it opens or closes, are read: opaque IDs and state names.
	var shape struct {
		State          string `json:"state"`            // create
		From           string `json:"from"`             // transition and the hold events
		To             string `json:"to"`               // transition
		RequestedTo    string `json:"requested_to"`     // transition_requested, _denied, _failed
		HoldID         string `json:"hold_id"`          // every event that opens or closes a hold
		CanceledHoldID string `json:"canceled_hold_id"` // an escape
		Reaction       string `json:"reaction"`         // reaction_applied: <type>:<reaction>
		// The fields the event set and cleared: IDs, never values.
		FieldIDs        []string `json:"field_ids"`
		ClearedFieldIDs []string `json:"cleared_field_ids"`
	}
	if err := json.Unmarshal(ev.Payload, &shape); err != nil {
		return Delivery{}, fmt.Errorf("decode payload of event %d: %w", ev.Seq, err)
	}
	// Belt and braces: only a create or a transition enters a state, whatever
	// keys another event's payload carries.
	var entered string
	switch ev.Op {
	case "create":
		entered = shape.State
	case "transition":
		entered = shape.To
	}
	// An escape names the hold it canceled; it is passed as the hold the
	// event closes, marked Canceled so it is never read as allowed.
	holdID, canceled := shape.HoldID, false
	if holdID == "" && shape.CanceledHoldID != "" {
		holdID, canceled = shape.CanceledHoldID, true
	}
	// An applied undo names its reaction, for the hooks that run on it
	// (the refund email); the workflow knows reactions by their own ID.
	var reaction string
	if ev.Op == lifecycle.OpReactionApplied {
		_, reaction, _ = strings.Cut(shape.Reaction, ":")
	}

	ty, err := r.types.Load(ev.TypeID, ev.TypeVersion, func() (*typedef.Type, error) {
		return r.loadType(ctx, ev.TypeID, ev.TypeVersion)
	})
	if err != nil {
		return Delivery{}, err
	}
	// For a Type with a timer at a field's instant: which of those fields
	// the event set or cleared, so the workflow reads the instant again.
	// Still a shape: IDs the Type itself declares, no value. Every other
	// Type, and every other field, adds nothing to what is sent.
	var atFields []string
	for _, f := range ty.AtFields() { // sorted
		if slices.Contains(shape.FieldIDs, f) || slices.Contains(shape.ClearedFieldIDs, f) {
			atFields = append(atFields, f)
		}
	}
	return Delivery{
		ItemID:   id.String(),
		UpdateID: fmt.Sprintf("%s:%d", id, ev.Seq),
		Event: workflow.Event{
			Seq: ev.Seq, Op: ev.Op, From: shape.From, To: entered, At: ev.At,
			RequestedTo: shape.RequestedTo, HoldID: holdID, Canceled: canceled, Reaction: reaction,
			FieldIDs:      atFields,
			CorrelationID: ev.CorrelationID, Hop: ev.Hop,
		},
		// The Type decides, in one place, whether this event needs a
		// running workflow: it enters a state with behavior, or opens a hold.
		Start: lifecycle.StartsWorkflow(ty, lifecycle.Event{Op: ev.Op, Entered: entered, From: shape.From,
			ClosesHold: holdID != "" && ev.Op != lifecycle.OpTransitionRequested,
			Allowed:    ev.Op == "transition" && holdID != "" && !canceled}),
	}, nil
}

func (r *Relay) loadType(ctx context.Context, id string, version int) (*typedef.Type, error) {
	var def []byte
	err := r.pool.QueryRow(ctx,
		`select definition from types where id = $1 and version = $2`, id, version).Scan(&def)
	if err != nil {
		return nil, fmt.Errorf("read type %s v%d: %w", id, version, err)
	}
	return typedef.FromJSON(def)
}
