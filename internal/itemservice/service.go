// Package itemservice holds the write operations (create, update, transition, link,
// archive) and reads. Every change to an Item goes through this package.
package itemservice

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/policy"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Go reports failures as returned values, not exceptions. These "sentinel"
// errors are compared with errors.Is, much like catching a specific
// exception class in Java.
var (
	// ErrInvalid means the request is malformed (for example, no idempotency
	// key) or names a Type that is not registered.
	ErrInvalid = errors.New("itemservice: invalid request")
	// ErrNotFound means no Item has the given ID, or the principal may not
	// see it. The two cases are deliberately indistinguishable.
	ErrNotFound = errors.New("itemservice: item not found")
	// ErrRejected means the request is well formed but the Item's Type refuses
	// it: an unknown or mistyped field, or an illegal transition. The error
	// text carries the reason.
	ErrRejected = errors.New("itemservice: rejected")
	// ErrStale means the request named an expected item_seq and the Item has
	// moved since. Unlike ErrRejected, the write may well be fine: the caller
	// should re-read the Item and try again.
	ErrStale = errors.New("itemservice: stale")
	// ErrForbidden means the principal can see the Item and the move is legal,
	// but the transition's `allowed` list does not include them. It is never
	// returned for an Item the principal cannot see: that is ErrNotFound, so
	// the Item's existence does not leak.
	ErrForbidden = errors.New("itemservice: forbidden")
	// ErrNotCaughtUp means a read asked for a min_seq that was not reached
	// within its wait. Nothing is wrong: the caller may simply try again.
	ErrNotCaughtUp = errors.New("itemservice: not caught up")
	// ErrIdempotencyKeyReuse means the key was already used with a different request.
	ErrIdempotencyKeyReuse = errors.New("itemservice: idempotency key reused with a different request")
)

// defaultNamespace is the namespace of a principal that names none.
const defaultNamespace = "default"

// nsOf returns the namespace a principal acts in.
func nsOf(p item.Principal) string {
	if p.Namespace == "" {
		return defaultNamespace
	}
	return p.Namespace
}

// CreateRequest asks for a new Item.
type CreateRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	TypeID         string
	TypeVersion    int
	State          string         // optional; if set it must be the Type's initial state
	Fields         map[string]any // keyed by stable field ID
	ContainerID    *uuid.UUID
	Attribution    item.Attribution // optional; what caused this write
	// Narrow is optional. Without it the Item is in its container's scope
	// (the namespace's root scope when it has no container). With it the
	// Item gets a scope node of its own, readable only by the readers named,
	// and only by those of them who can read the container.
	Narrow *Narrow
}

// Narrow names the readers of a narrowed Item, as opaque principal IDs.
type Narrow struct {
	Readers []string
}

// WriteResult is returned by every write.
type WriteResult struct {
	ItemID    uuid.UUID `json:"item_id"`
	CommitSeq int64     `json:"commit_seq"`
	ItemSeq   int64     `json:"item_seq"`
}

// Service keeps all Item state in Postgres. The only thing it holds in
// memory is parsed Types, which never change once registered.
type Service struct {
	pool         *pgxpool.Pool
	types        *typedef.Cache // nil when Options.CacheTypes is false
	writeTimeout time.Duration
	maxWait      time.Duration    // the longest Transition.Wait accepted
	now          func() time.Time // the clock guards read today from
	clockSkew    time.Duration    // tests only: see Options.ClockSkew
}

// DefaultWriteTimeout bounds every statement of a write. A write that waits
// longer (on a lock, say) fails and rolls back instead of holding its
// transaction open: an open transaction holds back the min_seq watermark for
// the whole database.
const DefaultWriteTimeout = 5 * time.Second

