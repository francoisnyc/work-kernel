// Package item holds the plain data types shared by the kernel and policy
// packages. It has no behavior and imports neither of them, which keeps the
// import graph free of cycles (Go rejects circular imports at compile time).
package item

import "github.com/google/uuid"

// Principal says who performs an operation. Both values are opaque IDs.
type Principal struct {
	Actor      string // the user or agent making the call
	OnBehalfOf string // empty when the actor acts for itself
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
