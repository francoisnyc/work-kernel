// Package typedef holds a Type: versioned data that describes the fields,
// states and transitions of a kind of Item. A Type names code but never
// contains it. This package only parses and checks Types; deciding whether a
// transition may happen is the job of package lifecycle.
package typedef

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"cel.dev/cel-go/cel"
	"go.yaml.in/yaml/v3"

	"github.com/francoisnyc/work-kernel/internal/item"
)

// FieldType is the kind of value a field holds. A named string type gives
// Go something close to a Java enum: the compiler keeps it apart from plain strings.
type FieldType string

const (
	String FieldType = "string"
	Int    FieldType = "int"
	Bool   FieldType = "bool"
	Enum   FieldType = "enum"
	// Date is a calendar date, stored as an ISO 8601 string (2026-12-01).
	// Guards see it as a CEL timestamp at 00:00 UTC.
	Date FieldType = "date"
	// DateTime is an instant, stored in UTC as an RFC 3339 string in one
	// fixed-width form (DateTimeLayout), so byte order is time order.
	DateTime FieldType = "datetime"
)

// WritableBy says who may write a field. The zero value is anyone who may
// write the Item. It is a named type, not a bool, so link types (part 2) can
// take the same attribute and later writers (link reactions) can be added.
type WritableBy string

const (
	WritableByAnyone WritableBy = ""
	// WritableByKernel: only the internal door (hooks) writes the field; the
	// public door refuses it on create, update and a transition's patch.
	WritableByKernel WritableBy = "kernel"
)

// The entries of a field's writable_by list besides role:<name> (fields
// closed to agents by default): any person, and any agent.
const (
	WriterPeople = "people"
	WriterAgents = "agents"
)

// Item attributes a hook may read besides fields: set by the kernel at
// create and never editable. They are also guard variables.
const (
	AttrCreatedBy         = "created_by"
	AttrCreatedOnBehalfOf = "created_on_behalf_of"
	// AttrCreator is who the Item was created for: created_on_behalf_of,
	// else created_by (the same rule as allowed: [creator]). Hooks may read
	// it (Step 2, part 3), e.g. to email the requester.
	AttrCreator = "creator"
)

// VarToday is the one guard variable that is not a field: today's date
// (UTC). Who acts is never a guard variable: identity rules are declared in
// `allowed` and `except`. The names below stay reserved (no field may take
// one, nor "links"), so a later version can give them a meaning.
const (
	VarToday            = "today"
	VarCaller           = "caller"
	VarCallerOnBehalfOf = "caller_on_behalf_of"
)

// reservedNames may not be field IDs: guard variables, the creator
// attributes a hook may read, and the links prefix.
var reservedNames = []string{VarToday, VarCaller, VarCallerOnBehalfOf, AttrCreatedBy, AttrCreatedOnBehalfOf, AttrCreator, "links", "cause",
	ContainerVar, ChildrenVar, EachVar}

// IsAttribute reports whether name is an Item attribute a hook may read
// besides its fields: created_by or created_on_behalf_of.
func IsAttribute(name string) bool {
	return name == AttrCreatedBy || name == AttrCreatedOnBehalfOf || name == AttrCreator
}

// categories are the fixed state categories from the design. "none" means
// "not work": views of work leave it out, so a chat message never shows up as
// active or done work.
var categories = []string{"triage", "backlog", "todo", "active", "waiting", "done", "canceled", "none"}

// ValidCategory reports whether c is one of the fixed state categories.
func ValidCategory(c string) bool { return slices.Contains(categories, c) }

// A Type that declares no states has no lifecycle. It gets this one implicit
// state, so every Item still has a state and nothing else needs a special case.
const (
	ImplicitState         = "default"
	ImplicitStateCategory = "none"
)

// DateTimeLayout is the one stored and returned form of a datetime value:
// UTC, six fractional digits, "Z".
const DateTimeLayout = "2006-01-02T15:04:05.000000Z"

// VarCauseAt is the variable a `sets` expression reads the write's time
// from: the timestamp of the event being written.
const VarCauseAt = "cause.at"

// AtTimer moves an Item out of a state when the instant held by one of its
// datetime fields passes. Like Timer, it names a transition the Type
// declares from that state.
type AtTimer struct {
	Field string `yaml:"field" json:"field"`
	To    string `yaml:"to" json:"to"`
}

// TimerPrincipal is the principal a timer's move is made as. A timer gets no
// silent exemption from `allowed`: the Type must name it (see check).
const TimerPrincipal = "system:timer"

// The two kinds of entry in a transition's `allowed` list.
const (
	AllowedRolePrefix      = "role:"
	AllowedPrincipalPrefix = "principal:"
	// AllowedFieldPrefix names a principal field: the entry matches the
	// principal the field holds when the transition runs.
	AllowedFieldPrefix = "field:"
	// AllowedCreator in `allowed` matches the requester: the principal whose
	// user is the user the Item was created for (policy.IsRequester), judged
	// once for the principal as a whole. In `except` it matches anyone who
	// took part in creating the Item (policy.TookPartInCreation). It is the
	// only entry `except` takes for now.
	AllowedCreator = "creator"
)

// The struct tags (the text in backquotes) tell the YAML and JSON libraries
// which key maps to which field, like Jackson annotations.