// Options tune a Service.
type Options struct {
	// CacheTypes keeps each parsed Type version in memory after its first
	// read. New turns it on: in Step 5 the cache cut single-writer write
	// latency by 19-30% (docs/journal.md).
	CacheTypes bool
	// WriteTimeout is the statement timeout on the write path; zero means
	// DefaultWriteTimeout. There is no "no timeout".
	WriteTimeout time.Duration
	// MaxWait caps TransitionRequest.Wait; zero means MaxWait (5 s). Tests
	// set a short cap so they can test it without waiting 5 s.
	MaxWait time.Duration
	// Now is the clock guards read `today` from (as a UTC date); nil means
	// time.Now. Tests set a fixed clock.
	Now func() time.Time
	// ClockSkew is for tests only: it is added to the time every write is
	// stamped with (the event's created_at, cause.at), as if this service's
	// clock ran that far from the worker's. Negative: behind. No flag,
	// environment variable or config file sets it, and none may: a test
	// (TestClockSkew_NoProductionCodeSetsIt) reads the module's source and
	// fails if anything but a test names it.
	ClockSkew time.Duration
}

// New returns a Service that uses the given connection pool, with the Type
// cache on.
func New(pool *pgxpool.Pool) *Service {
	return NewWithOptions(pool, Options{CacheTypes: true})
}

// NewWithOptions returns a Service with the given options.
func NewWithOptions(pool *pgxpool.Pool, opts Options) *Service {
	k := &Service{pool: pool, writeTimeout: opts.WriteTimeout, maxWait: opts.MaxWait, now: opts.Now,
		clockSkew: opts.ClockSkew}
	if k.now == nil {
		k.now = time.Now
	}
	if k.writeTimeout <= 0 {
		k.writeTimeout = DefaultWriteTimeout
	}
	if k.maxWait <= 0 {
		k.maxWait = MaxWait
	}
	if opts.CacheTypes {
		k.types = &typedef.Cache{}
	}
	return k
}

