// Package reading renders a Type as plain-language sentences a person can
// approve (Ventoux item 6): every state, move, hook, reaction, timer, hold
// and visibility rule of the Type, one sentence each, from the Type and the
// action contracts it pins, nothing else.
//
// Each sentence has a kind and the clauses it uses (its Parts), and the
// paths in the Type it reads (transitions[1], states[2].timer). Every part
// has a witness: a test that runs the engine through internal/story's Run
// and shows it does what the sentence says (witness_test.go). The list of
// parts is closed (Parts); a test holds the witnesses to it, another holds
// every key of the Type format to a part or to a reason it is left out
// (KeyParts, LeftOut), and a third requires every element of every Type in
// types/ to be cited by a sentence.
//
// Read renders a Type (render.go); Text lays it out for a person;
// Condition reads one expression (expr.go). The wording of every kind is
// in docs/design.md, "Ventoux V6, as built".
package reading

// Part is a kind of sentence ("move"), or a clause a sentence of that kind
// may carry ("move.guard"). A named string type keeps parts apart from
// plain strings, like a Java enum.
type Part string

// The sentence kinds and their clauses. The wording of each is in Parts'
// comments and docs/design.md, "Ventoux V6, as built".
const (
	// In short.
	Starts      Part = "starts"       // A {type} starts in {state}.
	Closed      Part = "closed"       // It moves only as listed below; any other move is refused.
	NoLifecycle Part = "no_lifecycle" // A {type} has no lifecycle: it stays in `default`.
	Invariant   Part = "invariant"    // Every write keeps this true: {rule}; a write that would not is refused ({id}).

	// Who writes what.
	FieldKernel Part = "field_kernel" // Only the kernel writes {f}: {writers}.
	FieldRoles  Part = "field_roles"  // Only holders of {roles} write {f}.
	FieldUnique Part = "field_unique" // No two {type}s in one container, both in {states}, hold the same {f}.
	// Fields closed to agents by default (who writes what).
	FieldAgentsOnly      Part = "field_agents_only"      // Only agents[ and holders of {roles}] write {f}.
	FieldAgents          Part = "field_agents"           // No agent writes its fields / An agent writes only {fields}
	FieldAgentsRoles     Part = "field_agents.roles"     // , except one that names a role the agent holds / , and a field that names a role the agent holds
	FieldAgentsCustomers Part = "field_agents.customers" // ; an agent acting for a customer sets only what the customer may

	// Links.
	Link           Part = "link"             // {Who} links it to {targets} as {name}[clauses]; a {target} sees it as {inverse}.
	LinkOne        Part = "link.one"         // , at most one
	LinkInverseOne Part = "link.inverse_one" // ; a {target} takes at most one
	LinkKernel     Part = "link.kernel"      // Only the kernel links it ...
	LinkRoles      Part = "link.roles"       // Only holders of {roles} link it ...
	Incoming       Part = "incoming"         // It reads the {types} linked to it as {link} under the name {name}.
	Rollup         Part = "rollup"           // When it is read, {id} counts its {over} by {count_by}.

	// A state: what entering it does, what happens while it is there.
	Waits         Part = "waits"          // While it is in {s}, it waits on {who}.
	WaitsClaimant Part = "waits.claimant" // While it is in {s}, it waits on whoever claimed it.
	Claim         Part = "claim"          // Whoever moves it into {s} claims it: {f} names them, and only they move it on from there, unless a move releases the claim.
	Frozen        Part = "frozen"         // While it is in {s}, nobody changes {fields}.
	RequesterOnly Part = "requester_only" // While it is in {s}, only the requester changes {fields}.
	Timer         Part = "timer"          // {d} after it enters {s}, if it is still there, the timer moves it to {to}.
	TimerWaits    Part = "timer.waits"    // ; if a hold is open then, the timer waits for it to close and looks again
	AtTimer       Part = "at_timer"       // When the instant in {f} passes while it is in {s}, the timer moves it to {to}.
	Acting        Part = "acting"         // {d} after it enters {s}, {hook} calls {action} with {inputs}, at most {n} times while it stays there.
	ActingEvery   Part = "acting.every"   // , then every {e}
	ActingAt      Part = "acting_at"      // When the instant in {f} passes while it is in {s}, {id} ... and {hook} calls {action} with {inputs}.
	ActingAtWhen  Part = "acting_at.when" // , if {when}
	ActingAtSets  Part = "acting_at.sets" // sets {f} to {value}
	Enrich        Part = "enrich"         // On entering {s}, {hook} calls {action} with {inputs} and writes its answer into {fields}.
	EnrichFallbk  Part = "enrich.fallback"
	Action        Part = "action" // On entering {s}, {hook} calls {action} with {inputs}, once per entry, even if it has moved on.
	ActionWrites  Part = "action.writes"
	Resolve       Part = "resolve" // On entering {s}, the kernel links {link} to the requester's {state} {target} ...
	Final         Part = "final"   // Nothing leaves {s}.
	ReactionFrom  Part = "reaction.from"
	ReactionWhen  Part = "reaction.when"
	ReactionSkip  Part = "reaction.skip"
	ReactionHook  Part = "reaction_hook" // When {reaction} has applied, {hook} calls {action} with {inputs}.

	// A move, and who may make it.
	Move         Part = "move" // {Who} moves it from {a} to {b}[clauses].
	MoveRequired Part = "move.required"
	MoveGiven    Part = "move.given"
	MoveGuard    Part = "move.guard"
	MoveEscape   Part = "move.escape"
	MoveClears   Part = "move.clears"
	MoveRelease  Part = "move.release" // , which releases the claim
	MoveSets     Part = "move.sets"

	WhoStaff              Part = "who.staff"     // Any staff member who can see it
	WhoCreator            Part = "who.creator"   // The requester
	WhoRole               Part = "who.role"      // Anyone with the role {r}
	WhoField              Part = "who.field"     // Whoever {f} names
	WhoPrincipal          Part = "who.principal" // {principal}
	WhoTimer              Part = "who.timer"     // Only the timer
	WhoHook               Part = "who.hook"      // Only the hook {h} may move it ...
	WhoNobody             Part = "who.nobody"    // No one moves it ... by hand
	ExceptCreator         Part = "except.creator"
	ExceptContainerCreatr Part = "except.container_creator"

	// What a move waits for.
	Check        Part = "check" // Asking for this move opens a hold instead: {hook} calls {action} ...
	CheckCorrect Part = "check.correct"
	Held         Part = "held" // Asking for this move opens a hold instead: {id} adds {expr} to {f} on the {target} ...
	Hold         Part = "hold" // While that hold is open, ...
	HoldFrozen   Part = "hold.frozen"
	HoldEscape   Part = "hold.escape" // , and the only move out of {a} is to {escapes}
	HoldNone     Part = "hold.none"   // , and nothing moves it
	Undo         Part = "undo"        // On entering {s} from {from}, {id} gives back what {held} added.

	// Reactions that move, create or set.
	MoveLink              Part = "move_link"      // ..., {id} moves the {target} it links to as {link} from {states} to {to}.
	MoveContainer         Part = "move_container" // ..., {id} moves its container from {states} to {to}.
	MoveChildren          Part = "move_children"  // ..., {id} moves the {types} inside it from {states} to {to}.
	MoveChildrenWhen      Part = "move_children.when"
	CreatedInsideSelf     Part = "created_inside.self"     // When ... creates a {type} inside it[ while it is in {from}], {id} moves it to {to}.
	CreatedInsideChildren Part = "created_inside.children" // ..., {id} moves the {types} inside it ...
	CreatedByCreator      Part = "created_inside.by_creator"
	CreatedByOther        Part = "created_inside.by_other"
	CreatedByAnyone       Part = "created_inside.by_anyone"
	Create                Part = "create" // ..., {id} creates a {type}@{v} inside it, with {values}.
	CreateForEach         Part = "create.for_each"
	Set                   Part = "set" // ..., {id} sets {f} on its container to {value}.

	// What other Types may do to it.
	ConsentAdd      Part = "consent.add"
	ConsentSet      Part = "consent.set"
	ConsentCreate   Part = "consent.create"
	ConsentMove     Part = "consent.move"
	ConsentWildcard Part = "consent.wildcard"
	Readable        Part = "readable" // Another Type's create reaction may read {fields} from it.
	Offer           Part = "offer"    // Items inside it may read {f} as {offer}.

	// Who sees it.
	CustomerFields     Part = "customer_fields" // A customer who sees it sees only {fields}, and sets only those when creating it.
	CustomerFieldsNone Part = "customer_fields.none"
	Own                Part = "own"    // A customer may create {types} inside it, and sees those they requested.
	Shared             Part = "shared" // The customer who requested it sees every {type} inside it, and may create them there.
	PlacementAnyone    Part = "placement.anyone"
	PlacementRoles     Part = "placement.roles"
	PlacementKernel    Part = "placement.kernel"

	// Who starts it (starting work, step 2): the latest version's start
	// block, which judges a create of every version.
	StartIn                  Part = "start_in"                    // A {type} is created only inside {types}.
	StartInTop               Part = "start_in.top"                // , or at the top level (or: only at the top level)
	StartWho                 Part = "start_who"                   // {Who} creates one[; clauses].
	StartWhoField            Part = "start_who.field"             // whoever {f} of the container names
	StartWhoContainerCreator Part = "start_who.container_creator" // anyone who took part in creating the container
	StartWhoNobody           Part = "start_who.nobody"            // No person creates a {type}: only a reaction does.
	StartWhoCustomers        Part = "start_who.customers"         // ; a customer creates one only where a container lets them
	StartLatest              Part = "start_latest"                // While this is the latest version of {type}, these rules apply to creating a {type} of any version.
	StartOlder               Part = "start_older"                 // While this is the latest version of {type}, a {type} of version {n} may still be created; one of any other older version may not. (Not built yet.)
	StartOlderAll            Part = "start_older.all"             // ... a {type} of any older version may still be created. (Not built yet.)
)