// Field is one field of a Type, known by a stable ID.
type Field struct {
	ID      string    `yaml:"id" json:"id"`
	Type    FieldType `yaml:"type" json:"type"`
	Options []string  `yaml:"options" json:"options,omitempty"` // enum only
	// WritableBy limits who may write the field; empty is anyone.
	WritableBy WritableBy `yaml:"writable_by" json:"writable_by,omitempty"`
	// WritableByRoles (from writable_by: [role:<name>, ...]): on the public
	// door only principals holding one of these roles write the field (an
	// agent and the user it acts for must both hold one). The kernel's own
	// writers are not affected. Role names, without the role: prefix.
	WritableByRoles []string `yaml:"-" json:"writable_by_roles,omitempty"`
	// WritableByPeople and WritableByAgents (from writable_by: [people,
	// agents, ...]): fields closed to agents by default. A field whose
	// writable_by lists nothing is people's (PeopleWrite); agents write
	// only a field that lists agents, or a role they hold.
	WritableByPeople bool `yaml:"-" json:"writable_by_people,omitempty"`
	WritableByAgents bool `yaml:"-" json:"writable_by_agents,omitempty"`
	// writers is the writable_by list as written, kept only when it names
	// people or agents, so that check's problems point at the right entry
	// (WritableByRoles holds the roles alone). Never stored.
	writers []string
	// Multiple makes the field hold a set of values of its type (string,
	// enum, principal or date): unordered, no duplicates, stored sorted; an
	// empty set is an unset field. Max, when set, is the most members it
	// may have.
	Multiple bool `yaml:"multiple" json:"multiple,omitempty"`
	Max      int  `yaml:"max" json:"max,omitempty"`
	// UniqueAmongSiblings (Jira 2.4b, kernel change 9), on a principal
	// field: no two Items of this Type in the same container, both in one
	// of these states, hold the same value. Phase B stub: parsed only.
	UniqueAmongSiblings []string `yaml:"unique_among_siblings" json:"unique_among_siblings,omitempty"`
	// Label and Help are presentation hints (the Inbox for people, PR 1):
	// a short name for people and a line of help. They enforce nothing.
	// Empty: none. See hints.go for their checks.
	Label string `yaml:"label" json:"label,omitempty"`
	Help  string `yaml:"help" json:"help,omitempty"`
}

// State is one lifecycle state. Every state has a category.
type State struct {
	ID       string `yaml:"id" json:"id"`
	Category string `yaml:"category" json:"category"`
	// Timer is nil for a state without one. A pointer field plays the role of
	// an Optional here; omitempty leaves it out of the stored JSON when nil.
	Timer *Timer `yaml:"timer" json:"timer,omitempty"`
	// At is nil for a state without a timer at a field's instant. A state
	// has Timer or At, never both.
	At *AtTimer `yaml:"at" json:"at,omitempty"`
	// Frozen are the fields no write may change while an Item is in this
	// state. The transition entering the state may set them.
	Frozen []string `yaml:"frozen" json:"frozen,omitempty"`
	// RequesterOnly are the fields only the Item's requester (its effective
	// creator, or an agent acting for them) writes on the public door while
	// the Item is in this state, besides what writable_by asks
	// (policy.MayWriteInState). Never a kernel-written field, nor one this
	// state freezes.
	RequesterOnly []string `yaml:"requester_only" json:"requester_only,omitempty"`
	// Claim names the principal field that holds who claimed the Item
	// while it is in this state (track 1 item 5, step 2): whoever moves it
	// into the state is recorded there, and only they move it on, besides
	// what `allowed` asks; a move marked release gives the claim up.
	// Packages policy and itemservice judge it.
	Claim string `yaml:"claim" json:"claim,omitempty"`
	// WaitsOn says whom an Item in this state waits on besides what its
	// moves name (track 1 item 5, step 5): "requester", its requester (the
	// effective creator). Empty for none. Package policy judges it.
	WaitsOn string `yaml:"waits_on" json:"waits_on,omitempty"`
	// Acting are the state's acting timers (Step 2, part 3): each runs an
	// action hook after a delay, without a transition, optionally again at
	// an interval, at most Max times per entry into the state.
	Acting []ActingTimer `yaml:"acting" json:"acting,omitempty"`
	// Label and Help are presentation hints (the Inbox for people, PR 1):
	// a short name for people and a line of help. They enforce nothing.
	// Empty: none. See hints.go for their checks.
	Label string `yaml:"label" json:"label,omitempty"`
	Help  string `yaml:"help" json:"help,omitempty"`
	// Due (a presentation hint) names a datetime field whose instant an
	// Item in this state is due by. It enforces nothing. Empty: none.
	Due string `yaml:"due" json:"due,omitempty"`
}

// ActingTimer runs its hook (an action hook with on: timer) After the event
// that entered the state, then every Every, Max runs in all; leaving the
// state stops it. Its runs are keyed timer:<item>:<timer>:<entry seq>:<n>.
type ActingTimer struct {
	ID    string `yaml:"id" json:"id"`
	After string `yaml:"after" json:"after"`           // a Go duration, e.g. 48h
	Every string `yaml:"every" json:"every,omitempty"` // a Go duration; required when Max > 1
	Max   int    `yaml:"max" json:"max"`               // the total number of runs, at least 1
	Hook  string `yaml:"hook" json:"hook"`             // an action hook of this Type, on: timer

	// At, When and Sets make an acting timer at an instant (Jira part 2,
	// step 3; see jira23.go): it has no After, Every or Max.
	At   string            `yaml:"at" json:"at,omitempty"`
	When string            `yaml:"when" json:"when,omitempty"`
	Sets map[string]string `yaml:"sets" json:"sets,omitempty"`
	// When and Sets compiled, filled when the Type is loaded.
	WhenProgram cel.Program            `yaml:"-" json:"-"`
	SetPrograms map[string]cel.Program `yaml:"-" json:"-"`

	// After and Every parsed, filled when the Type is loaded.
	AfterDuration time.Duration `yaml:"-" json:"-"`
	EveryDuration time.Duration `yaml:"-" json:"-"`
}

