package itemservice

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// errRetry is returned from inside a write when it lost a race it cannot
// finish without risking a deadlock, or when the links a guard read changed
// before they were locked; write runs the whole transaction again, as it
// does after a deadlock or serialization failure (retryable).
var errRetry = errors.New("itemservice: retry the transaction")

// maxRetries bounds how often write reruns a transaction (see retryable).
const maxRetries = 3

// write runs one write operation in one transaction, wrapped in the
// idempotency protocol shared by every operation:
//
//  1. claim the key; if it was already used, return the stored result;
//  2. run apply, which changes the Item and appends its event;
//  3. store the result under the key.
//
// If apply returns an error the transaction rolls back, which also releases
// the key, so a rejected request can be retried with the same key.
//
// apply is a function value, like passing a lambda to a template method.
func (k *Service) write(ctx context.Context, p item.Principal, key, hash string,
	apply func(tx pgx.Tx) (WriteResult, error)) (WriteResult, error) {

	var res WriteResult
	// BeginFunc commits if the function returns nil and rolls back otherwise.
	run := func(tx pgx.Tx) error {
		// set_config(..., true) is SET LOCAL: the timeout ends with this
		// transaction, so the pooled connection goes back unchanged.
		_, err := tx.Exec(ctx, `select set_config('statement_timeout', $1, true)`,
			fmt.Sprintf("%dms", k.writeTimeout.Milliseconds()))
		if err != nil {
			return fmt.Errorf("set statement timeout: %w", err)
		}
		// An agent's session must be one minted for this agent and user.
		if err := checkSession(ctx, tx, p); err != nil {
			return err
		}
		// Claim the key first. If another transaction holds the same key, this
		// statement waits for it to finish, then sees its row and inserts nothing.
		// Keys are scoped by namespace and actor, so two clients never collide.
		tag, err := tx.Exec(ctx,
			`insert into idempotency (namespace_id, actor, key, request_hash, result)
			 values ($1, $2, $3, $4, '{}')
			 on conflict (namespace_id, actor, key) do nothing`,
			nsOf(p), p.Actor, key, hash)
		if err != nil {
			return fmt.Errorf("claim idempotency key: %w", err)
		}
		if tag.RowsAffected() == 0 {
			res, err = storedResult(ctx, tx, nsOf(p), p.Actor, key, hash)
			return err
		}

		if err := callTestHook(ctx, tx, "write"); err != nil {
			return err
		}
		// apply gets the transaction wrapped, so that the write's time can
		// travel with it (stampWrite, appendEvent).
		res, err = apply(&stampedTx{Tx: tx, skew: k.clockSkew})
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx,
			`update idempotency set result = $4 where namespace_id = $1 and actor = $2 and key = $3`,
			nsOf(p), p.Actor, key, res)
		if err != nil {
			return fmt.Errorf("store idempotency result: %w", err)
		}
		return nil
	}
	// A transaction that lost a race it cannot finish safely (errRetry),
	// or that Postgres aborted for a deadlock or a serialization failure,
	// is rolled back and run again from the start: everything it read is
	// read afresh, and its idempotency key was released with the rollback.
	var err error
	for attempt := 0; ; attempt++ {
		err = pgx.BeginFunc(ctx, k.pool, run)
		if !retryable(err) || attempt == maxRetries {
			break
		}
	}
	if err != nil {
		return WriteResult{}, err
	}
	return res, nil
}

// storedResult returns the result recorded for an actor's idempotency key, or
// ErrIdempotencyKeyReuse if the key was used with a different request.
func storedResult(ctx context.Context, tx pgx.Tx, ns, actor, key, hash string) (WriteResult, error) {
	var (
		storedHash string
		res        WriteResult
	)
	err := tx.QueryRow(ctx,
		`select request_hash, result from idempotency
		 where namespace_id = $1 and actor = $2 and key = $3`,
		ns, actor, key,
	).Scan(&storedHash, &res)
	if err != nil {
		return WriteResult{}, fmt.Errorf("read idempotency record: %w", err)
	}
	if storedHash != hash {
		return WriteResult{}, ErrIdempotencyKeyReuse
	}
	return res, nil
}

// lockedItem is the part of an Item row a write needs.
type lockedItem struct {
	TypeID      string
	TypeVersion int
	ContainerID *uuid.UUID
	ScopeID     uuid.UUID
	State       string
	Fields      map[string]any
	ItemSeq     int64
	// Who created it: set at create, never changed. `allowed` and `except`
	// read them for creator.
	CreatedBy, CreatedOnBehalfOf string
}

