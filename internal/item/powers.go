package item

import (
	"regexp"
	"strings"
)

// Powers an agent session may hold (capability mode, docs/design.md "Who is
// calling"). read is always implied. v1 enforces read versus any write
// power; the finer ones are stored for later.
const (
	PowerRead          = "read"
	PowerCreate        = "create"
	PowerEditFields    = "edit_fields"
	PowerMove          = "move"
	PowerComment       = "comment" // reserved: stored only; will mean "create comment Items"
	PowerOutbound      = "outbound"
	PowerTransitionAll = "transition:*"
	// PowerTransitionPrefix starts transition:<state>, "may move Items into
	// that state". Transitions have no IDs, so the power names the target.
	PowerTransitionPrefix = "transition:"
)

// stateName is the shape of a state ID: what transition:<state> may name.
// The state itself is not checked: a session spans Types.
var stateName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// KnownPower reports whether p is a power of the vocabulary above.
func KnownPower(p string) bool {
	switch p {
	case PowerRead, PowerCreate, PowerEditFields, PowerMove, PowerComment, PowerOutbound, PowerTransitionAll:
		return true
	}
	state, ok := strings.CutPrefix(p, PowerTransitionPrefix)
	return ok && stateName.MatchString(state)
}

// PowerCovers reports whether a granted power covers an asked one:
// transition:* covers every transition:<state>; otherwise only itself.
func PowerCovers(granted, asked string) bool {
	return granted == asked ||
		(granted == PowerTransitionAll && strings.HasPrefix(asked, PowerTransitionPrefix))
}

// Writes reports whether a set of powers allows any write: v1 tells read
// from write and nothing finer.
func Writes(powers []string) bool {
	for _, p := range powers {
		if p != PowerRead {
			return true
		}
	}
	return false
}