// Timer moves an Item out of a state it has stayed in for too long. It is a
// spike stand-in: real SLAs become standalone objects later.
type Timer struct {
	After string `yaml:"after" json:"after"` // as written, e.g. "4h" or "90s"
	To    string `yaml:"to" json:"to"`       // the state to move to

	// Duration is After in parsed form, filled when the Type is loaded.
	Duration time.Duration `yaml:"-" json:"-"`
}

// Transition is one allowed move between two states.
type Transition struct {
	From     string   `yaml:"from" json:"from"`
	To       string   `yaml:"to" json:"to"`
	Guard    string   `yaml:"guard" json:"guard,omitempty"`       // a CEL expression; see compileGuard
	Required []string `yaml:"required" json:"required,omitempty"` // field IDs that must be set
	// Given are fields of Required the move itself must give: set, with a
	// value, in its own patch (track 1 item 3). A value written before,
	// by anyone, does not count. Package lifecycle judges it.
	Given []string `yaml:"given" json:"given,omitempty"`
	// Allowed says who may make the move: entries are "role:<name>" or
	// "principal:<id>". Empty means anyone who can see the Item. The list is
	// only checked for shape here; package policy decides.
	Allowed []string `yaml:"allowed" json:"allowed,omitempty"`
	// Except refuses whoever matches it, even if `allowed` lets them in
	// (ErrForbidden). For now it takes only "creator".
	Except []string `yaml:"except" json:"except,omitempty"`
	// Release marks a move out of a claimed state that gives the claim up:
	// `allowed` alone decides it, where every other move out is the
	// claimant's only. It must clear the claim field.
	Release bool `yaml:"release" json:"release,omitempty"`
	// Escape marks a move that may leave a state while a hold is open on it
	// (cancel, say). Making it cancels the hold.
	Escape bool `yaml:"escape" json:"escape,omitempty"`
	// Clears are the fields the move unsets in the same write (reopening
	// clears the resolution). Required fields and the guard are judged
	// before the clear, the invariants after it.
	Clears []string `yaml:"clears" json:"clears,omitempty"`
	// Sets are computed field effects: field ID -> CEL expression,
	// evaluated in the move's own write, on the guard's view of the fields
	// plus cause.at and today. A result of null unsets the field.
	Sets map[string]string `yaml:"sets" json:"sets,omitempty"`
	// Label and Help are presentation hints (the Inbox for people, PR 1):
	// a short name for people and a line of help. They enforce nothing.
	// Empty: none. See hints.go for their checks.
	Label string `yaml:"label" json:"label,omitempty"`
	Help  string `yaml:"help" json:"help,omitempty"`
	// Primary (a presentation hint; see hints.go) marks the main move out of its from
	// state, for a screen's one button: at most one per state. It decides
	// nothing about who may make it.
	Primary bool `yaml:"primary" json:"primary,omitempty"`

	// Program is Guard compiled, filled when the Type is loaded; nil when
	// there is no guard. The "-" tag keeps it out of YAML and JSON. Package
	// lifecycle evaluates it; this package only compiles it.
	Program cel.Program `yaml:"-" json:"-"`
	// GuardLinks are the link names the guard reads (links.<name>), sorted;
	// filled when the Type is loaded. A transition with none needs no link
	// read and no lock beyond its own Item.
	GuardLinks []string `yaml:"-" json:"-"`
	// SetPrograms are the Sets expressions compiled, by field ID; filled
	// when the Type is loaded. Package lifecycle evaluates them.
	SetPrograms map[string]cel.Program `yaml:"-" json:"-"`
	// Jira 2.4b, filled when the Type is loaded: whether the guard reads
	// the container (container.state, .category); the Types of contained
	// Items it reads (children.<type>), sorted; the container's fields the
	// Sets read (container.<field>), sorted.
	GuardContainer bool     `yaml:"-" json:"-"`
	GuardChildren  []string `yaml:"-" json:"-"`
	SetsContainer  []string `yaml:"-" json:"-"`
}

