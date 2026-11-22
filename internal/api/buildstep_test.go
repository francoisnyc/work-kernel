// Tests for build_step@1 (step 1 of docs/plans/autonomous-loop.md), through
// the public API: the way this repository is built, as a Type
// (docs/plans/build-step-type.md, with the owner's decisions of 2026-11-22).
//
//   - Only a maintainer creates a step: f_owner is writable by
//     role:maintainer alone, and the invariant owner_missing refuses a step
//     without one. An agent holds no role, so it can neither name an owner
//     nor create a step at all.
//   - Every move whose allowed list is [field:f_owner] is the owner's: an
//     agent acting for the owner is NOT_ALLOWED (a field: entry never
//     matches an agent), and so is another user. The agent makes the other
//     moves.
//   - Required fields on their moves (MISSING_FIELDS, metadata fields).
//   - Frozen fields per state (FIELD_FROZEN, metadata fields and state), and
//     the same fields writable where they are not frozen.
//   - GetType shows the Type as declared.
package api_test

import (
	"slices"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
)

const buildStepV1 = "namespaces/acme/types/build_step/versions/1"

// buildStepLoaded stops the test at once, with a clear message, while the
// Type is not loaded.
func buildStepLoaded(t *testing.T) *kernelv1.Type {
	t.Helper()
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: buildStepV1}))
	if err != nil {
		t.Fatalf("build_step@1 is not loaded: GetType %s: %v", buildStepV1, err)
	}
	return res.Msg.Type
}

// grantMaintainer gives ana the maintainer role in acme. bob never has it.
func grantMaintainer(t *testing.T) {
	t.Helper()
	if _, err := pool.Exec(testCtx(t), `insert into principal_roles (namespace_id, principal, role)
		values ('acme', 'user:ana', 'maintainer') on conflict do nothing`); err != nil {
		t.Fatalf("grant maintainer to ana: %v", err)
	}
}

// botForAna is the agent working for ana, the owner, with every power her
// grant allows that a step needs.
func botForAna(t *testing.T, powers ...string) kernelv1connect.ItemServiceClient {
	t.Helper()
	if len(powers) == 0 {
		powers = []string{"read", "create", "edit_fields", "transition:*"}
	}
	return asAgent("bot", startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, powers...))
}

