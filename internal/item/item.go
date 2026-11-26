// Package item holds the plain data types shared by the item service and policy
// packages. It has no behavior and imports neither of them, which keeps the
// import graph free of cycles (Go rejects circular imports at compile time).
package item

import (
	"time"

	"github.com/google/uuid"
)

// Principal says who performs an operation. Both values are opaque IDs.
type Principal struct {
	Actor      string // the user or agent making the call
	OnBehalfOf string // empty when the actor acts for itself
	// AgentSession is set when an agent acts, on reads and writes alike. It
	// is a session the item service minted (StartAgentSession), never one
	// the caller made up.
	AgentSession string
	// Namespace is the namespace the principal acts in: the same principal
	// ID in two namespaces is two principals. Empty means "default" (the
	// spike's earlier tests rely on it; docs/journal.md, Later: make the
	// kernel require it). The json:"-" tag keeps it out of the idempotency
	// request hash: the namespace already scopes the key.
	Namespace string `json:"-"`
}

// Attribution says what caused a write, beyond who made it. Every value is an
// opaque ID; empty means "not set" and is stored as null. See "Agents" in
// docs/design.md.
type Attribution struct {
	AgentSession    string // set when an agent acts
	OriginatingHook string // the hook, connector or timer that caused the write
	CausationID     string // the event that caused this write, as "item:seq"
	CorrelationID   string // groups a whole chain of writes
	Hop             int    // depth in the chain; 0 for a write a person made
}

// Item is the current state of one unit of work.
type Item struct {
	ID          uuid.UUID
	TypeID      string
	TypeVersion int
	// Pointers stand in for "may be absent", like a nullable reference in Java.
	ContainerID *uuid.UUID
	ScopeID     *uuid.UUID
	State       string
	Fields      map[string]any // keyed by stable field ID
	ItemSeq     int64
	// ContentSeq counts the writes that changed something a caller could
	// overwrite (the state, a field a caller may write, a link, the
	// readers, the container). The public etag is derived from it;
	// ItemSeq counts every event. (The etag step.)
	ContentSeq int64
	// Hold is the Item's latest hold (open, or how it ended), or nil if the
	// Item never had one. A hold is visible on the Item.
	Hold *Hold
	// CreatedAt, CreatedBy and CreatedOnBehalfOf come from the Item's first
	// event: when, its actor, and the user that actor acted for (empty when
	// none). Kept on the row so a reader needs no history to show them.
	CreatedAt         time.Time
	CreatedBy         string
	CreatedOnBehalfOf string
	// Links in both directions, outgoing first, at most MaxLinksShown per
	// direction (restricted ones count). Only Get fills them; LinksTruncated
	// says more exist.
	Links          []Link
	LinksTruncated bool
	// Rollups are the values the Item's Type version computes when the
	// Item is read, keyed by rollup ID. Only Get fills them; they count
	// only what the reader can see.
	Rollups map[string]Rollup
	// StateEnteredAt is when the Item entered the state it is in: its
	// create, or its latest transition (items.state_entered_at).
	StateEnteredAt time.Time
	// Moves are the to-states of the transitions out of the Item's state
	// that the reader may make for who they are (policy.MayMove), in the
	// Type's order. Only Search and List with Filter.WaitingOn fill it.
	Moves []string
}

// Rollup is one computed value: how many linked Items the reader can see,
// by state category or by state. A key with no Item is absent.
type Rollup struct {
	Counts map[string]int64
}

// Hold is a request for a checked transition, held while its check runs.
type Hold struct {
	ID     uuid.UUID
	Hook   string // the check hook's ID
	From   string // the state the Item is held in
	To     string // the state requested
	Status string // one of the Hold* constants
	Reason string // a code, set when denied or error
}

// The states of a hold. Only HoldOpen holds the Item.
const (
	HoldOpen     = "open"
	HoldAllowed  = "allowed"  // the requested transition was applied
	HoldDenied   = "denied"   // the check said no, or the requester no longer may
	HoldError    = "error"    // the check could not answer
	HoldCanceled = "canceled" // an escape left the state
)

// LinkDirection says which end of a link an Item is.
type LinkDirection string

const (
	LinkOutgoing LinkDirection = "outgoing" // the Item is the link's source
	LinkIncoming LinkDirection = "incoming" // the Item is the link's target
)

// Link is one link of an Item, as a principal sees it from that Item.
type Link struct {
	// LinkType is the name as seen from this Item: the declared name when
	// outgoing (blocks), the inverse name when incoming (blocked_by).
	LinkType  string
	Direction LinkDirection
	// Other is the Item at the other end; uuid.Nil when Restricted.
	Other      uuid.UUID
	Restricted bool
}
