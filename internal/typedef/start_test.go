// Tests for starting work, step 2 (docs/plans/starting-work.md, 1.1 to
// 1.7): the start block on a Type, its load checks and Start.Admits.
//
//   - start: {in, allowed}. in lists container Type IDs (any version of
//     each) or top (no container); absent means anywhere. allowed takes
//     role:<name>, principal:user:<id>, field:<id> (a principal field of
//     the container), container_creator or nobody.
//   - Stage 2, the Type alone: a Type named top (RESERVED_NAME); an entry
//     of another kind, creator, or a principal other than user:
//     (INVALID_VALUE); field: or container_creator without in, or with top
//     in it, and nobody beside another entry (CONFLICT); a malformed in
//     entry (INVALID_NAME); an entry twice (DUPLICATE); in: [] (INVALID_VALUE);
//     start: {} (REQUIRED).
//   - Stage 3, the Types together: in names a Type that is not loaded
//     (UNKNOWN_TYPE); field: missing, or not a principal field, on a
//     version of a Type in in (UNKNOWN_FIELD, WRONG_FIELD_TYPE); own or
//     shared naming a Type whose latest start.in excludes the container's
//     Type, and a create reaction whose source the target's latest in
//     excludes (START_NOT_IN); own or shared naming a Type whose latest
//     allowed is [nobody] (CONFLICT): fail closed.
package typedef_test

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const codeStartNotIn = typedef.Code("START_NOT_IN")

// stMemo is a Type with nothing but its start block, which the cases add.
func stMemo(start string) string {
	return "id: memo\nversion: 1\nfields:\n  - {id: f_lead, type: principal}\n" + start
}