// Type is one version of a Type definition.
type Type struct {
	ID      string `yaml:"id" json:"id"`
	Version int    `yaml:"version" json:"version"`
	// Label and Help are the Type's own presentation hints (the Inbox, the
	// look): its name for people and a line about it, checked as the
	// other labels are (hints.go). They enforce nothing.
	Label        string       `yaml:"label" json:"label,omitempty"`
	Help         string       `yaml:"help" json:"help,omitempty"`
	InitialState string       `yaml:"initial_state" json:"initial_state"`
	Fields       []Field      `yaml:"fields" json:"fields,omitempty"`
	States       []State      `yaml:"states" json:"states"`
	Transitions  []Transition `yaml:"transitions" json:"transitions,omitempty"`
	Hooks        []Hook       `yaml:"hooks" json:"hooks,omitempty"`
	// Links are the link types Items of this version may have as sources.
	Links []LinkType `yaml:"links" json:"links,omitempty"`
	// Reactions are the writes this Type's transitions make on linked Items.
	Reactions []Reaction `yaml:"reactions" json:"reactions,omitempty"`
	// AcceptsReactions: which reactions of other Types (<type>:<id>, no
	// version) may write which fields of this Type's Items. Anything else
	// is refused (reaction_not_accepted).
	AcceptsReactions map[string]Accepted `yaml:"accepts_reactions" json:"accepts_reactions,omitempty"`
	// Invariants are rules over the fields every write must keep.
	Invariants []Invariant `yaml:"invariants" json:"invariants,omitempty"`
	// Incoming names links other Types declare towards this Type.
	Incoming []Incoming `yaml:"incoming" json:"incoming,omitempty"`
	// Rollups are values computed when an Item of this Type is read.
	Rollups []Rollup `yaml:"rollups" json:"rollups,omitempty"`
	// ReadableByReactions are the fields a create reaction of another Type
	// may read from an Item of this Type (Jira part 2, step 3).
	ReadableByReactions []string `yaml:"readable_by_reactions" json:"readable_by_reactions,omitempty"`
	// Offers (Jira part 2, step 3b; see jira23b.go): what
	// this Type offers to the hooks of the Items contained in its Items,
	// offer name -> one of its principal fields. A hook there reads
	// container:<name> (ContainerOfferPrefix).
	Offers map[string]string `yaml:"offers" json:"offers,omitempty"`
	// Customers (Jira part 2, step 4a). Own and Shared are declared by a
	// container's Type: the Types customers may create inside its Items and
	// see when they are their own (Own), or see and create inside an Item a
	// customer sees (Shared). CustomerFields is declared by any Type whose
	// Items a customer may see: the fields, link names and rollup IDs a
	// customer sees and may set at creation (see customers.go).
	Own            []string `yaml:"own" json:"own,omitempty"`
	Shared         []string `yaml:"shared" json:"shared,omitempty"`
	CustomerFields []string `yaml:"customer_fields" json:"customer_fields,omitempty"`
	// PlacementWritableBy (Jira 2.4b, kernel change 10): who may move or
	// narrow Items of this version: [kernel] (no person) or a list of
	// role:<name>. Empty is anyone who sees the Item, as before. Phase B
	// stub: parsed only.
	PlacementWritableBy []string `yaml:"placement_writable_by" json:"placement_writable_by,omitempty"`
	// Start (starting work, step 2; see start.go): where Items of the
	// Type may be created, and by whom. nil when the Type has no block.
	Start *Start `yaml:"start" json:"start,omitempty"`
	// Migrate (track 1, item 6, step 2): who may migrate an open Item of
	// an older version to this one, while it is the latest. nil: nobody.
	// The latest version's block decides (see migrate.go).
	Migrate *Migrate `yaml:"migrate" json:"migrate,omitempty"`
	// Summary (a presentation hint) names the fields that summarise an
	// Item under its title (a list row, a card): at most 3. Empty: none.
	Summary []string `yaml:"summary" json:"summary,omitempty"`
	// emptyHints are the paths of the labels and helps the YAML wrote as
	// "", which a string cannot tell from none (see hints.go). Never stored.
	emptyHints []string
}

// Migrate is a Type's migrate block. Allowed lists who may migrate Items
// to this version: role:<name> and principal:user:<id>.
type Migrate struct {
	Allowed []string `yaml:"allowed" json:"allowed,omitempty"`
}

// Jira 2.4b keywords. AllowedNobody (kernel change 12) is the `allowed`
// entry no person, agent or customer passes: only what passes by its own
// rule (a reaction by consent) makes the move. AllowedContainerCreator
// (kernel change 7), in `except`, matches whoever took part in creating
// the Item's container. OnGuardSkip (kernel change 6) is a move
// reaction's on_guard value: a target guard that does not hold is a skip
// (guard_false), not a failure. Phase B stubs: declared only.
const (
	AllowedNobody           = "nobody"
	AllowedContainerCreator = "container_creator"
	OnGuardSkip             = "skip"
	// EachVar is the CEL name of the member a for_each create reaction is
	// creating one Item for (kernel change 3).
	EachVar = "each"
)

// ContainerOfferPrefix starts a hook read that names an offer of the
// Item's container: container:<offer>.
const ContainerOfferPrefix = "container:"

// Parse reads a Type from YAML and checks it. Unknown keys are errors, so a
// typo in a Type file is caught at load time. The error, if any, is
// Problems: every problem found, positioned in the YAML.
func Parse(data []byte) (*Type, error) {
	t, _, ps := parseDoc(data)
	if len(ps) > 0 {
		return nil, ps
	}
	return t, nil
}

// parseDoc runs stages (a) and (b) on one YAML document. It returns the
// document too, so ParseDir can place the problems of stage (c).
func parseDoc(data []byte) (*Type, *yaml.Node, Problems) {
	doc, ps := readYAML(data)
	if len(ps) == 0 {
		var t *Type
		if t, ps = decodeType(doc); len(ps) == 0 {
			ps = t.problems()
			position(ps, doc)
			if len(ps) == 0 {
				return t, doc, nil
			}
		}
	}
	sortProblems(ps)
	return nil, doc, ps
}

// FromJSON reads a Type from the JSON form produced by json.Marshal, which
// is how the item service stores it in Postgres. Its problems have paths
// but no positions.
func FromJSON(data []byte) (*Type, error) {
	var t Type
	if err := json.Unmarshal(data, &t); err != nil {
		var ps Problems
		newReporter(&ps, "", 0).fail(CodeWrongShape, "", "Store the definition json.Marshal produced",
			"typedef: parse JSON: %v", err)
		return nil, ps
	}
	if ps := t.problems(); len(ps) > 0 {
		sortProblems(ps)
		return nil, ps
	}
	return &t, nil
}

// problems runs stage (b), the checks on this Type alone.
func (t *Type) problems() Problems {
	var ps Problems
	errors.As(t.check(), &ps)
	return ps
}

// PeopleWrite reports whether any person (a user or a customer) may write
// the field on the public door, roles aside: when its writable_by lists
// people, or lists nothing at all (the default: closed to agents, open to
// people). A kernel field is no one's.
func (f Field) PeopleWrite() bool {
	if f.WritableBy == WritableByKernel {
		return false
	}
	return f.WritableByPeople || (len(f.WritableByRoles) == 0 && !f.WritableByAgents)
}

// Field returns the field with the given ID. The second value reports
// whether it exists, Go's usual alternative to returning null.
func (t *Type) Field(id string) (Field, bool) {
	for _, f := range t.Fields {
		if f.ID == id {
			return f, true
		}
	}
	return Field{}, false
}

