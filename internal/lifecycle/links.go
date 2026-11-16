package lifecycle

import "github.com/francoisnyc/work-kernel/internal/typedef"

// The ops of a link change. The event is on the source Item only.
const (
	OpLinkAdded   = "link_added"
	OpLinkRemoved = "link_removed"
)

// LinkedItem is what a guard sees of an Item at the other end of a link:
// its state and that state's category, never its fields.
type LinkedItem struct {
	State    string
	Category string
}

// Links are the linked Items a guard may read, keyed by the name the guard
// uses: a declared name for outgoing links (blocks), an inverse name for
// incoming ones (blocked_by). A name with no links may be absent. A nil
// Links means "not read": a guard that reads links then does not hold, so a
// caller that forgets them fails safe.
type Links map[string][]LinkedItem

// TransitionLinked is Transition with the linked Items its guard may read.
// It is the one pure lifecycle function; Transition is it with no links.
func TransitionLinked(t *typedef.Type, current string, fields map[string]any, links Links,
	to string) (Result, error) {
	return Transition(t, current, fields, to) // Step 1, Phase B stub: links are ignored
}
