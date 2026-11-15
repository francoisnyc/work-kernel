package itemservice

// Filter narrows Search and List. Every part that is set must hold; within a
// list, any entry may match. The zero Filter matches everything.
type Filter struct {
	TypeIDs []string // Items of any version of these Types
	States  []string
	// Categories are read in each Item's own Type version, so a category
	// becomes a set of (type, version, state).
	Categories []string
	// Fields needs exactly one Type in TypeIDs, and the field must have one
	// type in every version of it; else ErrInvalid.
	Fields []FieldFilter
}

// FieldFilter is a condition on one field: Eq, or a range of one or two
// bounds. nil means "no such bound". Values are as Item.Fields holds them
// (an int field takes any whole number). Ranges are for int fields only.
type FieldFilter struct {
	Field                string
	Eq, Gt, Gte, Lt, Lte any
}
