// Tests for Ventoux V2b: ValidateTypes checks draft Types against every
// registered version (typedef.Check) and writes nothing. A draft with
// problems is a result, not an error: the error is for the kernel's own
// failures. Identical means what RegisterType means: the same definition,
// compared as jsonb with the stored one.
//
// The tests share one database with the rest of the package, so each uses
// Type IDs of its own and never expects the registered set to be clean.
package itemservice_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// vtID returns a Type ID no other test uses: vt<8 hex>_<base>.
func vtID(base string) string {
	return "vt" + strings.ReplaceAll(uuid.NewString()[:8], "-", "") + "_" + base
}

func registerYAML(t *testing.T, k *itemservice.Service, src string) {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("test bug: parse: %v\n%s", err, src)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register %s v%d: %v", ty.ID, ty.Version, err)
	}
}

func validate(t *testing.T, k *itemservice.Service, drafts ...typedef.Source) typedef.Problems {
	t.Helper()
	ps, err := k.ValidateTypes(testCtx(t), drafts)
	if err != nil {
		t.Fatalf("ValidateTypes: %v", err)
	}
	return ps
}

func src(name, yaml string) typedef.Source {
	return typedef.Source{Name: name, Content: []byte(yaml)}
}

// typesTable is the whole types table, as text: what "nothing written"
// compares. The package's tests run one at a time, so it is stable here.
func typesTable(t *testing.T) []string {
	t.Helper()
	rows, err := pool.Query(testCtx(t), `select id || '@' || version || ' ' || md5(definition::text) from types order by 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func notRegistered(t *testing.T, k *itemservice.Service, id string, version int) {
	t.Helper()
	if _, err := k.Type(testCtx(t), id, version); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("%s v%d: Type = %v, want it not registered", id, version, err)
	}
}

// A draft with problems is a successful call that lists them, each with
// the draft's name and its position in the draft.
func TestValidateTypes_ProblemsAreAResult(t *testing.T) {
	k := itemservice.New(pool)
	id := vtID("memo")
	ps := validate(t, k, src("drafts/memo.yaml", fmt.Sprintf(`id: %s
version: 1
initial_state: open
states:
  - {id: open, category: doing}
transitions:
  - {from: open, to: closed}
`, id)))
	var got []string
	for _, p := range ps {
		got = append(got, fmt.Sprintf("%s:%d:%d %s %s %s@%d", p.Source, p.Line, p.Column, p.Code, p.Path, p.TypeID, p.Version))
	}
	want := []string{
		fmt.Sprintf("drafts/memo.yaml:5:16 INVALID_VALUE states[0].category %s@1", id),
		fmt.Sprintf("drafts/memo.yaml:7:18 UNKNOWN_STATE transitions[0].to %s@1", id),
	}
	if !slices.Equal(got, want) {
		t.Errorf("problems =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	notRegistered(t, k, id, 1)
}

// A clean draft, a broken one and a changed version: none of them writes.
func TestValidateTypes_WritesNothing(t *testing.T) {
	k := itemservice.New(pool)
	memo, note := vtID("memo"), vtID("note")
	registerYAML(t, k, fmt.Sprintf("id: %s\nversion: 1\n", memo))
	before := typesTable(t)

	if ps := validate(t, k, src("note.yaml", fmt.Sprintf("id: %s\nversion: 1\n", note))); len(ps) != 0 {
		t.Errorf("a clean draft: problems %v", ps)
	}
	if ps := validate(t, k, src("note.yaml", fmt.Sprintf("id: %s\nversion: 2\nfields: [{id: today, type: date}]\n", note))); len(ps) == 0 {
		t.Error("a broken draft: no problems")
	}
	if ps := validate(t, k, src("memo.yaml", fmt.Sprintf("id: %s\nversion: 1\nfields: [{id: f_x, type: string}]\n", memo))); len(ps) == 0 {
		t.Error("a changed version: no problems")
	}

	if after := typesTable(t); !slices.Equal(before, after) {
		t.Errorf("the types table changed:\nbefore %v\nafter  %v", before, after)
	}
	notRegistered(t, k, note, 1)
	notRegistered(t, k, note, 2)
}

// An identical registered id@version passes; a different one is
// VERSION_CHANGED, on the draft's version key.
func TestValidateTypes_VersionChanged(t *testing.T) {
	k := itemservice.New(pool)
	memo := vtID("memo")
	registerYAML(t, k, fmt.Sprintf("id: %s\nversion: 1\nfields:\n  - {id: f_title, type: string}\n", memo))

	same := fmt.Sprintf("# reformatted\nfields: [{type: string, id: f_title}]\nversion: 1\nid: %s\n", memo)
	if ps := validate(t, k, src("memo.yaml", same)); len(ps) != 0 {
		t.Errorf("the same definition, written another way: problems %v", ps)
	}

	changed := fmt.Sprintf("id: %s\nversion: 1\nfields:\n  - {id: f_title, type: int}\n", memo)
	ps := validate(t, k, src("memo.yaml", changed))
	if len(ps) != 1 {
		t.Fatalf("%d problems, want 1:\n%v", len(ps), ps)
	}
	p := ps[0]
	if p.Code != typedef.CodeVersionChanged || p.Source != "memo.yaml" || p.Path != "version" || p.Line != 2 || p.Column != 1 ||
		p.TypeID != memo || p.Version != 1 {
		t.Errorf("problem = %+v, want VERSION_CHANGED at memo.yaml:2:1 version", p)
	}
}

// Identical is decided as RegisterType decides it: against the stored
// definition, as jsonb. Here the stored row holds a key this binary does
// not read (as a row from another build might), so a draft that would
// re-encode the same is still not identical, and RegisterType refuses it.
func TestValidateTypes_IdenticalAsRegisterTypeDecides(t *testing.T) {
	k := itemservice.New(pool)
	memo := vtID("memo")
	draft := fmt.Sprintf("id: %s\nversion: 1\n", memo)
	ty, err := typedef.Parse([]byte(draft))
	if err != nil {
		t.Fatal(err)
	}
	stored, err := json.Marshal(ty)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(stored, &m); err != nil {
		t.Fatal(err)
	}
	m["x_from_another_build"] = true
	stored, _ = json.Marshal(m)
	if _, err := pool.Exec(testCtx(t), `insert into types (id, version, definition) values ($1, 1, $2::jsonb)`, memo, string(stored)); err != nil {
		t.Fatalf("store the row: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err == nil {
		t.Fatal("test bug: RegisterType accepts the draft; the stored row is not different enough")
	}

	ps := validate(t, k, src("memo.yaml", draft))
	if len(ps) != 1 || ps[0].Code != typedef.CodeVersionChanged {
		t.Errorf("problems = %v, want VERSION_CHANGED, as RegisterType refuses it", ps)
	}
}

// Drafts are checked against every registered version: a draft that needs
// a registered Type finds it, and a draft that breaks a registered version
// reports the problem on that version, with no source and no position.
func TestValidateTypes_AgainstEveryRegisteredVersion(t *testing.T) {
	k := itemservice.New(pool)
	part, whole := vtID("part"), vtID("whole")
	openDone := "initial_state: open\nstates:\n  - {id: open, category: todo}\n  - {id: done, category: done}\ntransitions:\n  - {from: open, to: done}\n"
	partV1 := fmt.Sprintf("id: %s\nversion: 1\n%slinks:\n  - {name: whole, inverse: parts, targets: [%s], max: one}\nreactions:\n  - {id: finish, kind: move, on: enter, state: done, link: whole, to: done}\n",
		part, openDone, whole)
	wholeV1 := fmt.Sprintf("id: %s\nversion: 1\n%saccepts_reactions:\n  %s:finish: {moves: [done]}\n", whole, openDone, part)
	registerYAML(t, k, wholeV1)
	registerYAML(t, k, partV1)

	// part v2, a draft, links to whole, which only the registry holds.
	partV2 := strings.Replace(partV1, "version: 1", "version: 2", 1)
	if ps := validate(t, k, src("part.v2.yaml", partV2)); len(ps) != 0 {
		t.Errorf("part v2: problems %v", ps)
	}

	// whole v2 drops the consent: registered part v1's reaction breaks.
	wholeV2 := fmt.Sprintf("id: %s\nversion: 2\n%s", whole, openDone)
	ps := validate(t, k, src("whole.v2.yaml", wholeV2))
	if len(ps) != 1 {
		t.Fatalf("whole v2: %d problems, want 1:\n%v", len(ps), ps)
	}
	p := ps[0]
	if p.Code != typedef.CodeConsentMissing || p.Source != "" || p.Line != 0 || p.Column != 0 ||
		p.TypeID != part || p.Version != 1 || p.Path != "reactions[0]" {
		t.Errorf("problem = %+v, want CONSENT_MISSING on registered %s v1 reactions[0], no source or position", p, part)
	}
}

// The kernel's own failure is an error, not Problems: here, a canceled
// context.
func TestValidateTypes_AKernelFailureIsAnError(t *testing.T) {
	k := itemservice.New(pool)
	ctx, cancel := context.WithCancel(testCtx(t))
	cancel()
	ps, err := k.ValidateTypes(ctx, []typedef.Source{src("memo.yaml", "id: "+vtID("memo")+"\nversion: 1\n")})
	if !errors.Is(err, context.Canceled) || ps != nil {
		t.Errorf("ValidateTypes = %v, %v; want no problems and context.Canceled", ps, err)
	}
}
