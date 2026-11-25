package story

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// errNotBuilt is what every stub answers until Phase C.
var errNotBuilt = errors.New("story: not built yet")

// Story is a story file, read: the semantic model that YAML (or a witness
// written in Go) fills in.
type Story struct {
	Source      string // the file it was read from, for problems and reports
	Name        string // story:
	Author      string // author:
	Day1        time.Time
	Observer    string // a person's name; empty: the default observer
	People      []Person
	Names       []NameRule
	HideValues  map[string][]string // Type ID -> fields whose values lines leave out
	ShowSkipped bool
	Fakes       []Fake
	Given       []Step // no time: they run at day 1 00:00, in order
	Steps       []Step
	Until       time.Time // zero: a year after the last step
	Expect      []Line
}

// DefaultObserver is the person whose reads make the timeline when a story
// names none: a staff user with no roles.
const DefaultObserver = "observer"

// Person is one of the story's people.
type Person struct {
	Line     int
	Name     string
	Roles    []string
	Customer bool
	// Agent makes the person an agent (fields closed to agents by default):
	// it acts alone, or for a person of the story ("bot for ana updates
	// ..."), in a session the harness starts with every power its user's
	// kind allows. An agent is never a customer.
	Agent bool
}

// Principal is the person's principal: user:<name>, customer:<name> or
// agent:<name>.
func (p Person) Principal() string {
	switch {
	case p.Customer:
		return "customer:" + p.Name
	case p.Agent:
		return "agent:" + p.Name
	}
	return "user:" + p.Name
}

// Verb is what a step does.
type Verb string

const (
	Creates    Verb = "creates"
	Updates    Verb = "updates"
	Moves      Verb = "moves"
	Links      Verb = "links"
	Unlinks    Verb = "unlinks"
	Sees       Verb = "sees"        // sees ITEM with values
	SeesFields Verb = "sees fields" // sees ITEM fields f, f, ...
	CannotSee  Verb = "cannot see"
	Places     Verb = "places"  // places ITEM in ITEM | places ITEM at the top level
	Narrows    Verb = "narrows" // narrows ITEM to person, person, ...
	// FindsWaiting is a STUB (Phase B): finds waiting ITEM, ITEM, ... |
	// finds nothing waiting. Parse does not read it yet and Run does not
	// run it; waiting_test.go is the spec.
	FindsWaiting Verb = "finds waiting"
)

// Step is one step of the timeline (or of given).
type Step struct {
	Line  int
	At    time.Time // zero for a given step
	Text  string    // the step as written
	Actor string    // a person's name
	For   string    // the person an agent acts for ("bot for ana ..."), if any
	Verb  Verb
	Item  string

	Type    string // creates
	Version int    // creates
	In      string // creates: the container's name, if any

	To string // moves

	Top     bool     // places: at the top level (In is empty)
	Readers []string // narrows: people's names

	Link   string // links, unlinks
	Target string // links, unlinks

	Values []Assignment // with ...
	Fields []string     // sees ... fields
	Items  []string     // finds waiting: the Items' names (none: nothing waiting); a stub, as FindsWaiting

	Refused string // "-> refused REASON": the reason the write must be refused with
}

// Assignment is field = value.
type Assignment struct {
	Field string
	Value Value
}

// ValueKind is how a value was written.
type ValueKind string

const (
	Int     ValueKind = "int"
	Bool    ValueKind = "bool"
	Null    ValueKind = "null"
	Quoted  ValueKind = "quoted"  // "text"
	Word    ValueKind = "word"    // an enum option, a person, or text
	Day     ValueKind = "day"     // day N: a date
	DayTime ValueKind = "daytime" // day N HH:MM[:SS]: an instant
	List    ValueKind = "list"
)

// Value is a value as written. It takes its meaning from the field, input
// or output it is given to (Check).
type Value struct {
	Kind ValueKind
	Int  int64
	Bool bool
	Text string        // Quoted, Word
	Day  int           // Day, DayTime
	Time time.Duration // DayTime: the time of day
	List []Value
}

// NameRule names the Items the kernel creates: TYPE [in ITEM] [where f = v].
type NameRule struct {
	Line  int
	Name  string
	Type  string
	In    string      // empty: any container
	Where *Assignment // nil: any
}

// Fake is the story's fake for one action, name@major.
type Fake struct {
	Line   int
	Action string
	Rules  []Rule
}

// Rule is one rule of a fake.
type Rule struct {
	Line  int
	When  []Assignment // inputs by the contract's names
	From  time.Time    // zero: from the start
	Until time.Time    // zero: to the end
	Answer
}

// AnswerKind is what a fake answers.
type AnswerKind string

const (
	Allow   AnswerKind = "allow"
	Deny    AnswerKind = "deny"
	Correct AnswerKind = "correct"
	OK      AnswerKind = "ok"
	Fail    AnswerKind = "fail"
)

// Failures a fake can script.
const (
	FailTransient    = "transient"    // retried within the hook's budget
	FailInvalid      = "invalid"      // permanent
	FailUnauthorized = "unauthorized" // connector_unauthorized
	FailKeyConflict  = "key_conflict"
)

// Answer is a fake's answer.
type Answer struct {
	Kind    AnswerKind
	Reason  string       // deny
	Values  []Assignment // correct, ok: outputs by the contract's names
	Failure string       // fail: one of the Fail* constants
}

// Line is one line of a timeline, expected or seen.
type Line struct {
	Line int // in the story file; 0 for a line that was seen
	At   time.Time
	// To, when set, makes an expected line a window: it matches one line
	// seen at any instant from At to To, both included. Only for timing
	// the kernel does not promise (retries, budgets).
	To   time.Time
	Item string // an Item's name, or a connector's
	Text string
}

// Format writes the line as a story does: "day N HH:MM[:SS] ITEM: TEXT",
// or "between TIME and TIME ITEM: TEXT" for a window.
func (l Line) Format(day1 time.Time) string {
	if !l.To.IsZero() {
		return "between " + FormatTime(day1, l.At) + " and " + FormatTime(day1, l.To) + " " + l.Item + ": " + l.Text
	}
	return FormatTime(day1, l.At) + " " + l.Item + ": " + l.Text
}

// FormatTime writes an instant in story time: day N HH:MM, with :SS when
// the seconds are not zero.
func FormatTime(day1, at time.Time) string {
	at = at.UTC().Truncate(time.Second)
	day := int(math.Floor(at.Sub(day1).Hours()/24)) + 1 // day 0 is the day before day 1
	of := at.Sub(day1.AddDate(0, 0, day-1))
	return fmt.Sprintf("day %d %s", day, clockOf(of))
}

// Problem is something wrong with a story itself, found before it runs.
type Problem struct {
	Source  string
	Line    int
	Message string
}

func (p Problem) String() string { return fmt.Sprintf("%s:%d: %s", p.Source, p.Line, p.Message) }

// Problems is every problem found, as an error.
type Problems []Problem

func (ps Problems) Error() string {
	lines := make([]string, len(ps))
	for i, p := range ps {
		lines[i] = p.String()
	}
	return strings.Join(lines, "\n")
}
