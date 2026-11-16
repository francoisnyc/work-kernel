package item

import "strings"

// Kinds of principal. A principal ID is "<kind>:<id>", given by the caller,
// so groups (and other kinds) fit later without a migration.
const (
	KindUser   = "user"
	KindAgent  = "agent"
	KindGroup  = "group"  // reserved: no groups yet
	KindSystem = "system" // the kernel's own actors, such as system:timer
	// KindHook is a Type's hook acting: hook:<type>:<hook-id>. It has no
	// version, so a Type's `allowed` list matches it across versions; the
	// version is on the event's originating_hook instead.
	KindHook = "hook"
	// KindReaction is a Type's link reaction acting: reaction:<type>:<id>
	// (proof of concept, Step 2, part 2). Like a hook, it has no version;
	// the version is on originating_hook, reaction:<type>@v<n>:<id>.
	KindReaction = "reaction"
)

// ReactionParts splits a reaction principal, reaction:<type>:<id>, into its
// Type and reaction ID. ok is false for anything else.
//
// Phase B stub: not implemented yet.
func ReactionParts(id string) (typeID, reactionID string, ok bool) {
	return "", "", false
}

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
	case KindHook:
		if _, _, ok := hookParts(rest); ok {
			return kind, true
		}
	}
	return "", false
}

// HookParts splits a hook principal, hook:<type>:<hook-id>, into its Type
// and hook ID. ok is false for anything else.
func HookParts(id string) (typeID, hookID string, ok bool) {
	rest, found := strings.CutPrefix(id, KindHook+":")
	if !found {
		return "", "", false
	}
	return hookParts(rest)
}

// hookParts splits "<type>:<hook-id>". Both parts are non-empty and hold
// no further colon, and no "@": a version belongs on originating_hook.
func hookParts(rest string) (typeID, hookID string, ok bool) {
	typeID, hookID, found := strings.Cut(rest, ":")
	if !found || typeID == "" || hookID == "" || strings.ContainsAny(typeID+hookID, ":@") {
		return "", "", false
	}
	return typeID, hookID, true
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

// Valid reports whether the principal is well formed: the actor is a user,
// an agent, a system principal or a hook; on_behalf_of, when set, is a user (the human
// at the root of a chain); and an agent, and only an agent, names its
// session. Whether that session exists and is the agent's is checked against
// the database by the item service. Which door a kind may use (public or
// internal) is the item service's rule, not this one.
func (p Principal) Valid() bool {
	if !IsKind(p.Actor, KindUser, KindAgent, KindSystem, KindHook) {
		return false
	}
	if p.OnBehalfOf != "" && !IsKind(p.OnBehalfOf, KindUser) {
		return false
	}
	return (p.AgentSession != "") == IsKind(p.Actor, KindAgent)
}