func createStep(t *testing.T, c kernelv1connect.ItemServiceClient, f map[string]any) (*kernelv1.CreateItemResponse, error) {
	t.Helper()
	res, err := c.CreateItem(testCtx(t), connect.NewRequest(&kernelv1.CreateItemRequest{
		Parent: "namespaces/acme", RequestId: newID(), Type: buildStepV1, Fields: fields(t, f),
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// newStep: ana, a maintainer, creates a step she owns, in questions.
func newStep(t *testing.T) string {
	t.Helper()
	grantMaintainer(t)
	res, err := createStep(t, as("ana"), map[string]any{"f_title": "Step " + word(), "f_owner": "user:ana"})
	if err != nil {
		t.Fatalf("ana creates a build_step: %v", err)
	}
	return res.Item.Name
}

func stepMove(t *testing.T, c kernelv1connect.ItemServiceClient, name, to string, f map[string]any) (*kernelv1.TransitionItemResponse, error) {
	t.Helper()
	res, err := c.TransitionItem(testCtx(t), connect.NewRequest(&kernelv1.TransitionItemRequest{
		Name: name, RequestId: newID(), ToState: to, Fields: fields(t, f),
	}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

// mustStepMove makes a move that must apply, and checks it landed in to.
func mustStepMove(t *testing.T, who string, c kernelv1connect.ItemServiceClient, name, to string, f map[string]any) {
	t.Helper()
	res, err := stepMove(t, c, name, to, f)
	if err != nil {
		t.Fatalf("%s moves the step to %s: %v", who, to, err)
	}
	if res.Outcome != kernelv1.TransitionOutcome_TRANSITION_OUTCOME_APPLIED || res.Item.GetState() != to {
		t.Fatalf("%s moves the step to %s = %v, want APPLIED into %s", who, to, res, to)
	}
}

func stepUpdate(t *testing.T, c kernelv1connect.ItemServiceClient, name string, f map[string]any) error {
	t.Helper()
	_, err := c.UpdateItem(testCtx(t), connect.NewRequest(&kernelv1.UpdateItemRequest{
		Name: name, RequestId: newID(), Fields: fields(t, f),
	}))
	return err
}

// The happy path, in order, with the field each move needs.
var stepPath = []struct {
	to string
	f  map[string]any
}{
	{"answering", map[string]any{"f_questions": "docs/questions.md#step-1"}},
	{"writing_tests", nil},
	{"tests_proposed", map[string]any{"f_tests_ref": "internal/api/x_test.go@abc1234"}},
	{"building", nil},
	{"in_review", map[string]any{"f_pr_url": "https://example.test/pull/1"}},
	{"done", map[string]any{"f_journal": "docs/journal.md#step-1"}},
}

// stepIn makes a new step and ana, its owner, walks it into state.
func stepIn(t *testing.T, state string) string {
	t.Helper()
	name := newStep(t)
	if state == "questions" {
		return name
	}
	if state == "dropped" {
		mustStepMove(t, "ana", as("ana"), name, "dropped", nil)
		return name
	}
	for _, m := range stepPath {
		mustStepMove(t, "ana", as("ana"), name, m.to, m.f)
		if m.to == state {
			return name
		}
	}
	t.Fatalf("no path to state %q", state)
	return ""
}

// --- GetType ---

func TestBuildStep_GetType(t *testing.T) {
	ty := buildStepLoaded(t)
	if ty.InitialState != "questions" {
		t.Errorf("initial_state = %q, want questions", ty.InitialState)
	}
	var owner *kernelv1.Field
	for _, f := range ty.Fields {
		if f.Id == "f_owner" {
			owner = f
		}
	}
	if owner == nil || !slices.Equal(owner.WritableByRoles, []string{"maintainer"}) ||
		owner.WritableBy != kernelv1.FieldWriter_FIELD_WRITER_UNSPECIFIED {
		t.Errorf("f_owner = %v, want writable by role maintainer only", owner)
	}

	wantFrozen := map[string][]string{
		"questions":      nil,
		"answering":      nil,
		"writing_tests":  {"f_questions"},
		"tests_proposed": {"f_questions", "f_tests_ref"},
		"building":       {"f_owner", "f_questions", "f_tests_ref"},
		"in_review":      {"f_owner", "f_pr_url", "f_questions", "f_tests_ref"},
		"done":           {"f_journal", "f_owner", "f_pr_url", "f_questions", "f_tests_ref"},
		"dropped":        nil,
	}
	seen := map[string]bool{}
	for _, s := range ty.States {
		seen[s.Id] = true
		want, ok := wantFrozen[s.Id]
		if !ok {
			t.Errorf("unexpected state %s", s.Id)
			continue
		}
		got := slices.Sorted(slices.Values(s.Frozen))
		if !slices.Equal(got, want) {
			t.Errorf("state %s frozen = %v, want %v", s.Id, got, want)
		}
	}
	for id := range wantFrozen {
		if !seen[id] {
			t.Errorf("state %s is missing", id)
		}
	}

	owners := []string{"field:f_owner"}
	type move struct{ from, to string }
	type rule struct{ allowed, required []string }
	want := map[move]rule{
		{"questions", "answering"}:          {nil, []string{"f_questions"}},
		{"answering", "writing_tests"}:      {owners, nil},
		{"writing_tests", "tests_proposed"}: {nil, []string{"f_tests_ref"}},
		{"tests_proposed", "building"}:      {owners, nil},
		{"tests_proposed", "writing_tests"}: {owners, nil},
		{"building", "writing_tests"}:       {nil, nil},
		{"building", "in_review"}:           {nil, []string{"f_pr_url"}},
		{"in_review", "building"}:           {owners, nil},
		{"in_review", "done"}:               {owners, []string{"f_journal"}},
		{"questions", "dropped"}:            {owners, nil},
		{"answering", "dropped"}:            {owners, nil},
		{"writing_tests", "dropped"}:        {owners, nil},
		{"tests_proposed", "dropped"}:       {owners, nil},
		{"building", "dropped"}:             {owners, nil},
		{"in_review", "dropped"}:            {owners, nil},
		{"dropped", "questions"}:            {owners, nil},
	}
	if len(ty.Transitions) != len(want) {
		t.Errorf("%d transitions, want %d", len(ty.Transitions), len(want))
	}
	drops := 0
	for _, tr := range ty.Transitions {
		w, ok := want[move{tr.FromState, tr.ToState}]
		if !ok {
			t.Errorf("unexpected transition %s -> %s", tr.FromState, tr.ToState)
			continue
		}
		if !slices.Equal(tr.Allowed, w.allowed) {
			t.Errorf("%s -> %s allowed = %v, want %v", tr.FromState, tr.ToState, tr.Allowed, w.allowed)
		}
		if !slices.Equal(tr.Required, w.required) {
			t.Errorf("%s -> %s required = %v, want %v", tr.FromState, tr.ToState, tr.Required, w.required)
		}
		if tr.ToState == "dropped" {
			drops++
		}
	}
	// Six X->dropped transitions, one from each state before done, each
	// allowed exactly [field:f_owner] (checked in the table above). GetType
	// shows transitions expanded, so this cannot tell how the YAML spells them.
	if drops != 6 {
		t.Errorf("%d transitions into dropped, want 6", drops)
	}
}

// --- creation: only a maintainer ---

func TestBuildStep_Create(t *testing.T) {
	buildStepLoaded(t)
	grantMaintainer(t)

	t.Run("a maintainer creates a step she owns", func(t *testing.T) {
		res, err := createStep(t, as("ana"), map[string]any{"f_title": "Step " + word(), "f_owner": "user:ana"})
		if err != nil {
			t.Fatalf("ana creates a build_step: %v", err)
		}
		if it := res.Item; it.State != "questions" || it.Fields.AsMap()["f_owner"] != "user:ana" {
			t.Errorf("created = state %s, fields %v; want questions, f_owner user:ana", it.State, it.Fields.AsMap())
		}
	})

	t.Run("an agent for the maintainer names an owner", func(t *testing.T) {
		_, err := createStep(t, botForAna(t, "read", "create"), map[string]any{"f_title": "Step " + word(), "f_owner": "user:ana"})
		ce := apiError(t, "the bot creates with f_owner", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
		if md := metadata(t, ce); md["fields"] != "f_owner" {
			t.Errorf("metadata = %v, want fields f_owner", md)
		}
	})

	t.Run("an agent for the maintainer creates without an owner", func(t *testing.T) {
		_, err := createStep(t, botForAna(t, "read", "create"), map[string]any{"f_title": "Step " + word()})
		ce := apiError(t, "the bot creates without f_owner", err, connect.CodeFailedPrecondition, "INVARIANT_FAILED")
		if md := metadata(t, ce); md["invariant"] != "owner_missing" {
			t.Errorf("metadata = %v, want invariant owner_missing", md)
		}
	})

	t.Run("a user who is not a maintainer names an owner", func(t *testing.T) {
		_, err := createStep(t, as("bob"), map[string]any{"f_title": "Step " + word(), "f_owner": "user:bob"})
		ce := apiError(t, "bob creates with f_owner", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
		if md := metadata(t, ce); md["fields"] != "f_owner" {
			t.Errorf("metadata = %v, want fields f_owner", md)
		}
	})

	t.Run("an agent for the maintainer changes the owner", func(t *testing.T) {
		name := newStep(t)
		err := stepUpdate(t, botForAna(t), name, map[string]any{"f_owner": "user:ana"})
		ce := apiError(t, "the bot updates f_owner", err, connect.CodePermissionDenied, "FIELD_NOT_WRITABLE")
		if md := metadata(t, ce); md["fields"] != "f_owner" {
			t.Errorf("metadata = %v, want fields f_owner", md)
		}
	})
}

// --- the owner's moves ---

// Every move allowed to field:f_owner: the agent acting for the owner is
// refused, bob is refused, the owner's move applies.
func TestBuildStep_OwnerOnlyMoves(t *testing.T) {
	buildStepLoaded(t)
	journal := map[string]any{"f_journal": "docs/journal.md#step-1"}
	moves := []struct {
		from, to string
		f        map[string]any // a patch every caller sends, so only allowed can refuse
	}{
		{"answering", "writing_tests", nil},
		{"tests_proposed", "building", nil},
		{"tests_proposed", "writing_tests", nil},
		{"in_review", "building", nil},
		{"in_review", "done", journal},
		{"questions", "dropped", nil},
		{"answering", "dropped", nil},
		{"writing_tests", "dropped", nil},
		{"tests_proposed", "dropped", nil},
		{"building", "dropped", nil},
		{"in_review", "dropped", nil},
		{"dropped", "questions", nil},
	}
	for _, m := range moves {
		t.Run(m.from+"->"+m.to, func(t *testing.T) {
			name := stepIn(t, m.from)
			_, err := stepMove(t, botForAna(t), name, m.to, m.f)
			apiError(t, "the bot for the owner", err, connect.CodePermissionDenied, "NOT_ALLOWED")
			_, err = stepMove(t, as("bob"), name, m.to, m.f)
			apiError(t, "bob", err, connect.CodePermissionDenied, "NOT_ALLOWED")
			mustStepMove(t, "ana, the owner", as("ana"), name, m.to, m.f)
		})
	}
}

// --- the agent's moves ---

// The agent working for the owner does the work: it asks, proposes tests,
// sends tests back, and opens the review. The owner answers and agrees.
func TestBuildStep_AgentMoves(t *testing.T) {
	buildStepLoaded(t)
	name := newStep(t)
	bot := botForAna(t)
	mustStepMove(t, "the bot", bot, name, "answering", map[string]any{"f_questions": "docs/questions.md#step-1"})
	mustStepMove(t, "ana", as("ana"), name, "writing_tests", nil)
	mustStepMove(t, "the bot", bot, name, "tests_proposed", map[string]any{"f_tests_ref": "internal/api/x_test.go@abc1234"})
	mustStepMove(t, "ana", as("ana"), name, "building", nil)
	mustStepMove(t, "the bot", bot, name, "writing_tests", nil)
	mustStepMove(t, "the bot", bot, name, "tests_proposed", map[string]any{"f_tests_ref": "internal/api/x_test.go@def5678"})
	mustStepMove(t, "ana", as("ana"), name, "building", nil)
	mustStepMove(t, "the bot", bot, name, "in_review", map[string]any{"f_pr_url": "https://example.test/pull/1"})
}

// --- required fields ---

func TestBuildStep_RequiredFields(t *testing.T) {
	buildStepLoaded(t)
	cases := []struct {
		from, to, field, value string
		owner                  bool // the owner moves; otherwise the bot
	}{
		{"questions", "answering", "f_questions", "docs/questions.md#step-1", false},
		{"writing_tests", "tests_proposed", "f_tests_ref", "internal/api/x_test.go@abc1234", false},
		{"building", "in_review", "f_pr_url", "https://example.test/pull/1", false},
		{"in_review", "done", "f_journal", "docs/journal.md#step-1", true},
	}
	for _, c := range cases {
		t.Run(c.from+"->"+c.to, func(t *testing.T) {
			name := stepIn(t, c.from)
			who, client := "the bot", botForAna(t)
			if c.owner {
				who, client = "ana", as("ana")
			}
			_, err := stepMove(t, client, name, c.to, nil)
			ce := apiError(t, who+" moves without "+c.field, err, connect.CodeFailedPrecondition, "MISSING_FIELDS")
			if md := metadata(t, ce); md["fields"] != c.field {
				t.Errorf("metadata = %v, want fields %s", md, c.field)
			}
			mustStepMove(t, who, client, name, c.to, map[string]any{c.field: c.value})
		})
	}
}

// --- frozen fields ---

// In each state, ana (a maintainer and the owner, so every field is hers to
// write) names each frozen field, one at a time: FIELD_FROZEN.
func TestBuildStep_FrozenPerState(t *testing.T) {
	buildStepLoaded(t)
	values := map[string]string{
		"f_questions": "docs/questions.md#rewritten",
		"f_tests_ref": "internal/api/x_test.go@fff0000",
		"f_owner":     "user:ana",
		"f_pr_url":    "https://example.test/pull/2",
		"f_journal":   "docs/journal.md#rewritten",
	}
	frozen := []struct {
		state  string
		fields []string
	}{
		{"writing_tests", []string{"f_questions"}},
		{"tests_proposed", []string{"f_questions", "f_tests_ref"}},
		{"building", []string{"f_questions", "f_tests_ref", "f_owner"}},
		{"in_review", []string{"f_questions", "f_tests_ref", "f_owner", "f_pr_url"}},
		{"done", []string{"f_questions", "f_tests_ref", "f_owner", "f_pr_url", "f_journal"}},
	}
	// One subtest, and one fresh step, per (state, field): every freeze is
	// its own test.
	for _, s := range frozen {
		for _, f := range s.fields {
			t.Run(s.state+"/"+f, func(t *testing.T) {
				name := stepIn(t, s.state)
				err := stepUpdate(t, as("ana"), name, map[string]any{f: values[f]})
				ce := apiError(t, "ana writes "+f+" in "+s.state, err, connect.CodeFailedPrecondition, "FIELD_FROZEN")
				if md := metadata(t, ce); md["fields"] != f || md["state"] != s.state {
					t.Errorf("metadata = %v, want fields %s, state %s", md, f, s.state)
				}
			})
		}
	}
}

// The controls: the same fields are writable where they are not frozen. A
// move leaving a state that freezes a field may not write it; the move
// entering one may.
func TestBuildStep_FrozenControls(t *testing.T) {
	buildStepLoaded(t)

	t.Run("f_questions while answering", func(t *testing.T) {
		name := stepIn(t, "answering")
		if err := stepUpdate(t, botForAna(t), name, map[string]any{"f_questions": "docs/questions.md#more"}); err != nil {
			t.Errorf("the bot rewrites f_questions in answering: %v", err)
		}
	})

	t.Run("f_tests_ref back in writing_tests", func(t *testing.T) {
		name := stepIn(t, "building")
		bot := botForAna(t)
		// Leaving building, the test reference is still frozen.
		_, err := stepMove(t, bot, name, "writing_tests", map[string]any{"f_tests_ref": "internal/api/x_test.go@def5678"})
		ce := apiError(t, "the bot leaves building with a new f_tests_ref", err, connect.CodeFailedPrecondition, "FIELD_FROZEN")
		if md := metadata(t, ce); md["fields"] != "f_tests_ref" || md["state"] != "building" {
			t.Errorf("metadata = %v, want fields f_tests_ref, state building", md)
		}
		mustStepMove(t, "the bot", bot, name, "writing_tests", nil)
		if err := stepUpdate(t, bot, name, map[string]any{"f_tests_ref": "internal/api/x_test.go@def5678"}); err != nil {
			t.Errorf("the bot rewrites f_tests_ref in writing_tests: %v", err)
		}
	})

	t.Run("f_owner in tests_proposed", func(t *testing.T) {
		name := stepIn(t, "tests_proposed")
		if err := stepUpdate(t, as("ana"), name, map[string]any{"f_owner": "user:bob"}); err != nil {
			t.Fatalf("ana writes f_owner in tests_proposed: %v", err)
		}
		if got := get(t, "ana", name).Fields.AsMap()["f_owner"]; got != "user:bob" {
			t.Errorf("f_owner = %v after the write, want user:bob", got)
		}
	})

	t.Run("f_pr_url back in building", func(t *testing.T) {
		name := stepIn(t, "in_review")
		mustStepMove(t, "ana", as("ana"), name, "building", nil)
		if err := stepUpdate(t, botForAna(t), name, map[string]any{"f_pr_url": "https://example.test/pull/3"}); err != nil {
			t.Errorf("the bot rewrites f_pr_url in building: %v", err)
		}
	})

	t.Run("f_journal in in_review", func(t *testing.T) {
		name := stepIn(t, "in_review")
		if err := stepUpdate(t, botForAna(t), name, map[string]any{"f_journal": "docs/journal.md#draft"}); err != nil {
			t.Errorf("the bot writes f_journal in in_review: %v", err)
		}
	})
}
