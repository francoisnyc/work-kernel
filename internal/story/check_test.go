// Tests for Check: a story is held against the Types and the action
// contracts before it runs. A fake cannot answer what its contract does not
// allow; every action a story's Types may call has a fake; every person,
// Item, Type, field, state and value is one the story and its Types know.
// The stories here are built in Go, as item 6's witnesses will build them.
package story_test

import (
	"strings"
	"sync"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/story"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

var (
	typesOnce sync.Once
	allTypes  []*typedef.Type
	typesErr  error
)

// repoTypes are the Types of types/, read as the item service reads them.
func repoTypes(t *testing.T) []*typedef.Type {
	t.Helper()
	typesOnce.Do(func() { allTypes, typesErr = typedef.ParseDir("../../types") })
	if typesErr != nil {
		t.Fatalf("read types/: %v", typesErr)
	}
	return allTypes
}

// approveStory is stories/vacation/approve.yaml, built in Go: a sound story.
func approveStory() *story.Story {
	return &story.Story{
		Source: "approve.go", Name: "an approved request", Author: "a test", Day1: day1,
		People: []story.Person{{Line: 1, Name: "alice"}, {Line: 2, Name: "bob"}, {Line: 3, Name: "hana", Roles: []string{"hr_admin"}}},
		Fakes: []story.Fake{
			{Line: 4, Action: "hr.manager_of@1", Rules: []story.Rule{{Line: 5,
				When:   []story.Assignment{set("createdBy", word("alice"))},
				Answer: story.Answer{Kind: story.Correct, Values: []story.Assignment{set("manager", word("bob"))}}}}},
			{Line: 6, Action: "email.notify@1", Rules: []story.Rule{{Line: 7, Answer: story.Answer{Kind: story.OK}}}},
		},
		Given: []story.Step{{Line: 8, Actor: "hana", Verb: story.Creates, Item: "balance", Type: "leave_balance", Version: 1,
			Values: []story.Assignment{set("f_owner", word("alice")), set("f_allowance", integer(20))}}},
		Steps: []story.Step{
			{Line: 10, At: at(1, 9, 0, 0), Actor: "alice", Verb: story.Creates, Item: "request", Type: "vacation_request", Version: 3,
				Values: []story.Assignment{set("f_start", dayValue(15)), set("f_end", dayValue(19)), set("f_days", integer(5)),
					set("f_leave_type", word("annual"))}},
			{Line: 11, At: at(1, 9, 5, 0), Actor: "alice", Verb: story.Moves, Item: "request", To: "submitted"},
			{Line: 12, At: at(2, 9, 30, 0), Actor: "alice", Verb: story.Moves, Item: "request", To: "approved", Refused: "NOT_ALLOWED"},
			{Line: 13, At: at(2, 10, 0, 0), Actor: "bob", Verb: story.Moves, Item: "request", To: "approved"},
			{Line: 14, At: at(2, 10, 0, 0), Actor: "hana", Verb: story.Sees, Item: "balance",
				Values: []story.Assignment{set("f_used", integer(5))}},
		},
		Until:  at(10, 0, 0, 0),
		Expect: []story.Line{{Line: 16, At: at(1, 9, 0, 0), Item: "request", Text: "alice created it in draft"}},
	}
}

// deskStory: a portal and an incident in it, with the email fake the
// incident's goals need.
func deskStory() *story.Story {
	return &story.Story{
		Source: "desk.go", Name: "a desk", Author: "a test", Day1: day1,
		People: []story.Person{{Line: 1, Name: "lea", Roles: []string{"sla_admin"}}, {Line: 2, Name: "kim", Customer: true}},
		Names: []story.NameRule{{Line: 3, Name: "first", Type: "sla_goal", In: "inc",
			Where: &story.Assignment{Field: "f_metric", Value: word("first_response")}}},
		HideValues: map[string][]string{"sla_goal": {"f_week", "f_zone"}},
		Fakes:      []story.Fake{{Line: 4, Action: "email.notify@1", Rules: []story.Rule{{Line: 5, Answer: story.Answer{Kind: story.OK}}}}},
		Given: []story.Step{
			{Line: 20, Actor: "lea", Verb: story.Creates, Item: "cal", Type: "calendar", Version: 1,
				Values: []story.Assignment{set("f_name", quoted("office hours")), set("f_zone", quoted("UTC")),
					set("f_week", story.Value{Kind: story.List, List: []story.Value{quoted("mon 09:00-17:00"), quoted("tue 09:00-17:00"),
						quoted("wed 09:00-17:00"), quoted("thu 09:00-17:00"), quoted("fri 09:00-17:00")}})}},
			{Line: 21, Actor: "lea", Verb: story.Creates, Item: "policy", Type: "sla_policy", Version: 2,
				Values: []story.Assignment{set("f_name", quoted("standard")),
					set("f_first_response_s_p1", integer(3600)), set("f_first_response_s_p2", integer(7200)),
					set("f_first_response_s_p3", integer(14400)), set("f_first_response_s_p4", integer(28800)),
					set("f_resolution_s_p1", integer(14400)), set("f_resolution_s_p2", integer(28800)),
					set("f_resolution_s_p3", integer(57600)), set("f_resolution_s_p4", integer(115200))}},
			{Line: 22, Actor: "lea", Verb: story.Creates, Item: "portal", Type: "desk", Version: 2,
				Values: []story.Assignment{set("f_key", quoted("IT")), set("f_name", quoted("IT portal"))}},
			{Line: 23, Actor: "lea", Verb: story.Links, Item: "portal", Link: "calendar", Target: "cal"},
			{Line: 24, Actor: "lea", Verb: story.Links, Item: "portal", Link: "sla_policy", Target: "policy"},
		},
		Steps: []story.Step{
			{Line: 8, At: at(1, 10, 0, 0), Actor: "kim", Verb: story.Creates, Item: "inc", Type: "incident", Version: 3, In: "portal",
				Values: []story.Assignment{set("f_title", quoted("down")), set("f_priority", word("P1"))}},
			{Line: 9, At: at(1, 10, 0, 0), Actor: "kim", Verb: story.CannotSee, Item: "first"},
			{Line: 10, At: at(1, 10, 0, 0), Actor: "kim", Verb: story.SeesFields, Item: "inc", Fields: []string{"f_priority", "f_title"}},
		},
	}
}

func check(t *testing.T, s *story.Story) story.Problems {
	t.Helper()
	return story.Check(s, repoTypes(t), contracts.Default())
}

func TestCheck_SoundStoriesHaveNoProblems(t *testing.T) {
	for name, s := range map[string]*story.Story{"approve": approveStory(), "desk": deskStory()} {
		t.Run(name, func(t *testing.T) {
			if ps := check(t, s); len(ps) != 0 {
				t.Errorf("problems = %v, want none", ps)
			}
		})
	}
}

// wantOne checks that ps holds exactly one problem, on line, naming every
// one of words, and that it names the story's source.
func wantOne(t *testing.T, ps story.Problems, source string, line int, words ...string) {
	t.Helper()
	if len(ps) != 1 {
		t.Fatalf("problems = %v, want exactly one", ps)
	}
	p := ps[0]
	if p.Source != source || p.Line != line {
		t.Errorf("problem at %s:%d, want %s:%d: %v", p.Source, p.Line, source, line, p)
	}
	for _, w := range words {
		if !strings.Contains(p.Message, w) {
			t.Errorf("message %q does not say %q", p.Message, w)
		}
	}
}

func TestCheck_Problems(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *story.Story)
		line   int
		words  []string
	}{
		// People.
		{"an unknown person", func(s *story.Story) { s.Steps[3].Actor = "carol" }, 13, []string{"carol"}},
		{"a value naming an unknown person", func(s *story.Story) { s.Given[0].Values[0] = set("f_owner", word("zed")) }, 8,
			[]string{"zed"}},
		{"a customer holding a role", func(s *story.Story) {
			s.People = append(s.People, story.Person{Line: 30, Name: "kim", Customer: true, Roles: []string{"hr_admin"}})
		}, 30, []string{"kim", "customer"}},
		{"an unknown observer", func(s *story.Story) { s.Observer = "olga" }, 0, []string{"olga"}},
		{"a person twice", func(s *story.Story) { s.People = append(s.People, story.Person{Line: 31, Name: "bob"}) }, 31,
			[]string{"bob"}},
		// Items and Types.
		{"an unknown Type version", func(s *story.Story) { s.Steps[0].Version = 9 }, 10, []string{"vacation_request@9"}},
		{"an Item used before it is created", func(s *story.Story) { s.Steps[1].Item = "other" }, 11, []string{"other"}},
		{"an Item created twice", func(s *story.Story) {
			s.Steps = append(s.Steps, story.Step{Line: 15, At: at(3, 9, 0, 0), Actor: "alice", Verb: story.Creates, Item: "request",
				Type: "vacation_request", Version: 3})
		}, 15, []string{"request"}},
		{"an unknown container", func(s *story.Story) { s.Steps[0].In = "nowhere" }, 10, []string{"nowhere"}},
		// Fields, states, links, values.
		{"an unknown field", func(s *story.Story) { s.Steps[0].Values = append(s.Steps[0].Values, set("f_nope", integer(1))) }, 10,
			[]string{"f_nope"}},
		{"a value of the wrong type", func(s *story.Story) { s.Steps[0].Values[2] = set("f_days", word("many")) }, 10,
			[]string{"f_days"}},
		{"an option the enum lacks", func(s *story.Story) { s.Steps[0].Values[3] = set("f_leave_type", word("sabbatical")) }, 10,
			[]string{"f_leave_type", "sabbatical"}},
		{"a date that is an instant", func(s *story.Story) {
			s.Steps[0].Values[0] = set("f_start", story.Value{Kind: story.DayTime, Day: 15, Time: 9 * 60 * 60 * 1e9})
		}, 10, []string{"f_start"}},
		{"an unknown state", func(s *story.Story) { s.Steps[3].To = "approvedd" }, 13, []string{"approvedd"}},
		{"an unknown link", func(s *story.Story) {
			s.Steps = append(s.Steps, story.Step{Line: 15, At: at(3, 9, 0, 0), Actor: "hana", Verb: story.Links, Item: "request",
				Link: "owner", Target: "balance"})
		}, 15, []string{"owner"}},
		{"a read of an unknown field", func(s *story.Story) { s.Steps[4].Values = []story.Assignment{set("f_spent", integer(5))} }, 14,
			[]string{"f_spent"}},
		{"an unknown refusal", func(s *story.Story) { s.Steps[2].Refused = "NOPE" }, 12, []string{"NOPE"}},
		{"steps out of order", func(s *story.Story) { s.Steps[1].At = at(1, 8, 0, 0) }, 11, []string{"order"}},
		{"until before the last step", func(s *story.Story) { s.Until = at(2, 9, 0, 0) }, 0, []string{"until"}},
		// Fakes and contracts.
		{"an unknown action", func(s *story.Story) { s.Fakes[0].Action = "hr.manager_of@2" }, 4, []string{"hr.manager_of@2"}},
		{"an answer the kind does not take", func(s *story.Story) { s.Fakes[0].Rules[0].Answer = story.Answer{Kind: story.OK} }, 5,
			[]string{"ok", "check"}},
		{"an undeclared reason", func(s *story.Story) {
			s.Fakes[0].Rules[0].Answer = story.Answer{Kind: story.Deny, Reason: "on_vacation"}
		}, 5, []string{"on_vacation"}},
		{"an unknown output", func(s *story.Story) {
			s.Fakes[0].Rules[0].Answer.Values = []story.Assignment{set("boss", word("bob"))}
		}, 5, []string{"boss"}},
		{"an output of the wrong type", func(s *story.Story) {
			s.Fakes[0].Rules[0].Answer.Values = []story.Assignment{set("manager", integer(5))}
		}, 5, []string{"manager"}},
		{"a correction missing an output", func(s *story.Story) { s.Fakes[0].Rules[0].Answer.Values = nil }, 5, []string{"manager"}},
		{"an unknown input", func(s *story.Story) { s.Fakes[0].Rules[0].When = []story.Assignment{set("employee", word("alice"))} }, 5,
			[]string{"employee"}},
		{"an input of the wrong type", func(s *story.Story) { s.Fakes[0].Rules[0].When = []story.Assignment{set("createdBy", integer(5))} },
			5, []string{"createdBy"}},
		{"an action with an output it does not have", func(s *story.Story) {
			s.Fakes[1].Rules[0].Answer.Values = []story.Assignment{set("messageId", quoted("m-1"))}
		}, 7, []string{"messageId"}},
		{"an action answering allow", func(s *story.Story) { s.Fakes[1].Rules[0].Answer = story.Answer{Kind: story.Allow} }, 7,
			[]string{"allow", "action"}},
		{"a window that ends before it starts", func(s *story.Story) {
			s.Fakes[0].Rules[0].From, s.Fakes[0].Rules[0].Until = at(2, 0, 0, 0), at(1, 0, 0, 0)
		}, 5, []string{"until"}},
		{"a fake listed twice", func(s *story.Story) {
			s.Fakes = append(s.Fakes, story.Fake{Line: 40, Action: "email.notify@1",
				Rules: []story.Rule{{Line: 41, Answer: story.Answer{Kind: story.OK}}}})
		}, 40, []string{"email.notify@1"}},
		{"a fake without rules", func(s *story.Story) { s.Fakes[1].Rules = nil }, 6, []string{"email.notify@1", "rule"}},
		{"a missing fake", func(s *story.Story) { s.Fakes = s.Fakes[:1] }, 0, []string{"email.notify@1", "vacation_request@3"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := approveStory()
			c.mutate(s)
			wantOne(t, check(t, s), "approve.go", c.line, c.words...)
		})
	}
}

