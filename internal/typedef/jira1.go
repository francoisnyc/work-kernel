package typedef

// Jira proof of concept, part 1: contract stubs (Phase B). The declarations
// parse; nothing checks or uses them yet.

// AnyType, alone in a link type's targets, lets the link point to an Item
// of any Type (a Subscription's `watches`).
const AnyType = "*"

// Incoming names the links that other Types declare towards this Type: the
// Items of the `From` Types linked to an Item by their link `Link`. Guards
// read it as links.<Name> (state and category only) and rollups count over
// it. Name must equal the inverse every From Type declares for Link, so one
// name serves reads, search, guards and rollups (checked across Types at
// load, CheckIncoming).
type Incoming struct {
	Name string   `yaml:"name" json:"name"`
	Link string   `yaml:"link" json:"link"`
	From []string `yaml:"from" json:"from"` // Type IDs, any version
}

// The two things a rollup counts by.
const (
	CountByCategory = "category"
	CountByState    = "state"
)

// Rollup is a value computed when an Item is read: a count of the Items
// linked under Over (a link name the Type can read: declared, own inverse
// or incoming), grouped by state category or by state. It counts only what
// the reader can see; it is never stored and not searchable.
type Rollup struct {
	ID      string `yaml:"id" json:"id"`
	Over    string `yaml:"over" json:"over"`
	CountBy string `yaml:"count_by" json:"count_by"`
}

// CheckIncoming checks every Type's `incoming` against the set loaded at
// start: each From Type is loaded, some version of it declares Link with
// this Type among its targets, and every such declaration's inverse is the
// incoming name. ParseDir will call it: one Type file cannot know the others.
//
// Phase B stub: it checks nothing.
func CheckIncoming(types []*Type) error {
	return nil
}