// lockItem reads the Item row and locks it until the transaction ends. Two
// writes to one Item therefore run one after the other, which is what keeps
// item_seq free of gaps. An Item of another namespace is ErrNotFound.
func lockItem(ctx context.Context, tx pgx.Tx, ns string, id uuid.UUID) (lockedItem, error) {
	var it lockedItem
	err := tx.QueryRow(ctx,
		`select type_id, type_version, container_id, scope_id, state, fields, item_seq,
		        created_by, coalesce(created_on_behalf_of, '')
		 from items where id = $1 and namespace_id = $2 for update`,
		id, ns,
	).Scan(&it.TypeID, &it.TypeVersion, &it.ContainerID, &it.ScopeID, &it.State, &it.Fields, &it.ItemSeq,
		&it.CreatedBy, &it.CreatedOnBehalfOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedItem{}, ErrNotFound
	}
	if err != nil {
		return lockedItem{}, fmt.Errorf("lock item: %w", err)
	}
	return it, nil
}

// checkPrincipal refuses a principal that is not typed: the actor must be
// user:, agent:, system: or hook:, and on_behalf_of, when set, a user:.
// Which door each kind may use is checkPublic's and checkKernel's job.
func checkPrincipal(p item.Principal) error {
	if !p.Valid() {
		return fmt.Errorf("%w: principals are typed IDs (user:<id>, agent:<id>, system:<id>, hook:<type>:<id>), "+
			"and on_behalf_of must be a user", ErrInvalid)
	}
	return nil
}

// checkPublic is the public door's check: people and agents only. The
// kernel's own actors (system:, hook:) and the attribution only the kernel
// may set (originating_hook, causation_id, hop) are ErrInvalid here; they go
// through Internal(). correlation_id stays open to callers, as a claim.
func checkPublic(p item.Principal, a item.Attribution) error {
	if err := checkPublicPrincipal(p); err != nil {
		return err
	}
	if a.OriginatingHook != "" || a.CausationID != "" || a.Hop != 0 {
		return fmt.Errorf("%w: originating_hook, causation_id and hop are set by the kernel only", ErrInvalid)
	}
	return nil
}

// checkPublicPrincipal refuses an untyped principal, and the kernel's own
// actors, on every public operation, reads included.
func checkPublicPrincipal(p item.Principal) error {
	if err := checkPrincipal(p); err != nil {
		return err
	}
	if item.IsKernelActor(p.Actor) {
		return fmt.Errorf("%w: %s is a kernel actor; it does not use the public door", ErrInvalid, p.Actor)
	}
	return nil
}

// checkAttribution refuses malformed attribution before any work is done.
func checkAttribution(a item.Attribution) error {
	if a.Hop < 0 {
		return fmt.Errorf("%w: hop cannot be negative", ErrInvalid)
	}
	return nil
}

// checkOptions validates the optional parts shared by Update and Transition.
func checkOptions(expectedItemSeq int64, a item.Attribution) error {
	if expectedItemSeq < 0 {
		return fmt.Errorf("%w: expected item_seq cannot be negative", ErrInvalid)
	}
	return checkAttribution(a)
}

// checkExpectedSeq is the if-match. It runs under the row lock, so the
// item_seq it compares cannot change before the write. Zero means "no check".
func checkExpectedSeq(it lockedItem, expected int64) error {
	if expected != 0 && expected != it.ItemSeq {
		return fmt.Errorf("%w: expected item_seq %d, but the Item is at %d", ErrStale, expected, it.ItemSeq)
	}
	return nil
}