// State returns the state with the given ID.
func (t *Type) State(id string) (State, bool) {
	for _, s := range t.States {
		if s.ID == id {
			return s, true
		}
	}
	return State{}, false
}

// Transition returns the transition between two states, if the Type declares one.
func (t *Type) Transition(from, to string) (Transition, bool) {
	for _, tr := range t.Transitions {
		if tr.From == from && tr.To == to {
			return tr, true
		}
	}
	return Transition{}, false
}

// check validates the definition, parses every guard, and gives a Type
// without states its one implicit state. It reports every problem of the
// Type (stage b) as Problems. A declaration with a problem still counts as
// declared, so what only follows from it is not reported again.
func (t *Type) check() error {
	var ps Problems
	r := newReporter(&ps, t.ID, t.Version)
	if t.ID == "" {
		r.fail(CodeRequired, "id", "Name the Type, such as id: ticket", "id is required")
	}
	if t.Version < 1 {
		r.fail(CodeInvalidValue, "version", "Number the first version 1, and each new version one more", "version must be 1 or more")
	}

	seen := map[string]bool{}
	for i := range t.Fields {
		t.checkField(r, i, seen)
	}

	noStates := len(t.States) == 0
	if noStates {
		// No lifecycle: naming an initial state or a transition is a mistake.
		if t.InitialState != "" {
			r.fail(CodeKeyNotApplicable, "initial_state", "Remove initial_state, or declare the states",
				"initial_state %q is set but the Type declares no states", t.InitialState)
		}
		if len(t.Transitions) > 0 {
			r.fail(CodeKeyNotApplicable, "transitions", "Remove the transitions, or declare the states",
				"transitions are declared but the Type declares no states")
		}
		t.States = []State{{ID: ImplicitState, Category: ImplicitStateCategory}}
		t.InitialState = ImplicitState
	}
	seen = map[string]bool{}
	for i := range t.States {
		t.checkState(r, i, seen)
	}
	if _, ok := t.State(t.InitialState); !ok {
		if t.InitialState == "" {
			r.fail(CodeRequired, "initial_state", "Name the state Items start in",
				"initial_state %q is not a declared state", t.InitialState)
		} else {
			r.fail(CodeUnknownState, "initial_state", "Name one of the declared states",
				"initial_state %q is not a declared state", t.InitialState)
		}
	}

	t.checkLinks(r)
	t.checkKernelLinks(r)
	// Before the guards: they may read the incoming names.
	t.checkIncoming(r)
	t.checkRollups(r)
	guards, err := newGuardEnv(t)
	if err != nil {
		// celFields leaves out what would break the environment; anything
		// else is reported once, on the fields.
		r.fail(CodeExpressionInvalid, "fields", "Fix the fields the message names", "%s", err.Error())
	}
	if !noStates {
		seen = map[string]bool{}
		for i := range t.Transitions {
			t.checkTransition(r, i, seen, guards)
		}
	}

	// Timers come last: each must name a transition checked above.
	for i := range t.States {
		t.checkTimer(r, i)
	}
	t.checkAtTimers(r)

	// Hooks last: they name states, transitions and fields checked above.
	t.checkOffers(r)
	t.checkCustomers(r)
	t.checkHooks(r)
	// Then what names hooks and fields: reactions, invariants, consents.
	t.checkReactions(r)
	t.checkInvariants(r)
	t.checkAccepts(r)
	t.checkMultiUses(r)
	t.checkReadable(r)
	t.checkJira24b(r)
	// Where Items of this Type may be created, and by whom.
	t.checkStart(r)
	t.checkMigrate(r)
	// Clears name fields, and may not sit on a held transition.
	t.checkClears(r)
	// Computed effects: the same, and each expression compiles to a value
	// of its field.
	t.checkSets(r)
	// What the move itself must give: fields a patch may name, on a move
	// a person makes.
	t.checkGiven(r)
	// A claimed state, and the moves that claim and release.
	t.checkClaim(r)
	t.checkWaitsOn(r)
	// Presentation hints: words for people, which enforce nothing.
	t.checkHints(r)

	// A field only the kernel writes needs a writer: a hook that writes it,
	// or another Type's reaction this Type accepts for it.
	for i, f := range t.Fields {
		if f.WritableBy == WritableByKernel && !t.kernelFieldWritten(f.ID) {
			r.fail(CodeMissingCounterpart, at("fields", i, "writable_by"),
				"Add a hook or a computed effect that writes it, accept a reaction for it, or remove writable_by: kernel",
				"field %s is writable_by kernel, but no hook writes it", f.ID)
		}
	}
	if len(ps) > 0 {
		return ps
	}
	return nil
}

