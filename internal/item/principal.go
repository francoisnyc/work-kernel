package item

import "strings"

// Kinds of principal. A principal ID is "<kind>:<id>", given by the caller,
// so groups (and other kinds) fit later without a migration.
const (
	KindUser   = "user"
	KindAgent  = "agent"
	KindGroup  = "group"  // reserved: no groups yet
	KindSystem = "system" // the kernel's own actors, such as system:timer
)

// KindOf returns the kind of a typed principal ID. ok is false for an
// untyped ID, an unknown kind, or an empty ID after the colon.
func KindOf(id string) (kind string, ok bool) {
	// Cut splits at the first colon: "system:timer" is kind "system", ID "timer".
	kind, rest, found := strings.Cut(id, ":")
	if !found || rest == "" {
		return "", false
	}
	switch kind {
	case KindUser, KindAgent, KindGroup, KindSystem:
		return kind, true
	}
	return "", false
}

// IsKind reports whether id is a typed principal ID of one of the kinds.
func IsKind(id string, kinds ...string) bool {
	k, ok := KindOf(id)
	if !ok {
		return false
	}
	for _, want := range kinds {
		if k == want {
			return true
		}
	}
	return false
}

// Valid reports whether the principal may act: the actor is a user, an
// agent or a system principal; on_behalf_of, when set, is a user (the human
// at the root of a chain); and an agent, and only an agent, names its
// session. Whether that session exists and is the agent's is checked against
// the database by the item service.
func (p Principal) Valid() bool {
	if !IsKind(p.Actor, KindUser, KindAgent, KindSystem) {
		return false
	}
	if p.OnBehalfOf != "" && !IsKind(p.OnBehalfOf, KindUser) {
		return false
	}
	return (p.AgentSession != "") == IsKind(p.Actor, KindAgent)
}
