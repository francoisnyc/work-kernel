package lifecycle

import "github.com/francoisnyc/work-kernel/internal/typedef"

// Migrating an Item to the latest version of its Type (track 1, item 6,
// step 2): whether the Item fits that version is lifecycle logic, so it is
// decided here, in one pure function, from both versions and the Item's
// facts. The item service gathers the facts under the Item's lock and adds
// the checks that need the database (unique among siblings, quiet).

// OpMigrated is the event of a migrate: its payload is from_version and
// to_version, nothing else.
const OpMigrated = "migrated"

// MigrationCheck is the code of one reason an Item cannot be migrated.
type MigrationCheck string

// The checks, in the order they are reported.
const (
	TargetNotLatest      MigrationCheck = "target_not_latest"      // the version named is not the latest, or not newer than the Item's
	MigrateClosed        MigrationCheck = "closed"                 // the Item's state is done or canceled
	MigrateHeld          MigrationCheck = "held"                   // an open hold
	ActionUnfinished     MigrationCheck = "action_unfinished"      // an action or enrich hook has not finished, under either version
	ReactionUnfinished   MigrationCheck = "reaction_unfinished"    // a reaction or an undo has not finished, under either version
	HasTimer             MigrationCheck = "has_timer"              // the state has a timer, under either version
	EventsUndelivered    MigrationCheck = "events_undelivered"     // events of the Item are still in the outbox
	StateMissing         MigrationCheck = "state_missing"          // the new version has no such state
	FieldMissing         MigrationCheck = "field_missing"          // a field holding a value is not in the new version
	FieldChanged         MigrationCheck = "field_changed"          // a field holding a value has another type, shape or options
	MigrateInvariant     MigrationCheck = "invariant_failed"       // an invariant of the new version does not hold
	MigrateNotUnique     MigrationCheck = "not_unique"             // a sibling holds the same value of a unique field
	LinkChanged          MigrationCheck = "link_changed"           // a link the Item has is not declared the same way
	ContainerNotAdmitted MigrationCheck = "container_not_admitted" // the new version's start.in does not admit where the Item is
	ClaimEmpty           MigrationCheck = "claim_empty"            // the state has a claim in the new version and nobody holds it
	PathNotDeclared      MigrationCheck = "path_not_declared"      // a move the Item made is not a move of the new version
	PathMissingFields    MigrationCheck = "path_missing_fields"    // the last move's required fields are not set
)

// Misfit is one reason an Item does not fit the new version. It names
// what the Type declares (states, fields, link types, an invariant), never
// a value or another Item.
type Misfit struct {
	Check     MigrationCheck
	State     string   // the state it is about; for PathNotDeclared, the state the move left
	ToState   string   // PathNotDeclared: the state the move entered
	Fields    []string // field IDs, sorted
	LinkType  string
	Invariant string
}

// HeldLink is a link an Item has as source: its declared name and the
// Type of its target.
type HeldLink struct {
	Name       string
	TargetType string
}

// MigrationFacts is what Fits needs to know of an Item.
type MigrationFacts struct {
	State  string
	Fields map[string]any
	Links  []HeldLink
	// Top is true for an Item in no container; else ContainerType is its
	// container's Type ID.
	Top           bool
	ContainerType string
	// Path is every state the Item entered, oldest first: its first state,
	// then the state each of its moves entered. The last is State.
	Path []string
}

// Fits reports every reason an Item on version from does not fit version
// to of the same Type; none means it fits. A stub: not built yet.
func Fits(from, to *typedef.Type, f MigrationFacts) []Misfit {
	return nil
}