// checkField checks fields[i]. seen holds the IDs declared before it.
func (t *Type) checkField(r reporter, i int, seen map[string]bool) {
	f, p := t.Fields[i], at("fields", i)
	if f.ID == "" {
		r.fail(CodeRequired, at(p, "id"), "Give the field an id, such as f_title", "a field has no id")
		return
	}
	if seen[f.ID] {
		r.fail(CodeDuplicate, at(p, "id"), "Rename one of the two, or remove the second", "duplicate field %q", f.ID)
		return
	}
	seen[f.ID] = true
	if slices.Contains(reservedNames, f.ID) {
		r.fail(CodeReservedName, at(p, "id"), "Choose another id; field ids usually start with f_",
			"field %s: %q is a reserved name (guard variables, creator attributes, links)", f.ID, f.ID)
	}
	switch f.WritableBy {
	case WritableByAnyone, WritableByKernel:
	default:
		r.fail(CodeInvalidValue, at(p, "writable_by"), "Write writable_by: kernel, a list of role:<name>, or leave it out",
			"field %s: writable_by %q: the only value is kernel", f.ID, f.WritableBy)
	}
	f.checkRoles(r, p)
	switch f.Type {
	case String, Int, Bool, Principal, Date, DateTime:
		if len(f.Options) > 0 {
			r.fail(CodeKeyNotApplicable, at(p, "options"), "Remove options, or make the field an enum",
				"field %s: only enum fields take options", f.ID)
		}
	case Enum:
		if len(f.Options) == 0 {
			r.fail(CodeRequired, at(p, "options"), "List the enum's values under options", "field %s: an enum needs options", f.ID)
		}
	default:
		r.fail(CodeInvalidValue, at(p, "type"), "Use string, int, bool, enum, principal, date or datetime",
			"field %s: unknown type %q", f.ID, f.Type)
	}
	// A set of values: for types whose values are text. One value or
	// several is part of what a field is (a new field ID to change it).
	switch {
	case f.Multiple && f.Type != String && f.Type != Enum && f.Type != Principal && f.Type != Date:
		r.fail(CodeKeyNotApplicable, at(p, "multiple"), "Remove multiple, or use a string, enum, principal or date field",
			"field %s: multiple is for string, enum, principal and date fields, not %s", f.ID, f.Type)
	case f.Max < 0:
		r.fail(CodeInvalidValue, at(p, "max"), "Give max as 0 (no limit) or more", "field %s: max cannot be negative", f.ID)
	case f.Max > 0 && !f.Multiple:
		r.fail(CodeKeyNotApplicable, at(p, "max"), "Remove max, or make the field multiple", "field %s: max is for a multiple field", f.ID)
	}
}

// checkState checks states[i], but not its timers.
func (t *Type) checkState(r reporter, i int, seen map[string]bool) {
	s, p := t.States[i], at("states", i)
	if s.ID == "" {
		r.fail(CodeRequired, at(p, "id"), "Give the state an id, such as open", "a state has no id")
		return
	}
	if seen[s.ID] {
		r.fail(CodeDuplicate, at(p, "id"), "Rename one of the two, or remove the second", "duplicate state %q", s.ID)
		return
	}
	seen[s.ID] = true
	if !slices.Contains(categories, s.Category) {
		r.fail(CodeInvalidValue, at(p, "category"), "Use triage, backlog, todo, active, waiting, done, canceled or none",
			"state %s: unknown category %q", s.ID, s.Category)
	}
	frozen := map[string]bool{}
	for j, id := range s.Frozen {
		if _, ok := t.Field(id); !ok {
			r.fail(CodeUnknownField, at(p, "frozen", j), "Declare the field, or remove it from frozen",
				"state %s: frozen field %q is not declared", s.ID, id)
			continue
		}
		if frozen[id] {
			r.fail(CodeDuplicate, at(p, "frozen", j), "Remove the second one", "state %s: frozen field %q is listed twice", s.ID, id)
		}
		frozen[id] = true
	}
	// requester_only, field by field like frozen: a field only the kernel
	// writes has no requester to limit it to, and one the state freezes is
	// nobody's, so listing either contradicts the Type.
	mine := map[string]bool{}
	for j, id := range s.RequesterOnly {
		f, ok := t.Field(id)
		switch {
		case !ok:
			r.fail(CodeUnknownField, at(p, "requester_only", j), "Declare the field, or remove it from requester_only",
				"state %s: requester_only field %q is not declared", s.ID, id)
			continue
		case mine[id]:
			r.fail(CodeDuplicate, at(p, "requester_only", j), "Remove the second one",
				"state %s: requester_only field %q is listed twice", s.ID, id)
		case f.WritableBy == WritableByKernel:
			r.fail(CodeConflict, at(p, "requester_only", j), "Remove it from requester_only: only the kernel writes it",
				"state %s: requester_only field %s is writable_by kernel; the requester never writes it", s.ID, id)
		case frozen[id]:
			r.fail(CodeConflict, at(p, "requester_only", j), "Keep it in frozen or in requester_only, not both",
				"state %s: field %s is both frozen and requester_only; frozen means nobody writes it", s.ID, id)
		}
		mine[id] = true
	}
}

// checkTransition checks transitions[i] and compiles its guard. guards is
// nil if the guard environment could not be built (reported once).
func (t *Type) checkTransition(r reporter, i int, seen map[string]bool, guards *cel.Env) {
	tr, p := &t.Transitions[i], at("transitions", i)
	name := tr.From + " -> " + tr.To
	if _, ok := t.State(tr.From); !ok {
		r.fail(CodeUnknownState, at(p, "from"), "Declare the state, or name one that is declared",
			"transition %s: unknown state %q", name, tr.From)
	}
	if _, ok := t.State(tr.To); !ok {
		r.fail(CodeUnknownState, at(p, "to"), "Declare the state, or name one that is declared",
			"transition %s: unknown state %q", name, tr.To)
	}
	if seen[name] {
		r.fail(CodeDuplicate, p, "Remove one of the two: one transition per pair of states", "duplicate transition %s", name)
		return
	}
	seen[name] = true
	for j, id := range tr.Required {
		if _, ok := t.Field(id); !ok {
			r.fail(CodeUnknownField, at(p, "required", j), "Declare the field, or remove it from required",
				"transition %s: required field %q is not declared", name, id)
		}
	}
	for j, entry := range tr.Allowed {
		t.checkAllowedEntry(r, at(p, "allowed", j), name, tr.Allowed, entry)
	}
	for j, entry := range tr.Except {
		if entry != AllowedCreator && entry != AllowedContainerCreator {
			r.fail(CodeInvalidValue, at(p, "except", j), "Write creator or container_creator",
				"transition %s: except entry %q: except takes creator or %s", name, entry, AllowedContainerCreator)
		}
	}
	if guards == nil {
		return
	}
	prg, reads, err := compileGuard(guards, tr.Guard)
	var ee exprError
	switch {
	case err == nil:
		tr.Program, tr.GuardLinks = prg, reads.links
		tr.GuardContainer, tr.GuardChildren = reads.container, reads.children
	case errors.As(err, &ee) && ee.code == CodeExpressionType:
		r.fail(CodeExpressionType, at(p, "guard"), "Write a condition that is true or false, such as f_count > 0",
			"transition %s: guard %q: %v", name, tr.Guard, err)
	case errors.As(err, &ee) && ee.code == CodeReadNotAllowed:
		r.fail(CodeReadNotAllowed, at(p, "guard"), "Read only what the message says a guard may read",
			"transition %s: guard %q: %v", name, tr.Guard, err)
	default:
		r.fail(CodeExpressionInvalid, at(p, "guard"), "Fix the CEL expression; it reads fields by their ids",
			"transition %s: guard %q: %v", name, tr.Guard, err)
	}
}

