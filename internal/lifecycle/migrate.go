package lifecycle

import (
	"cmp"
	"maps"
	"slices"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

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

// checkRank is the order in which the checks are reported.
var checkRank = map[MigrationCheck]int{
	TargetNotLatest: 0, MigrateClosed: 1, MigrateHeld: 2, ActionUnfinished: 3, ReactionUnfinished: 4, HasTimer: 5,
	EventsUndelivered: 6, StateMissing: 7, FieldMissing: 8, FieldChanged: 9, MigrateInvariant: 10, MigrateNotUnique: 11,
	LinkChanged: 12, ContainerNotAdmitted: 13, ClaimEmpty: 14, PathNotDeclared: 15, PathMissingFields: 16,
}

// SortMisfits puts misfits in the order they are reported: by check, and
// within one check as they were found. The item service uses it to merge
// what Fits found with the checks that need the database.
func SortMisfits(ms []Misfit) {
	slices.SortStableFunc(ms, func(a, b Misfit) int { return cmp.Compare(checkRank[a.Check], checkRank[b.Check]) })
}

// HasStateTimer reports whether a state has anything that plays out over
// time while an Item stays in it: a timer, a timer at a field's instant,
// or an acting timer of either sort. An Item in such a state is not
// migrated in this step (track 1, item 6, step 3 will).
func HasStateTimer(t *typedef.Type, state string) bool {
	_, timer := TimerFor(t, state)
	_, at := AtTimerFor(t, state)
	return timer || at || len(ActingTimers(t, state)) > 0 || len(ActingAtTimers(t, state)) > 0
}

// Fits reports every reason an Item on version from does not fit version
// to of the same Type, in the order the checks are documented; none means
// it fits. It judges the Item as it is and the path it took:
//
//   - guards of past moves are not run again (the values of then are not
//     what the Item holds now), and who made them is not judged;
//   - a field that holds no value is not looked at, nor is who may write
//     a field, nor what a state freezes: a migrate writes no field.
func Fits(from, to *typedef.Type, f MigrationFacts) []Misfit {
	var out []Misfit
	if st, ok := from.State(f.State); ok && (st.Category == "done" || st.Category == "canceled") {
		out = append(out, Misfit{Check: MigrateClosed, State: f.State})
	}
	newState, stateThere := to.State(f.State)
	if !stateThere {
		out = append(out, Misfit{Check: StateMissing, State: f.State})
	}

	// Fields that hold a value: still declared, and the value still one
	// the new declaration takes.
	var missing, changed []string
	for _, id := range slices.Sorted(maps.Keys(f.Fields)) {
		v := f.Fields[id]
		if v == nil {
			continue
		}
		nf, ok := to.Field(id)
		if !ok {
			missing = append(missing, id)
			continue
		}
		if of, was := from.Field(id); was && (of.Type != nf.Type || of.Multiple != nf.Multiple) {
			changed = append(changed, id)
			continue
		}
		// More values than the new version lets the field hold.
		if list, isList := v.([]any); isList && nf.Max > 0 && len(list) > nf.Max {
			changed = append(changed, id)
			continue
		}
		if to.CheckFields(map[string]any{id: v}, false) != nil {
			changed = append(changed, id)
		}
	}
	if len(missing) > 0 {
		out = append(out, Misfit{Check: FieldMissing, Fields: missing})
	}
	if len(changed) > 0 {
		out = append(out, Misfit{Check: FieldChanged, Fields: changed})
	}

	// Every invariant of the new version that does not hold. Fields the
	// new version does not declare are not its variables.
	known := map[string]any{}
	for id, v := range f.Fields {
		if _, ok := to.Field(id); ok && !slices.Contains(changed, id) {
			known[id] = v
		}
	}
	for _, id := range failedInvariants(to, known) {
		out = append(out, Misfit{Check: MigrateInvariant, Invariant: id})
	}

	// Links the Item has, in the order its own version declares them.
	for _, old := range from.Links {
		var targets []string
		for _, l := range f.Links {
			if l.Name == old.Name {
				targets = append(targets, l.TargetType)
			}
		}
		if len(targets) == 0 {
			continue
		}
		if !sameLink(old, to, targets) {
			out = append(out, Misfit{Check: LinkChanged, LinkType: old.Name})
		}
	}

	where := f.ContainerType
	if f.Top {
		where = ""
	}
	if !to.Start.Admits(where) {
		out = append(out, Misfit{Check: ContainerNotAdmitted})
	}
	if !stateThere {
		return out // no claim and no path to judge in a state the new version lacks
	}
	if newState.Claim != "" {
		if who, _ := f.Fields[newState.Claim].(string); who == "" {
			out = append(out, Misfit{Check: ClaimEmpty, State: f.State, Fields: []string{newState.Claim}})
		}
	}
	return append(out, pathMisfits(to, f)...)
}

// failedInvariants are the IDs of the invariants of t that do not hold on
// fields, in the Type's order.
func failedInvariants(t *typedef.Type, fields map[string]any) []string {
	if len(t.Invariants) == 0 {
		return nil
	}
	vars := make(map[string]any, len(t.Fields))
	addFieldVars(vars, t, fields)
	var failed []string
	for _, inv := range t.Invariants {
		out, _, err := inv.Program.Eval(vars)
		holds := false
		if err == nil {
			holds, _ = out.Value().(bool)
		}
		if !holds {
			failed = append(failed, inv.ID)
		}
	}
	return failed
}

// sameLink reports whether t declares the link old the same way (inverse
// and both cardinalities) and still takes every target Type in targets.
func sameLink(old typedef.LinkType, t *typedef.Type, targets []string) bool {
	for _, l := range t.Links {
		if l.Name != old.Name {
			continue
		}
		if l.Inverse != old.Inverse || l.Max != old.Max || l.InverseMax != old.InverseMax {
			return false
		}
		if slices.Contains(l.Targets, "*") {
			return true
		}
		for _, target := range targets {
			if !slices.Contains(l.Targets, target) {
				return false
			}
		}
		return true
	}
	return false
}

// pathMisfits judges the path: the Item's first state is where t starts,
// every move it made is a move t declares (each undeclared one reported
// once), and what t requires for the last move is set now.
func pathMisfits(t *typedef.Type, f MigrationFacts) []Misfit {
	var out []Misfit
	if len(f.Path) == 0 {
		return nil
	}
	if f.Path[0] != t.InitialState {
		out = append(out, Misfit{Check: PathNotDeclared, ToState: f.Path[0]})
	}
	seen := map[[2]string]bool{}
	for i := 1; i < len(f.Path); i++ {
		step := [2]string{f.Path[i-1], f.Path[i]}
		if seen[step] {
			continue
		}
		seen[step] = true
		if _, ok := t.Transition(step[0], step[1]); !ok {
			out = append(out, Misfit{Check: PathNotDeclared, State: step[0], ToState: step[1]})
		}
	}
	if n := len(f.Path); n >= 2 {
		if tr, ok := t.Transition(f.Path[n-2], f.Path[n-1]); ok {
			var unset []string
			for _, id := range tr.Required {
				if f.Fields[id] == nil {
					unset = append(unset, id)
				}
			}
			if len(unset) > 0 {
				slices.Sort(unset)
				out = append(out, Misfit{Check: PathMissingFields, State: f.Path[n-1], Fields: unset})
			}
		}
	}
	return out
}

// Migration is one migrate in an Item's history: the item_seq of its
// migrated event, and the versions it went from and to.
type Migration struct {
	Seq      int64
	From, To int
}

// VersionAt returns the version of its Type the Item was on when its event
// at item_seq seq was written (track 1, item 6, step 3). ms are the Item's
// migrates in item_seq order; current is its version now. Each state entry
// is judged by the version it was made under. The migrated event itself
// is of the version it went to.
func VersionAt(ms []Migration, current int, seq int64) int {
	if len(ms) == 0 {
		return current
	}
	v := ms[0].From
	for _, m := range ms {
		if m.Seq > seq {
			break
		}
		v = m.To
	}
	return v
}
