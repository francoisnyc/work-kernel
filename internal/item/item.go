// Package item holds the plain data types shared by the item service and policy
// packages. It has no behavior and imports neither of them, which keeps the
// import graph free of cycles (Go rejects circular imports at compile time).
package item

import "github.com/google/uuid"

// Principal says who performs an operation. Both values are opaque IDs.
type Principal struct {
	Actor      string // the user or agent making the call
	OnBehalfOf string // empty when the actor acts for itself
	// AgentSession is set when an agent acts, on reads and writes alike. It
	// is a session the item service minted (StartAgentSession), never one
	// the caller made up.
	AgentSession string
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
}