// checkAllowedEntry checks one entry of a transition's allowed list.
func (t *Type) checkAllowedEntry(r reporter, p, name string, allowed []string, entry string) {
	if entry == AllowedCreator {
		return
	}
	// nobody: no person, agent or customer passes; only a reaction, by
	// consent, makes the move. It stands alone.
	if entry == AllowedNobody {
		if len(allowed) > 1 {
			r.fail(CodeConflict, p, "Leave nobody alone in allowed, or remove it",
				"transition %s: allowed entry %s stands alone: no one else may be named beside it", name, AllowedNobody)
		}
		return
	}
	if id, ok := strings.CutPrefix(entry, AllowedFieldPrefix); ok {
		f, declared := t.Field(id)
		switch {
		case id == "" || !declared:
			r.fail(CodeUnknownField, p, "Name a declared principal field after field:",
				"transition %s: allowed entry %q must name a declared principal field", name, entry)
		case f.Type != Principal:
			r.fail(CodeWrongFieldType, p, "Name a principal field after field:",
				"transition %s: allowed entry %q must name a declared principal field", name, entry)
		}
		return
	}
	// CutPrefix returns the rest of the string and whether the prefix was there.
	who, ok := strings.CutPrefix(entry, AllowedRolePrefix)
	if !ok {
		who, ok = strings.CutPrefix(entry, AllowedPrincipalPrefix)
	}
	if !ok {
		r.fail(CodeInvalidValue, p, "Write role:<name>, principal:<id>, field:<id>, creator or nobody",
			"transition %s: allowed entry %q must be role:<name>, principal:<id>, field:<id>, creator or nobody", name, entry)
		return
	}
	if who == "" {
		r.fail(CodeInvalidValue, p, "Name the role or the principal after the colon",
			"transition %s: allowed entry %q names nothing", name, entry)
		return
	}
	// A principal entry names a typed principal ID that may act.
	if strings.HasPrefix(entry, AllowedPrincipalPrefix) &&
		!item.IsKind(who, item.KindUser, item.KindAgent, item.KindSystem, item.KindHook) {
		r.fail(CodeInvalidValue, p, "Write principal: followed by user:<id>, agent:<id>, system:<id> or hook:<type>:<hook-id>",
			"transition %s: allowed entry %q must name user:<id>, agent:<id>, system:<id> "+
				"or hook:<type>:<hook-id>", name, entry)
		return
	}
	// A hook acts only on Items of its own Type, so another Type's hook
	// could never pass this list: naming it is a mistake.
	if hookType, _, ok := item.HookParts(who); ok && strings.HasPrefix(entry, AllowedPrincipalPrefix) && hookType != t.ID {
		r.fail(CodeInvalidValue, p, "Name a hook of this Type, or remove the entry",
			"transition %s: allowed entry %q names a hook of Type %q; a hook acts only on its own Type", name, entry, hookType)
	}
}

// checkTimer checks states[i].timer, once the transitions are known.
func (t *Type) checkTimer(r reporter, i int) {
	s := &t.States[i]
	if s.Timer == nil {
		return
	}
	p := at("states", i, "timer")
	switch d, err := time.ParseDuration(s.Timer.After); {
	case s.Timer.After == "":
		r.fail(CodeRequired, at(p, "after"), "Give after as a duration, such as 4h", "state %s: timer needs `after`, such as 4h", s.ID)
	case err != nil:
		r.fail(CodeInvalidValue, at(p, "after"), "Write a duration such as 90s, 4h or 48h", "state %s: timer `after`: %v", s.ID, err)
	case d <= 0:
		r.fail(CodeInvalidValue, at(p, "after"), "Give a duration of more than zero", "state %s: timer `after` must be more than zero", s.ID)
	default:
		s.Timer.Duration = d
	}
	if s.Timer.To == "" {
		r.fail(CodeRequired, at(p, "to"), "Name the state the timer moves to", "state %s: timer needs `to`, the state to move to", s.ID)
		return
	}
	// A timer may only make a move the Type already allows from this
	// state, so it can never do what a person could not.
	j := t.transitionIndex(s.ID, s.Timer.To)
	if j < 0 {
		r.fail(CodeUnknownTransition, at(p, "to"), "Declare the transition from this state, or move to another state",
			"state %s: timer moves to %q, but there is no transition %s -> %s", s.ID, s.Timer.To, s.ID, s.Timer.To)
		return
	}
	// The timer acts as TimerPrincipal and is subject to `allowed` like
	// anyone else. A list that leaves it out would make the timer fire and
	// be forbidden every time, so the Type is refused instead.
	timer := AllowedPrincipalPrefix + TimerPrincipal
	if tr := t.Transitions[j]; len(tr.Allowed) > 0 && !slices.Contains(tr.Allowed, timer) {
		r.fail(CodeTimerNotAllowed, at("transitions", j, "allowed"), "Add principal:system:timer to the transition's allowed list",
			"state %s: its timer uses transition %s -> %s, whose `allowed` list must include %s", s.ID, s.ID, s.Timer.To, timer)
	}
}