// The Types a story's Items create by reaction count: incident@3 makes
// sla_goal@3 goals, whose breach hook emails.
func TestCheck_AFakeForWhatReactionsCreate(t *testing.T) {
	s := deskStory()
	s.Fakes = nil
	wantOne(t, check(t, s), "desk.go", 0, "email.notify@1", "sla_goal@3")
}

func TestCheck_NamesAndHiddenValues(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(s *story.Story)
		line   int
		words  []string
	}{
		{"a name rule of an unknown Type", func(s *story.Story) { s.Names[0].Type = "sla_gaol" }, 3, []string{"sla_gaol"}},
		{"a name rule on an unknown field", func(s *story.Story) { s.Names[0].Where.Field = "f_kind" }, 3, []string{"f_kind"}},
		{"a name rule in an unknown Item", func(s *story.Story) { s.Names[0].In = "nowhere" }, 3, []string{"nowhere"}},
		// The read that named the rule's Item reads inc instead, so the
		// clash is the only problem (agreed in Phase C).
		{"a name a step also takes", func(s *story.Story) { s.Names[0].Name = "inc"; s.Steps[1].Item = "inc" }, 3, []string{"inc"}},
		{"a hidden field the Type lacks", func(s *story.Story) { s.HideValues["sla_goal"] = []string{"f_wek"} }, 0, []string{"f_wek"}},
		{"a hidden field of an unknown Type", func(s *story.Story) { s.HideValues["goal"] = []string{"f_week"} }, 0, []string{"goal"}},
		{"a read of a field the Type lacks", func(s *story.Story) { s.Steps[2].Fields = []string{"f_priority", "f_name"} }, 10,
			[]string{"f_name"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := deskStory()
			c.mutate(s)
			wantOne(t, check(t, s), "desk.go", c.line, c.words...)
		})
	}
}

// A customer holds no roles, and only user principals do: a person named
// for a principal field may be either.
func TestCheck_ACustomerAsAPrincipal(t *testing.T) {
	s := deskStory()
	s.Steps = append(s.Steps, story.Step{Line: 11, At: at(1, 11, 0, 0), Actor: "lea", Verb: story.Updates, Item: "inc",
		Values: []story.Assignment{set("f_assignee", word("kim"))}})
	if ps := check(t, s); len(ps) != 0 {
		t.Errorf("problems = %v: a principal field takes a customer (the kernel decides who may hold it)", ps)
	}
}

// Every story in stories/ checks clean against types/ and the contracts.
func TestCheck_TheStories(t *testing.T) {
	for _, f := range storyFiles(t) {
		t.Run(f.name, func(t *testing.T) {
			s, err := story.Parse(f.path, f.data)
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if ps := check(t, s); len(ps) != 0 {
				t.Errorf("%v", ps)
			}
		})
	}
}