// Parts is the closed list of parts, in the order of the constants.
func Parts() []Part {
	return []Part{
		Starts, Closed, NoLifecycle, Invariant,
		FieldKernel, FieldRoles, FieldUnique, FieldAgentsOnly, FieldAgents, FieldAgentsRoles, FieldAgentsCustomers,
		Link, LinkOne, LinkInverseOne, LinkKernel, LinkRoles, Incoming, Rollup,
		Claim, Waits, WaitsClaimant, Frozen, RequesterOnly, Timer, TimerWaits, AtTimer, Acting, ActingEvery, ActingAt, ActingAtWhen, ActingAtSets,
		Enrich, EnrichFallbk, Action, ActionWrites, Resolve, Final, ReactionFrom, ReactionWhen, ReactionSkip, ReactionHook,
		Move, MoveRequired, MoveGiven, MoveGuard, MoveEscape, MoveRelease, MoveClears, MoveSets,
		WhoStaff, WhoCreator, WhoRole, WhoField, WhoPrincipal, WhoTimer, WhoHook, WhoNobody, ExceptCreator, ExceptContainerCreatr,
		Check, CheckCorrect, Held, Hold, HoldFrozen, HoldEscape, HoldNone, Undo,
		MoveLink, MoveContainer, MoveChildren, MoveChildrenWhen,
		CreatedInsideSelf, CreatedInsideChildren, CreatedByCreator, CreatedByOther, CreatedByAnyone,
		Create, CreateForEach, Set,
		ConsentAdd, ConsentSet, ConsentCreate, ConsentMove, ConsentWildcard, Readable, Offer,
		CustomerFields, CustomerFieldsNone, Own, Shared, PlacementAnyone, PlacementRoles, PlacementKernel,
		StartIn, StartInTop, StartWho, StartWhoField, StartWhoContainerCreator, StartWhoNobody, StartWhoCustomers, StartLatest,
	}
}

// Reading is a Type in plain language.
type Reading struct {
	Type       string   // id@version
	Key        []Term   // the terms its sentences use, defined once
	Fields     []string // the field list: "`f_start`: a date"
	Sections   []Section
	Actions    []ActionNote // the actions it calls, from their contracts
	NotCovered []string     // what no reading of a Type can say
}

// Term is one entry of the key at the top of a reading.
type Term struct {
	Name    string // "the requester"
	Meaning string
}

// Section is a heading and its sentences: "In short", a state ("draft (to
// do)"), "Links", "Who sees it", ...
type Section struct {
	Title     string
	Sentences []Sentence
}

// Sentence is one claim about the Type.
type Sentence struct {
	N     int    // its number in the reading, from 1
	Kind  Part   // its kind: a part without a clause
	Parts []Part // its kind, then the clauses it uses
	Text  string
	Paths []string // the elements of the Type it reads: transitions[1], states[2].timer
}

// ActionNote is one action a Type calls, as its contract describes it.
type ActionNote struct {
	Action      string // hr.manager_of@1
	Kinds       []string
	SideEffects bool
	Risk        string
	Description string
}
