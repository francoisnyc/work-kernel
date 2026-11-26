package policy

import (
	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// MayMigrate answers "may this principal migrate an Item to this version
// of its Type?" (track 1, item 6, step 2). m is the migrate block of the
// latest version, the one the Item would move to: nil means nobody. Only a
// person may, acting for themselves: never an agent, whoever it acts for
// and whatever roles it holds, and never a customer. The person is one a
// principal:user: entry names, or holds a role a role: entry names
// (who.ActorRoles).
func MayMigrate(m *typedef.Migrate, who Who) bool {
	if m == nil || !item.IsKind(who.Actor, item.KindUser) {
		return false
	}
	if who.OnBehalfOf != "" && who.OnBehalfOf != who.Actor {
		return false
	}
	return matchesEntry(m.Allowed, who.Actor, who.ActorRoles, nil)
}