// transitionIndex is the index of the transition from -> to, or -1.
func (t *Type) transitionIndex(from, to string) int {
	return slices.IndexFunc(t.Transitions, func(tr Transition) bool { return tr.From == from && tr.To == to })
}

// kernelFieldWritten reports whether something writes a kernel-written
// field: a hook, a reaction this Type accepts for it, an acting timer at an
// instant, or a transition's computed effect.
func (t *Type) kernelFieldWritten(id string) bool {
	if slices.ContainsFunc(t.Hooks, func(h Hook) bool { return slices.Contains(h.Writes, id) }) {
		return true
	}
	for _, a := range t.AcceptsReactions {
		// A reaction that may write it, or set it on the Items it creates.
		// Or a set reaction of a contained Item may set it.
		if slices.Contains(a.Fields, id) || slices.Contains(a.Creates, id) || slices.Contains(a.Sets, id) {
			return true
		}
	}
	for _, s := range t.States {
		for _, a := range s.Acting {
			if _, set := a.Sets[id]; set {
				return true
			}
		}
	}
	for _, tr := range t.Transitions {
		if _, set := tr.Sets[id]; set {
			return true
		}
	}
	return false
}

// isDate reports whether s is an ISO 8601 calendar date, YYYY-MM-DD, of a
// real day.
func isDate(s string) bool {
	if len(s) != len(time.DateOnly) {
		return false
	}
	d, err := time.Parse(time.DateOnly, s)
	return err == nil && d.Format(time.DateOnly) == s
}

// ParseDate reads a date field's value as a time at 00:00 UTC.
func ParseDate(v any) (time.Time, bool) {
	s, ok := v.(string)
	if !ok || !isDate(s) {
		return time.Time{}, false
	}
	d, _ := time.Parse(time.DateOnly, s)
	return d, true
}

// KernelWritten returns the fields among ids that only the kernel may
// write, sorted.
func (t *Type) KernelWritten(ids []string) []string {
	var out []string
	for _, id := range ids {
		if f, ok := t.Field(id); ok && f.WritableBy == WritableByKernel {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// AllowedFields returns the fields among ids that the transition's own
// `allowed` names through field:<id>, sorted: its patch may not write them.
func (tr Transition) AllowedFields(ids []string) []string {
	var out []string
	for _, id := range ids {
		if slices.Contains(tr.Allowed, AllowedFieldPrefix+id) {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// maxExactInt is the largest integer a JSON number (a float64) holds exactly.
const maxExactInt = 1<<53 - 1

// AsInt converts a field value to an integer. It accepts Go integers and
// whole float64 values (numbers read back from JSON are float64), and refuses
// anything outside the range JSON can carry without rounding.
func AsInt(v any) (int64, bool) {
	var n int64
	// A type switch branches on the dynamic type, like a chain of instanceof.
	switch x := v.(type) {
	case int:
		n = int64(x)
	case int32:
		n = int64(x)
	case int64:
		n = x
	case float64:
		if x != math.Trunc(x) || math.Abs(x) > maxExactInt {
			return 0, false
		}
		n = int64(x)
	case json.Number:
		i, err := x.Int64()
		if err != nil {
			return 0, false
		}
		n = i
	default:
		return 0, false
	}
	if n > maxExactInt || n < -maxExactInt {
		return 0, false
	}
	return n, true
}

// FieldError says which field value a Type refuses, and why. The reason
// names the field ID and never repeats a value, which may be personal data.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string { return e.Reason }

// CheckFields validates values against the Type's schema. A nil value means
// "clear this field" and is accepted only when allowNull is true (updates).
// The error is a *FieldError naming the first bad field.
func (t *Type) CheckFields(values map[string]any, allowNull bool) error {
	ids := make([]string, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Strings(ids) // map order is random; sort so the first error is stable

	for _, id := range ids {
		fail := func(format string, args ...any) error {
			return &FieldError{Field: id, Reason: fmt.Sprintf(format, args...)}
		}
		f, ok := t.Field(id)
		if !ok {
			return fail("field %s is not declared by type %s v%d", id, t.ID, t.Version)
		}
		v := values[id]
		if v == nil {
			if !allowNull {
				return fail("field %s: null is not a value", id)
			}
			continue
		}
		if f.Multiple {
			if reason := checkSetValue(f, v); reason != "" {
				return fail("field %s: %s", id, reason)
			}
			continue
		}
		switch f.Type {
		case String:
			if _, ok := v.(string); !ok {
				return fail("field %s: want a string", id)
			}
		case Int:
			if _, ok := AsInt(v); !ok {
				return fail("field %s: want a whole number between -(2^53-1) and 2^53-1", id)
			}
		case Bool:
			if _, ok := v.(bool); !ok {
				return fail("field %s: want true or false", id)
			}
		case Enum:
			s, ok := v.(string)
			if !ok || !slices.Contains(f.Options, s) {
				return fail("field %s: want one of %s", id, strings.Join(f.Options, ", "))
			}
		case Principal:
			if !checkPrincipalValue(v) {
				return fail("field %s: want a principal, user:<id> or agent:<id>", id)
			}
		case Date:
			if s, ok := v.(string); !ok || !isDate(s) {
				return fail("field %s: want a date, YYYY-MM-DD", id)
			}
		case DateTime:
			if _, ok := NormalizeDateTime(v); !ok {
				return fail("field %s: want an instant, RFC 3339 with an offset and at most microseconds "+
					"(2026-12-04T17:00:00Z)", id)
			}
		}
	}
	return nil
}
