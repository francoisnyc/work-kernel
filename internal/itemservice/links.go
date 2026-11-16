package itemservice

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// Link refusal codes (docs/design.md, "Links, containment and people").
const (
	CodeLinkCardinality = "link_cardinality" // a max-one side already has its link; metadata link_type, side
	CodeAlreadyLinked   = "already_linked"   // the same link exists
	CodeNotLinked       = "not_linked"       // no such link to remove
	CodeLinkTargetType  = "link_target_type" // the target's Type is not one the link type allows
)

// MaxLinksShown is how many links Get shows per direction (restricted ones
// count); Item.LinksTruncated says there are more.
const MaxLinksShown = 200

// LinkRequest adds (Link) or removes (Unlink) one link from a source Item
// to a target. LinkType is the declared name, never the inverse.
type LinkRequest struct {
	IdempotencyKey string
	Principal      item.Principal
	SourceID       uuid.UUID
	LinkType       string
	TargetID       uuid.UUID
	// ExpectedItemSeq is the optional if-match, on the source.
	ExpectedItemSeq int64
	Attribution     item.Attribution // optional; what caused this write
}

// LinkFilter keeps Items with a link of this name to or from ItemID, as the
// result Items see it: outgoing takes a declared name, incoming an inverse
// name. The name matches every Type that declares it in that direction.
type LinkFilter struct {
	LinkType  string
	Direction item.LinkDirection
	ItemID    uuid.UUID
}

// errNotBuiltYet marks the Step 1 operations before Phase C.
var errNotBuiltYet = errors.New("itemservice: links are not built yet")

// Link adds a link and records link_added on the source.
func (k *Service) Link(ctx context.Context, req LinkRequest) (WriteResult, error) {
	return WriteResult{}, errNotBuiltYet // Step 1, Phase B stub
}

// Unlink removes a link and records link_removed on the source.
func (k *Service) Unlink(ctx context.Context, req LinkRequest) (WriteResult, error) {
	return WriteResult{}, errNotBuiltYet // Step 1, Phase B stub
}

// testHook is nil in production. Tests set it (export_test.go) to pause or
// act at named points of a write, inside its transaction:
//   - "write": after the idempotency key is claimed, on every attempt;
//   - "links_read": after a link-reading transition read its links unlocked;
//   - "link_guard_held": after its guard held, with the linked rows locked.
//
// An error it returns fails the attempt as if the point had failed.
var testHook func(ctx context.Context, tx pgx.Tx, point string) error