// The block parses: in and allowed, as written, and a Type without one
// has none.
func TestStart_Parses(t *testing.T) {
	ty, err := typedef.Parse([]byte(stMemo("start:\n  in: [desk, top]\n  allowed: ['role:lead', 'principal:user:ana']\n")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if ty.Start == nil || !slices.Equal(ty.Start.In, []string{"desk", typedef.StartTop}) ||
		!slices.Equal(ty.Start.Allowed, []string{"role:lead", "principal:user:ana"}) {
		t.Errorf("Start = %+v, want in [desk top], allowed [role:lead principal:user:ana]", ty.Start)
	}
	plain, err := typedef.Parse([]byte(stMemo("")))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if plain.Start != nil {
		t.Errorf("a Type without a start block: Start = %+v, want nil", plain.Start)
	}
	// start: ~ is no block.
	if none, err := typedef.Parse([]byte(stMemo("start: ~\n"))); err != nil || none.Start != nil {
		t.Errorf("start: ~ = %+v, %v; want no block", none, err)
	}
}

// The repository's Types: incident@4 is the one with a block, in: [desk]
// and no allowed (owner's decision, option B); no other has one.
func TestStart_TheRepositoryTypes(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	found := false
	for _, ty := range types {
		switch {
		case ty.ID == "incident" && ty.Version == 4:
			found = true
			if ty.Start == nil || !slices.Equal(ty.Start.In, []string{"desk"}) || len(ty.Start.Allowed) != 0 {
				t.Errorf("incident@4 Start = %+v, want in [desk], no allowed", ty.Start)
			}
		case ty.Start != nil:
			t.Errorf("%s@%d has a start block %+v; only incident@4 should", ty.ID, ty.Version, ty.Start)
		}
	}
	if !found {
		t.Error("no incident@4 in types/")
	}
}

// Admits judges start.in: a container Type ID (any version), or "" for
// the top level, which only top admits. A nil block, or one without in,
// admits anywhere.
func TestStart_Admits(t *testing.T) {
	cases := []struct {
		name  string
		start *typedef.Start
		where string
		want  bool
	}{
		{"no block, in a desk", nil, "desk", true},
		{"no block, at the top level", nil, "", true},
		{"a block without in, in a desk", &typedef.Start{Allowed: []string{"role:lead"}}, "desk", true},
		{"a block without in, at the top level", &typedef.Start{Allowed: []string{"role:lead"}}, "", true},
		{"in [desk], in a desk", &typedef.Start{In: []string{"desk"}}, "desk", true},
		{"in [desk], in a channel", &typedef.Start{In: []string{"desk"}}, "channel", false},
		{"in [desk], at the top level", &typedef.Start{In: []string{"desk"}}, "", false},
		{"in [top], at the top level", &typedef.Start{In: []string{typedef.StartTop}}, "", true},
		{"in [top], in a desk", &typedef.Start{In: []string{typedef.StartTop}}, "desk", false},
		{"in [desk, top], at the top level", &typedef.Start{In: []string{"desk", typedef.StartTop}}, "", true},
		{"in [desk, top], in a desk", &typedef.Start{In: []string{"desk", typedef.StartTop}}, "desk", true},
		{"in [desk, top], in a channel", &typedef.Start{In: []string{"desk", typedef.StartTop}}, "channel", false},
		// top is a word of in, never a Type: nothing can be a container of Type "top".
		{"in [desk], a container whose Type were named top", &typedef.Start{In: []string{"desk"}}, "top", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.start.Admits(c.where); got != c.want {
				t.Errorf("Admits(%q) = %v, want %v", c.where, got, c.want)
			}
		})
	}
}

// stCase is one load problem: the code and path it is reported at, on
// which Type version.
type stCase struct {
	name    string
	src     string
	code    typedef.Code
	path    string
	typeID  string
	version int
}

// wantOne checks that ps holds exactly the one problem c names.
func wantOne(t *testing.T, ps typedef.Problems, code typedef.Code, path, typeID string, version int) {
	t.Helper()
	if len(ps) != 1 {
		t.Fatalf("%d problems, want 1 (%s at %s on %s v%d):\n%v", len(ps), code, path, typeID, version, ps)
	}
	p := ps[0]
	if p.Code != code || p.Path != path || p.TypeID != typeID || p.Version != version {
		t.Errorf("problem = %s at %q on %s v%d (%s); want %s at %q on %s v%d",
			p.Code, p.Path, p.TypeID, p.Version, p.Message, code, path, typeID, version)
	}
	if strings.TrimSpace(p.Message) == "" {
		t.Error("the problem has no message")
	}
}

// Stage 2: each problem of the block on its own Type, alone.
func TestStart_TheTypeAlone(t *testing.T) {
	cases := []stCase{
		{name: "a Type named top", src: "id: top\nversion: 1\n", code: typedef.CodeReservedName, path: "id", typeID: "top", version: 1},
		{name: "creator: the Item has no creator yet", src: stMemo("start:\n  allowed: [creator]\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[0]"},
		{name: "an entry of no known kind", src: stMemo("start:\n  allowed: [anyone]\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[0]"},
		{name: "a role with no name", src: stMemo("start:\n  allowed: ['role:']\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[0]"},
		{name: "except is not a start entry", src: stMemo("start:\n  allowed: ['except:creator']\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[0]"},
		{name: "an untyped principal", src: stMemo("start:\n  allowed: ['principal:ana']\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[0]"},
		{name: "an agent principal", src: stMemo("start:\n  allowed: ['role:lead', 'principal:agent:bot']\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[1]"},
		{name: "the timer", src: stMemo("start:\n  allowed: ['principal:system:timer']\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[0]"},
		{name: "a hook of the Type", src: stMemo("start:\n  allowed: ['principal:hook:memo:check']\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[0]"},
		{name: "a customer: their gate is own and shared", src: stMemo("start:\n  allowed: ['principal:customer:kim']\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[0]"},
		{name: "an intake, before intakes exist", src: stMemo("start:\n  allowed: ['principal:intake:memo:siem']\n"),
			code: typedef.CodeInvalidValue, path: "start.allowed[0]"},
		{name: "field: without in", src: stMemo("start:\n  allowed: ['field:f_lead']\n"),
			code: typedef.CodeConflict, path: "start.allowed[0]"},
		{name: "field: with top in in", src: stMemo("start:\n  in: [desk, top]\n  allowed: ['role:lead', 'field:f_lead']\n"),
			code: typedef.CodeConflict, path: "start.allowed[1]"},
		{name: "container_creator without in", src: stMemo("start:\n  allowed: [container_creator]\n"),
			code: typedef.CodeConflict, path: "start.allowed[0]"},
		{name: "container_creator with top in in", src: stMemo("start:\n  in: [top]\n  allowed: [container_creator]\n"),
			code: typedef.CodeConflict, path: "start.allowed[0]"},
		{name: "nobody beside another entry", src: stMemo("start:\n  allowed: ['role:lead', nobody]\n"),
			code: typedef.CodeConflict, path: "start.allowed[1]"},
		{name: "an entry twice", src: stMemo("start:\n  allowed: ['role:lead', 'role:lead']\n"),
			code: typedef.CodeDuplicate, path: "start.allowed[1]"},
		{name: "a container Type that is no Type ID", src: stMemo("start:\n  in: [Desk]\n"),
			code: typedef.CodeInvalidName, path: "start.in[0]"},
		{name: "a container Type twice", src: stMemo("start:\n  in: [desk, desk]\n"),
			code: typedef.CodeDuplicate, path: "start.in[1]"},
		{name: "in that lists nothing", src: stMemo("start:\n  in: []\n"),
			code: typedef.CodeInvalidValue, path: "start.in"},
		{name: "a block that says nothing", src: stMemo("start: {}\n"),
			code: typedef.CodeRequired, path: "start"},
		{name: "a key start does not have", src: stMemo("start:\n  in: [desk]\n  except: [creator]\n"),
			code: typedef.CodeUnknownKey, path: "start.except"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			typeID, version := cmpOr(c.typeID, "memo"), max(c.version, 1)
			ty, err := typedef.Parse([]byte(c.src))
			if err == nil {
				t.Fatalf("Parse loaded it (Start = %+v); want %s at %s", ty.Start, c.code, c.path)
			}
			wantOne(t, problemsOf(t, err), c.code, c.path, typeID, version)
		})
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Stage 2: blocks that load.
func TestStart_BlocksThatLoad(t *testing.T) {
	for _, start := range []string{
		"start:\n  in: [top]\n",
		"start:\n  in: [desk, top]\n",
		"start:\n  in: [desk]\n",
		"start:\n  in: [desk]\n  allowed: []\n",
		"start:\n  allowed: ['role:lead']\n",
		"start:\n  allowed: [nobody]\n",
		"start:\n  allowed: ['principal:user:ana', 'role:lead']\n",
		// field: and container_creator read the container: in names one,
		// without top. Whether desk declares f_lead is stage 3's question.
		"start:\n  in: [desk]\n  allowed: ['field:f_lead', container_creator]\n",
	} {
		if _, err := typedef.Parse([]byte(stMemo(start))); err != nil {
			t.Errorf("%s: %v", strings.TrimSpace(start), err)
		}
	}
}

// The Types of the stage 3 cases. room and hall are containers; room has
// a principal field and a text field.
const (
	stRoom = "id: room\nversion: 1\nfields:\n  - {id: f_lead, type: principal}\n  - {id: f_note, type: string}\n"
	stHall = "id: hall\nversion: 1\n"
)

func stAsk(version int, start string) string {
	return fmt.Sprintf("id: ask\nversion: %d\nfields:\n  - {id: f_title, type: string}\n%s", version, start)
}

func srcs(files ...string) []typedef.Source {
	var out []typedef.Source
	for i, f := range files {
		out = append(out, typedef.Source{Name: fmt.Sprintf("t%d.yaml", i), Content: []byte(f)})
	}
	return out
}

// stHallMakes is a hall that creates an ask inside itself when it opens,
// and the ask's consent to it.
const stHallMakes = `id: hall
version: 1
initial_state: draft
states:
  - {id: draft, category: todo}
  - {id: open, category: active}
transitions:
  - {from: draft, to: open}
reactions:
  - {id: make, kind: create, on: enter, state: open, type: ask, version: 1, fields: {f_title: '"made"'}}
`

const stAskConsents = "accepts_reactions:\n  hall:make: {creates: [f_title]}\n"

// Stage 3: the Types together.
func TestStart_TheTypesTogether(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		stCase
	}{
		{name: "in names a Type that is not loaded",
			files:  []string{stRoom, stAsk(1, "start:\n  in: [ghost]\n")},
			stCase: stCase{code: typedef.CodeUnknownType, path: "start.in[0]", typeID: "ask", version: 1}},
		{name: "field: missing on a Type in in",
			files:  []string{stRoom, stHall, stAsk(1, "start:\n  in: [room, hall]\n  allowed: ['field:f_lead']\n")},
			stCase: stCase{code: typedef.CodeUnknownField, path: "start.allowed[0]", typeID: "ask", version: 1}},
		{name: "field: missing on one version of a Type in in",
			files: []string{stRoom, "id: room\nversion: 2\nfields:\n  - {id: f_note, type: string}\n",
				stAsk(1, "start:\n  in: [room]\n  allowed: ['field:f_lead']\n")},
			stCase: stCase{code: typedef.CodeUnknownField, path: "start.allowed[0]", typeID: "ask", version: 1}},
		{name: "field: not a principal field",
			files:  []string{stRoom, stAsk(1, "start:\n  in: [room]\n  allowed: ['field:f_note']\n")},
			stCase: stCase{code: typedef.CodeWrongFieldType, path: "start.allowed[0]", typeID: "ask", version: 1}},
		{name: "own names a Type whose in excludes the container",
			files:  []string{stRoom, stHall + "own: [ask]\n", stAsk(1, "start:\n  in: [room]\n")},
			stCase: stCase{code: codeStartNotIn, path: "own[0]", typeID: "hall", version: 1}},
		{name: "shared names a Type whose in excludes the container",
			files:  []string{stRoom, stHall + "shared: [ask]\n", stAsk(1, "start:\n  in: [room]\n")},
			stCase: stCase{code: codeStartNotIn, path: "shared[0]", typeID: "hall", version: 1}},
		{name: "own and the latest in: an earlier version's in does not count",
			files:  []string{stRoom, stHall + "own: [ask]\n", stAsk(1, "start:\n  in: [hall]\n"), stAsk(2, "start:\n  in: [room]\n")},
			stCase: stCase{code: codeStartNotIn, path: "own[0]", typeID: "hall", version: 1}},
		{name: "a create reaction whose source the target's in excludes",
			files:  []string{stRoom, stHallMakes, stAsk(1, "start:\n  in: [room]\n"+stAskConsents)},
			stCase: stCase{code: codeStartNotIn, path: "reactions[0]", typeID: "hall", version: 1}},
		{name: "a create reaction and the target's latest in",
			files: []string{stRoom, stHallMakes, stAsk(1, "start:\n  in: [hall]\n"+stAskConsents),
				stAsk(2, "start:\n  in: [room]\n"+stAskConsents)},
			stCase: stCase{code: codeStartNotIn, path: "reactions[0]", typeID: "hall", version: 1}},
		{name: "own names a Type nobody may start: fail closed",
			files:  []string{stRoom + "own: [ask]\n", stAsk(1, "start:\n  allowed: [nobody]\n")},
			stCase: stCase{code: typedef.CodeConflict, path: "own[0]", typeID: "room", version: 1}},
		{name: "shared names a Type nobody may start: fail closed",
			files:  []string{stRoom + "shared: [ask]\n", stAsk(1, "start:\n  allowed: [nobody]\n")},
			stCase: stCase{code: typedef.CodeConflict, path: "shared[0]", typeID: "room", version: 1}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			wantOne(t, checkProblems(t, srcs(c.files...), nil), c.code, c.path, c.typeID, c.version)
		})
	}
}

// Stage 3: what loads. The latest version decides; a block without in
// admits every container; top in in is no container Type.
func TestStart_TheTypesTogether_Load(t *testing.T) {
	cases := []struct {
		name  string
		files []string
	}{
		{"own, and in admits the container", []string{stRoom + "own: [ask]\n", stAsk(1, "start:\n  in: [room]\n")}},
		{"own, and in admits every version of the container",
			[]string{stRoom + "own: [ask]\n", "id: room\nversion: 2\nfields:\n  - {id: f_lead, type: principal}\nown: [ask]\n",
				stAsk(1, "start:\n  in: [room]\n  allowed: ['field:f_lead']\n")}},
		{"own, and the latest in admits it though an earlier one did not",
			[]string{stRoom, stHall + "own: [ask]\n", stAsk(1, "start:\n  in: [room]\n"), stAsk(2, "start:\n  in: [hall]\n")}},
		{"own, and the latest version dropped the block",
			[]string{stRoom, stHall + "own: [ask]\n", stAsk(1, "start:\n  in: [room]\n"), stAsk(2, "")}},
		{"own, and a block without in", []string{stHall + "own: [ask]\n", stAsk(1, "start:\n  allowed: ['role:lead']\n")}},
		{"a create reaction the target's latest in admits",
			[]string{stRoom, stHallMakes, stAsk(1, "start:\n  in: [room]\n"+stAskConsents), stAsk(2, "start:\n  in: [room, hall]\n"+stAskConsents)}},
		{"a create reaction into a Type nobody else may start",
			[]string{stHallMakes, stAsk(1, "start:\n  in: [hall]\n  allowed: [nobody]\n"+stAskConsents)}},
		{"field: and container_creator on every version of every Type in in",
			[]string{stRoom, "id: hall\nversion: 1\nfields:\n  - {id: f_lead, type: principal, multiple: true}\n",
				stAsk(1, "start:\n  in: [room, hall]\n  allowed: ['field:f_lead', container_creator]\n")}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			checkClean(t, srcs(c.files...), nil)
		})
	}
}

// ValidateTypes (Check with the registered versions): a draft target
// version whose in drops a registered source of a create reaction is
// refused, and the problem lands on the registered source, with no
// position. Registering the draft would leave hall creating asks its own
// rule refuses (starting-work.md, 1.5).
func TestStart_ADraftBreaksARegisteredSource(t *testing.T) {
	registered := [][]byte{
		storedJSON(t, stRoom), storedJSON(t, stHallMakes), storedJSON(t, stAsk(1, "start:\n  in: [room, hall]\n"+stAskConsents)),
	}
	ps := checkProblems(t, srcs(stAsk(2, "start:\n  in: [room]\n"+stAskConsents)), registered)
	wantOne(t, ps, codeStartNotIn, "reactions[0]", "hall", 1)
	if ps[0].Source != "" || ps[0].Line != 0 {
		t.Errorf("the problem on the registered hall v1: source %q, line %d; want neither", ps[0].Source, ps[0].Line)
	}
}

// storedJSON is a Type as RegisterType stores it.
func storedJSON(t *testing.T, src string) []byte {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	def, err := json.Marshal(ty)
	if err != nil {
		t.Fatal(err)
	}
	return def
}

// The stored form keeps the block, and a Type without one stores no key
// for it: every definition registered before this step is unchanged.
func TestStart_StoredForm(t *testing.T) {
	with := storedJSON(t, stAsk(1, "start:\n  in: [room, top]\n  allowed: ['role:lead']\n"))
	ty, err := typedef.FromJSON(with)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if ty.Start == nil || !slices.Equal(ty.Start.In, []string{"room", "top"}) || !slices.Equal(ty.Start.Allowed, []string{"role:lead"}) {
		t.Errorf("read back: Start = %+v", ty.Start)
	}
	if without := storedJSON(t, stAsk(1, "")); strings.Contains(string(without), "start") {
		t.Errorf("a Type without a block stores %s; want no start key", without)
	}
}