// Create writes a new Item, its first event and, if its Type has listeners,
// an outbox row, in one transaction. The same idempotency key returns the stored result.
func (k *Service) Create(ctx context.Context, req CreateRequest) (WriteResult, error) {
	if req.IdempotencyKey == "" || req.Principal.Actor == "" || req.TypeID == "" {
		// %w wraps ErrInvalid, so callers can still match it with errors.Is,
		// like an exception with a cause.
		return WriteResult{}, fmt.Errorf("%w: idempotency key, actor and type are required", ErrInvalid)
	}
	if err := checkPublic(req.Principal, req.Attribution); err != nil {
		return WriteResult{}, err
	}
	if err := checkAttribution(req.Attribution); err != nil {
		return WriteResult{}, err
	}
	attr, err := sessionAttribution(req.Principal, req.Attribution)
	if err != nil {
		return WriteResult{}, err
	}
	var readers []string
	if req.Narrow != nil {
		var err error
		if readers, err = checkReaders(req.Narrow.Readers, req.Principal); err != nil {
			return WriteResult{}, err
		}
	}
	hash, err := requestHash("create", req)
	if err != nil {
		return WriteResult{}, err
	}

	// A Type version never changes once registered, so it can be read
	// outside the write transaction.
	ty, err := k.loadType(ctx, k.pool, req.TypeID, req.TypeVersion)
	if err != nil {
		return WriteResult{}, err
	}
	// The Type chooses the initial state; a request may name it but not another.
	if req.State != "" && req.State != ty.InitialState {
		return WriteResult{}, rejected(CodeNotInitialState, "type %s v%d starts in state %q, not %q",
			ty.ID, ty.Version, ty.InitialState, req.State)
	}
	if err := ty.CheckFields(req.Fields, false); err != nil {
		return WriteResult{}, fieldsInvalid(err)
	}
	// Create is the public door only: a kernel-written field is refused.
	writer, err := publicWriter(ctx, k.pool, ty, req.Principal, req.Fields)
	if err != nil {
		return WriteResult{}, err
	}
	if err := checkPatch(ty, "", nil, req.Fields, writer); err != nil {
		return WriteResult{}, err
	}
	// Date-time values are stored in one form, whatever offset they came in.
	fields := ty.NormalizeFields(req.Fields)
	// Multi-value fields: a list is its set; a set operation is malformed
	// here; a set over its max is refused.
	if fields, _, err = applySets(ty, nil, fields, true); err != nil {
		return WriteResult{}, err
	}
	if fields == nil {
		fields = map[string]any{}
	}

	return k.write(ctx, req.Principal, req.IdempotencyKey, hash, func(tx pgx.Tx) (WriteResult, error) {
		id, err := uuid.NewV7()
		if err != nil {
			return WriteResult{}, fmt.Errorf("new item id: %w", err)
		}
		// The Item starts in its container's node (made now if this is the
		// container's first child), or the root scope. A container the
		// principal cannot see does not exist for them.
		// The scope the new Item lands under, before childScope may give the
		// container a node: the target of the agent write rule.
		ns := nsOf(req.Principal)
		target, err := landingScope(ctx, tx, ns, req.ContainerID)
		if err != nil {
			return WriteResult{}, err
		}
		scopeID, err := childScope(ctx, tx, ns, req.ContainerID)
		if err != nil {
			return WriteResult{}, err
		}
		if err := checkVisible(ctx, tx, req.Principal, scopeID); err != nil {
			return WriteResult{}, err
		}
		// An agent session creates only inside its scopes (at the top level
		// only if it covers the whole namespace), and only with a write power.
		if err := checkInSession(ctx, tx, req.Principal, req.ContainerID); err != nil {
			return WriteResult{}, err
		}
		if err := checkWritePower(ctx, tx, req.Principal); err != nil {
			return WriteResult{}, err
		}
		if err := checkAgentWrite(ctx, tx, req.Principal, target, req.ContainerID); err != nil {
			return WriteResult{}, err
		}

		// Last: the new Item keeps the Type's invariants.
		if err := checkInvariants(ty, fields); err != nil {
			return WriteResult{}, err
		}

		// The Item and its first event carry one time.
		at, err := stampWrite(ctx, tx)
		if err != nil {
			return WriteResult{}, err
		}
		// A row inserted in this transaction is locked until commit, so no
		// separate SELECT ... FOR UPDATE is needed for create.
		const itemSeq = 1
		_, err = tx.Exec(ctx,
			`insert into items (id, namespace_id, type_id, type_version, container_id, scope_id, state, fields, item_seq,
			                    created_at, created_by, created_on_behalf_of)
			 values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $12, $10, nullif($11, ''))`,
			id, ns, ty.ID, ty.Version, req.ContainerID, scopeID, ty.InitialState, fields, itemSeq,
			req.Principal.Actor, req.Principal.OnBehalfOf, at)
		if err != nil {
			return WriteResult{}, fmt.Errorf("insert item: %w", err)
		}
		// A narrowed Item gets a node of its own. The node names the Item, so
		// the Item row has to exist first.
		if req.Narrow != nil {
			node, err := insertNode(ctx, tx, scopeID, id, true, readers)
			if err != nil {
				return WriteResult{}, err
			}
			if _, err := tx.Exec(ctx, `update items set scope_id = $2 where id = $1`, id, node); err != nil {
				return WriteResult{}, fmt.Errorf("move item into its node: %w", err)
			}
		}

		// The payload records which fields were set, never their values:
		// values may hold personal data, and events are permanent.
		payload := map[string]any{
			"type_id":      ty.ID,
			"type_version": ty.Version,
			"state":        ty.InitialState,
			"field_ids":    sortedKeys(fields),
		}
		if req.ContainerID != nil {
			payload["container_id"] = req.ContainerID
		}
		if req.Narrow != nil {
			payload["readers"] = readers // opaque IDs, sorted
		}
		return appendEvent(ctx, tx, ty, id, itemSeq, req.Principal, attr, "create", payload, fields)
	})
}