// appendEvent records one accepted operation: it inserts the event (which
// takes its commit_seq), the values the event set, and, when ty has
// listeners, the outbox row. The caller has already written the Item row at
// itemSeq.
//
// payload holds the operation's shape (states, field IDs) and never a field
// value; values go to item_event_values so they can be erased later.
func appendEvent(ctx context.Context, tx pgx.Tx, ty *typedef.Type, id uuid.UUID, itemSeq int64, p item.Principal,
	a item.Attribution, op string, payload map[string]any, values map[string]any) (WriteResult, error) {

	// nullif stores an empty Go string as null: "not set".
	// commit_seq comes from the commit_seq sequence and xid from
	// pg_current_xact_id(), both column defaults. Taking a sequence number
	// locks nothing, so writes to different Items no longer queue behind each
	// other. The numbers are not in commit order; min_seq waits on xid instead
	// (see Search).
	//
	// created_at is the write's time: the one stampWrite read for this
	// transaction, or else the clock at this insert. Either way it is read
	// while the Item's row lock is held, so along item_seq an Item's event
	// times never go backwards. (now(), the transaction's start, would: a
	// write that waited for the lock started before the one it waited for.)
	at, skew := writeTimeOf(tx)
	var commitSeq int64
	err := tx.QueryRow(ctx,
		`insert into item_events (item_id, item_seq, actor, on_behalf_of,
		                          agent_session, originating_hook, causation_id, correlation_id, hop,
		                          op, payload, created_at)
		 values ($1, $2, $3, nullif($4, ''),
		         nullif($5, ''), nullif($6, ''), nullif($7, ''), nullif($8, ''), $9,
		         $10, $11, coalesce($12::timestamptz, clock_timestamp() + $13::interval))
		 returning commit_seq`,
		id, itemSeq, p.Actor, p.OnBehalfOf,
		a.AgentSession, a.OriginatingHook, a.CausationID, a.CorrelationID, a.Hop,
		op, payload, at, skew).Scan(&commitSeq)
	if err != nil {
		return WriteResult{}, fmt.Errorf("insert event: %w", err)
	}

	// An event that sets no field gets no values row.
	if len(values) > 0 {
		_, err = tx.Exec(ctx,
			`insert into item_event_values (item_id, item_seq, field_values) values ($1, $2, $3)`,
			id, itemSeq, values)
		if err != nil {
			return WriteResult{}, fmt.Errorf("insert event values: %w", err)
		}
	}

	// Pay for what you use: with nothing listening, there is nothing to send.
	if lifecycle.HasListeners(ty) {
		_, err = tx.Exec(ctx, `insert into outbox (item_id, item_seq) values ($1, $2)`, id, itemSeq)
		if err != nil {
			return WriteResult{}, fmt.Errorf("insert outbox: %w", err)
		}
	}
	return WriteResult{ItemID: id, CommitSeq: commitSeq, ItemSeq: itemSeq}, nil
}

// applyPatch applies a field patch to an Item's current fields without
// modifying either map. It returns the fields after the patch, the values the
// patch set, and the fields it cleared (a nil value in the patch clears).
// Go functions can return several values; there is no need for a result class.
func applyPatch(current, patch map[string]any) (merged, set, cleared map[string]any) {
	merged = maps.Clone(current)
	if merged == nil {
		merged = map[string]any{}
	}
	set = map[string]any{}
	cleared = map[string]any{}
	for id, v := range patch {
		if v == nil {
			delete(merged, id)
			cleared[id] = nil
		} else {
			merged[id] = v
			set[id] = v
		}
	}
	return merged, set, cleared
}

// sortedKeys returns the map's keys in order. Go maps iterate in random
// order, so anything stored or compared must be sorted first. The result is
// never nil, so it encodes as [] rather than null.
func sortedKeys(m map[string]any) []string {
	keys := slices.Sorted(maps.Keys(m))
	if keys == nil {
		keys = []string{}
	}
	return keys
}

// stampedTx is a write's transaction plus the time the write is stamped
// with. It embeds pgx.Tx: every method of the transaction is promoted, so
// a *stampedTx is a pgx.Tx wherever one is asked for (Go's composition in
// place of a subclass).
type stampedTx struct {
	pgx.Tx
	skew time.Duration // Options.ClockSkew; zero outside tests
	at   *time.Time    // set by stampWrite
}

// stampWrite reads the write's time, once per transaction, and returns it:
// the database's clock now (clock_timestamp(), not the transaction's start),
// to the microsecond. Call it AFTER the write has taken its row locks. The
// lock runs an Item's writes one after the other, so a time read under it
// is later than the time of every earlier event of the Item. The write's
// event carries this time (appendEvent), and so does anything computed from
// it (cause.at).
func stampWrite(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	st, ok := tx.(*stampedTx)
	if ok && st.at != nil {
		return *st.at, nil
	}
	var skew time.Duration
	if ok {
		skew = st.skew
	}
	var at time.Time
	if err := tx.QueryRow(ctx, `select clock_timestamp() + $1::interval`, skew).Scan(&at); err != nil {
		return time.Time{}, fmt.Errorf("read the write's time: %w", err)
	}
	at = at.UTC()
	if ok {
		st.at = &at
	}
	return at, nil
}

// writeTimeOf returns the time stampWrite read for this transaction (nil if
// it was not called) and the clock's skew.
func writeTimeOf(tx pgx.Tx) (*time.Time, time.Duration) {
	if st, ok := tx.(*stampedTx); ok {
		return st.at, st.skew
	}
	return nil, 0
}
