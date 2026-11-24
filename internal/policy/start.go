package policy

import (
	"slices"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// ContainerFacts is what start.allowed reads of the container an Item is
// created in: its fields (for field:<id>) and who created it (for
// container_creator). The zero value is the top level. At create time the
// new Item does not exist and its fields are the caller's, so every entry
// reads the container, never the new Item (starting-work.md, 1.2).
type ContainerFacts struct {
	Fields                       map[string]any
	CreatedBy, CreatedOnBehalfOf string
}

// MayStart answers "may this principal create an Item of a Type whose
// latest version has this start block?" from start.allowed. start.in is
// not its question (typedef.Start.Admits), and seeing the container is the
// caller's check, made before.
//
//   - No block, or an empty allowed: anyone.
//   - nobody: no person, agent or customer; only a create reaction, by
//     the target's consent, which never asks MayStart.
//   - A customer, or an agent acting for one, is judged by the container's
//     own and shared, not by allowed: every other entry lets them in.
//   - container_creator: anyone who took part in creating the container
//     (TookPartInCreation), judged once for the principal as a whole.
//   - role:, principal: and field: as in MayTransition (matchesEntry), for
//     the actor and for the user it acts for: both must pass.
func MayStart(start *typedef.Start, who Who, c ContainerFacts) bool {
	if start == nil {
		return true
	}
	if start.Nobody() {
		return false
	}
	if CustomerOf(item.Principal{Actor: who.Actor, OnBehalfOf: who.OnBehalfOf}) != "" || len(start.Allowed) == 0 {
		return true
	}
	if slices.Contains(start.Allowed, typedef.AllowedContainerCreator) &&
		TookPartInCreation(who.Actor, who.OnBehalfOf, c.CreatedBy, c.CreatedOnBehalfOf) {
		return true
	}
	if !matchesEntry(start.Allowed, who.Actor, who.ActorRoles, c.Fields) {
		return false
	}
	return who.OnBehalfOf == "" || matchesEntry(start.Allowed, who.OnBehalfOf, who.OnBehalfOfRoles, c.Fields)
}