// requestHash fingerprints a request. encoding/json writes map keys in sorted
// order, so equal requests always produce the same bytes.
func requestHash(op string, req any) (string, error) {
	b, err := json.Marshal(struct {
		Op      string `json:"op"`
		Request any    `json:"request"`
	}{op, req})
	if err != nil {
		return "", fmt.Errorf("%w: request cannot be encoded: %v", ErrInvalid, err)
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Get returns the Item as the principal may see it. An Item they may not see
// is ErrNotFound, exactly like one that does not exist.
func (k *Service) Get(ctx context.Context, p item.Principal, id uuid.UUID) (item.Item, error) {
	it, policyScope, err := k.visibleItem(ctx, p, id)
	if err != nil {
		return item.Item{}, err
	}
	// The values the Type computes on read (rollups) are part of the Item
	// that goes through Project. They count only what the principal sees.
	ty, err := k.loadType(ctx, k.pool, it.TypeID, it.TypeVersion)
	if err != nil {
		return item.Item{}, err
	}
	rollups, counted, err := k.rollups(ctx, p, ty, id)
	if err != nil {
		return item.Item{}, err
	}
	it.Rollups = rollups
	// The scope was found readable; Project is still the one door out.
	it, ok := policy.Project(p, []uuid.UUID{*it.ScopeID}, it)
	if !ok {
		return item.Item{}, ErrNotFound
	}
	// An agent's read is recorded for the write rule (narrowing scopes
	// only): the Item's own scope, and the scopes of the Items a rollup
	// counted.
	if err := recordReads(ctx, k.pool, p, append([]*uuid.UUID{policyScope}, counted...)); err != nil {
		return item.Item{}, err
	}
	// Links both ways, each end passed through visibility. They name other
	// Items by ID only, so they record no read.
	if it.Links, it.LinksTruncated, err = k.itemLinks(ctx, p, id); err != nil {
		return item.Item{}, err
	}
	return it, nil
}

// visibleItem reads an Item if the principal may see it: in the principal's
// namespace, visible to them, and inside an agent session's scopes; else
// ErrNotFound. It records no read, so it can also tell whether a name may be
// shown (history) without counting as reading that Item. It returns the
// Item, not yet projected, and its policy scope (nil when open).
func (k *Service) visibleItem(ctx context.Context, p item.Principal, id uuid.UUID) (item.Item, *uuid.UUID, error) {
	if err := checkPublicPrincipal(p); err != nil {
		return item.Item{}, nil, err
	}
	c, err := loadCaps(ctx, k.pool, p)
	if err != nil {
		return item.Item{}, nil, err
	}
	// One statement reads the Item and decides visibility from its scope,
	// so the two always agree. Nothing is cached: a change of readers takes
	// effect on the very next read.
	args := policy.VisibleArgs(p)
	args["id"], args["ns"] = id, nsOf(p)
	it := item.Item{ID: id}
	var (
		visible     bool
		policyScope *uuid.UUID
		hold        struct { // the latest hold; all null when there is none
			id                             *uuid.UUID
			hook, from, to, status, reason *string
		}
	)
	err = k.pool.QueryRow(ctx,
		`select i.type_id, i.type_version, i.container_id, i.scope_id, i.state, i.fields, i.item_seq,
		        i.created_at, i.created_by, coalesce(i.created_on_behalf_of, ''),
		        s.policy_scope_id, `+policy.VisibleCondition+`,
		        h.id, h.hook, h.from_state, h.to_state, h.status, coalesce(h.reason, '')
		 from items i join scopes s on s.id = i.scope_id
		 left join lateral (
		     select id, hook, from_state, to_state, status, reason from item_holds
		     where item_id = i.id order by requested_seq desc limit 1
		 ) h on true
		 where i.id = @id and i.namespace_id = @ns`, args,
	).Scan(&it.TypeID, &it.TypeVersion, &it.ContainerID, &it.ScopeID, &it.State, &it.Fields, &it.ItemSeq,
		&it.CreatedAt, &it.CreatedBy, &it.CreatedOnBehalfOf,
		&policyScope, &visible, &hold.id, &hold.hook, &hold.from, &hold.to, &hold.status, &hold.reason)
	if errors.Is(err, pgx.ErrNoRows) {
		return item.Item{}, nil, ErrNotFound
	}
	if err != nil {
		return item.Item{}, nil, fmt.Errorf("read item: %w", err)
	}
	if !visible {
		return item.Item{}, nil, ErrNotFound
	}
	// Outside an agent session's scopes the Item does not exist for it.
	if c != nil {
		in, err := inScopes(ctx, k.pool, c.scopes, &id)
		if err != nil {
			return item.Item{}, nil, err
		}
		if !in {
			return item.Item{}, nil, ErrNotFound
		}
	}
	if hold.id != nil {
		it.Hold = &item.Hold{ID: *hold.id, Hook: *hold.hook, From: *hold.from, To: *hold.to,
			Status: *hold.status, Reason: *hold.reason}
	}
	return it, policyScope, nil
}

// StateEntry says which state an Item is in and which event put it there.
// It is what a workflow needs to pick up an Item mid-history. It holds state
// names and opaque IDs only: no field values, so there is nothing for
// policy.Project to redact.
type StateEntry struct {
	TypeID      string
	TypeVersion int
	State       string
	ItemSeq     int64 // the Item's current item_seq

	EnteredSeq    int64     // the event that entered State
	EnteredAt     time.Time // when that event was written
	CorrelationID string    // of that event; empty if not set
	Hop           int       // of that event

	// HooksRun are the hooks that already wrote for this entry: events whose
	// cause is the entering event. OpenHold is the Item's open hold, if any.
	HooksRun []string
	OpenHold *HoldEntry
	// PendingActions are the action hooks not yet completed, for any entry
	// of their state, not just the current one: an action is never dropped
	// because the Item moved on (or the worker died).
	PendingActions []PendingAction
	// PendingUndos are the undos not yet done, for any hold of the Item
	// (Step 2, part 2): a workflow run that starts after a crash resumes them.
	PendingUndos []PendingUndo
	// PendingReactions are the move and create reactions not yet finished
	// (Jira part 2, step 2): every trigger of one (an entry into its state,
	// an Item created inside) with no outcome recorded on this Item yet.
	PendingReactions []PendingReaction
	// EnteredHold is the hold whose allowed resolution entered State, if
	// the entry was a held transition: an undo on leaving State reverses
	// that hold's reservation, never an earlier one's.
	EnteredHold uuid.UUID
	// ActingRuns are, per acting timer of State, how many of its runs for
	// this entry have completed (hook_ran or hook_failed) (Step 2, part 3):
	// a run after a crash goes on from there.
	ActingRuns map[string]int
}

// HoldEntry is an open hold, as the workflow needs it: opaque IDs, state
// names and the request's attribution.
type HoldEntry struct {
	ID            uuid.UUID
	Hook          string
	From, To      string
	RequestedSeq  int64
	CorrelationID string
	Hop           int
}

// StateEntry reads an Item's state and the event that entered it, in one
// statement, so the two always agree.
func (k *Service) StateEntry(ctx context.Context, id uuid.UUID) (StateEntry, error) {
	var se StateEntry
	// Only create and transition events enter a state.
	err := k.pool.QueryRow(ctx,
		`select i.type_id, i.type_version, i.state, i.item_seq,
		        e.item_seq, e.created_at, coalesce(e.correlation_id, ''), e.hop
		 from items i
		 join lateral (
		     select item_seq, created_at, correlation_id, hop
		     from item_events
		     where item_id = i.id and op in ('create', 'transition')
		     order by item_seq desc
		     limit 1
		 ) e on true
		 where i.id = $1`, id, // the workflow's read: by ID, in any namespace
	).Scan(&se.TypeID, &se.TypeVersion, &se.State, &se.ItemSeq,
		&se.EnteredSeq, &se.EnteredAt, &se.CorrelationID, &se.Hop)
	if errors.Is(err, pgx.ErrNoRows) {
		return StateEntry{}, ErrNotFound
	}
	if err != nil {
		return StateEntry{}, fmt.Errorf("read state entry: %w", err)
	}

	// A hook's write names the hook as actor, hook:<type>:<hook-id>.
	rows, err := k.pool.Query(ctx,
		`select distinct split_part(actor, ':', 3) from item_events
		 where item_id = $1 and causation_id = $2 and actor like 'hook:%'
		 order by 1`, id, fmt.Sprintf("%s:%d", id, se.EnteredSeq))
	if err != nil {
		return StateEntry{}, fmt.Errorf("read hooks run: %w", err)
	}
	// CollectRows reads every row with the given function, then closes them.
	se.HooksRun, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return StateEntry{}, fmt.Errorf("read hooks run: %w", err)
	}

	h := HoldEntry{}
	err = k.pool.QueryRow(ctx,
		`select h.id, h.hook, h.from_state, h.to_state, h.requested_seq, coalesce(e.correlation_id, ''), e.hop
		 from item_holds h join item_events e on e.item_id = h.item_id and e.item_seq = h.requested_seq
		 where h.item_id = $1 and h.status = 'open'`, id,
	).Scan(&h.ID, &h.Hook, &h.From, &h.To, &h.RequestedSeq, &h.CorrelationID, &h.Hop)
	switch {
	case err == nil:
		se.OpenHold = &h
	case !errors.Is(err, pgx.ErrNoRows):
		return StateEntry{}, fmt.Errorf("read open hold: %w", err)
	}

	ty, err := k.loadType(ctx, k.pool, se.TypeID, se.TypeVersion)
	if err != nil {
		return StateEntry{}, err
	}
	if se.PendingActions, err = k.pendingActions(ctx, id, ty); err != nil {
		return StateEntry{}, err
	}
	// The hold whose allowed resolution entered the state, if any: an
	// escape names its hold as canceled_hold_id, never hold_id.
	var entered *string
	if err := k.pool.QueryRow(ctx,
		`select payload ->> 'hold_id' from item_events where item_id = $1 and item_seq = $2 and op = 'transition'`,
		id, se.EnteredSeq).Scan(&entered); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return StateEntry{}, fmt.Errorf("read the entering hold: %w", err)
	}
	if entered != nil {
		if se.EnteredHold, err = uuid.Parse(*entered); err != nil {
			return StateEntry{}, fmt.Errorf("read the entering hold: %w", err)
		}
	}
	if se.PendingUndos, err = k.pendingUndos(ctx, id, ty); err != nil {
		return StateEntry{}, err
	}
	if se.ActingRuns, err = k.actingRuns(ctx, id, se.EnteredSeq); err != nil {
		return StateEntry{}, err
	}
	return se, nil
}

// actingRuns counts, per acting timer, the runs completed for one entry:
// hook_ran or hook_failed naming the timer, caused by the entering event.
func (k *Service) actingRuns(ctx context.Context, id uuid.UUID, entered int64) (map[string]int, error) {
	rows, err := k.pool.Query(ctx,
		`select payload ->> 'timer', count(distinct payload ->> 'run') from item_events
		 where item_id = $1 and causation_id = $2 and op in ($3, $4) and payload ? 'timer'
		 group by 1`, id, fmt.Sprintf("%s:%d", id, entered), lifecycle.OpHookRan, lifecycle.OpHookFailed)
	if err != nil {
		return nil, fmt.Errorf("read acting runs: %w", err)
	}
	type count struct {
		timer string
		n     int
	}
	counts, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (count, error) {
		var c count
		return c, r.Scan(&c.timer, &c.n)
	})
	if err != nil {
		return nil, fmt.Errorf("read acting runs: %w", err)
	}
	out := map[string]int{}
	for _, c := range counts {
		out[c.timer] = c.n
	}
	return out, nil
}

// Type returns a registered Type version.
func (k *Service) Type(ctx context.Context, id string, version int) (*typedef.Type, error) {
	return k.loadType(ctx, k.pool, id, version)
}
