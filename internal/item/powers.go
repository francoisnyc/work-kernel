package item

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
	powerTransition    = "transition:" // transition:<id>
)

// KnownPower reports whether p is a power of the vocabulary above.
func KnownPower(p string) bool {
	return false // not built yet: Step 0, part 2
}
