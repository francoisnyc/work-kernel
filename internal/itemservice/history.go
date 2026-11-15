package itemservice

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
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
}

// Event is one accepted operation as a principal may see it: the event's
// shape, its values from item_event_values, and every other Item it names
// passed through visibility.
type Event struct {
	ItemID    uuid.UUID
	ItemSeq   int64
	CommitSeq int64
	// Op: create, update, transition, transition_requested,
	// transition_denied, transition_failed or hook_failed.
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
}

// ItemRef is another Item named by an event: its ID, or Restricted (and no
// ID) when the principal cannot see it.
type ItemRef struct {
	ID         uuid.UUID
	Restricted bool
}

// ListEvents returns one page of an Item's history, projected for the
// principal. An Item the principal cannot see is ErrNotFound.
func (k *Service) ListEvents(ctx context.Context, req ListEventsRequest) (ListEventsResult, error) {
	return ListEventsResult{}, errNotBuilt
}
