// Tests for track 1, item 6, step 1 (a create names the latest version):
// the key start.older_versions and its load checks.
//
//   - start: {older_versions: [1, 2]} or {older_versions: all}: the older
//     versions of the Type that may still be created while this version is
//     the latest. Absent: only the latest version is created. The item
//     service applies it (internal/itemservice, create_latest_test.go).
//   - A block may hold this key alone: it then says nothing of where or
//     by whom.
//   - Stage 1: a map, or a map or a list inside the list (WRONG_SHAPE).
//   - Stage 2, the Type alone: a word other than all, a list that names
//     nothing, an entry that is not a whole number, a version below 1, and
//     a version that is not older than the Type's own (INVALID_VALUE); a
//     version twice (DUPLICATE, on the second).
//   - Stage 3, the Types together: a listed version that is not loaded
//     (UNKNOWN_TYPE), also for a draft against the registered versions.
//   - The stored form keeps the key as written: "all", or the list.
//   - The repository's Types: each new latest version is the one before it
//     with older_versions: all and nothing else changed.
package typedef_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// Blocks that load: the key alone, beside in and allowed, and all on a
// first version (nothing is older yet; it speaks for when it is latest).
func TestOlderVersions_BlocksThatLoad(t *testing.T) {
	for _, c := range []struct {
		version int
		start   string
	}{
		{3, "start:\n  older_versions: [1, 2]\n"},
		{3, "start:\n  older_versions: [2]\n"},
		{3, "start:\n  older_versions: all\n"},
		{1, "start:\n  older_versions: all\n"},
		{3, "start:\n  in: [top]\n  allowed: ['role:lead']\n  older_versions: [1]\n"},
		{2, "start:\n  allowed: [nobody]\n  older_versions: all\n"},
	} {
		if _, err := typedef.Parse([]byte(stAsk(c.version, c.start))); err != nil {
			t.Errorf("ask@%d %s: %v", c.version, strings.TrimSpace(c.start), err)
		}
	}
}

// Each problem of the key on its own Type, alone.
func TestOlderVersions_TheTypeAlone(t *testing.T) {
	cases := []struct {
		name    string
		version int
		start   string
		code    typedef.Code
		path    string
	}{
		{"a map", 3, "start:\n  older_versions: {from: 1}\n", typedef.CodeWrongShape, "start.older_versions"},
		{"a map inside the list", 3, "start:\n  older_versions: [{from: 1}]\n", typedef.CodeWrongShape, "start.older_versions[0]"},
		{"a word other than all", 3, "start:\n  older_versions: some\n", typedef.CodeInvalidValue, "start.older_versions"},
		{"a single number, not a list", 3, "start:\n  older_versions: 2\n", typedef.CodeInvalidValue, "start.older_versions"},
		{"a list that names nothing", 3, "start:\n  older_versions: []\n", typedef.CodeInvalidValue, "start.older_versions"},
		{"all inside a list", 3, "start:\n  older_versions: [all]\n", typedef.CodeInvalidValue, "start.older_versions[0]"},
		{"an entry that is no number", 3, "start:\n  older_versions: [1, two]\n", typedef.CodeInvalidValue, "start.older_versions[1]"},
		{"version 0", 3, "start:\n  older_versions: [0]\n", typedef.CodeInvalidValue, "start.older_versions[0]"},
		{"a negative version", 3, "start:\n  older_versions: [-1]\n", typedef.CodeInvalidValue, "start.older_versions[0]"},
		{"its own version", 3, "start:\n  older_versions: [1, 3]\n", typedef.CodeInvalidValue, "start.older_versions[1]"},
		{"a later version", 3, "start:\n  older_versions: [4]\n", typedef.CodeInvalidValue, "start.older_versions[0]"},
		{"a list on a first version: nothing is older", 1, "start:\n  older_versions: [1]\n", typedef.CodeInvalidValue, "start.older_versions[0]"},
		{"a version twice", 3, "start:\n  older_versions: [1, 2, 1]\n", typedef.CodeDuplicate, "start.older_versions[2]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ty, err := typedef.Parse([]byte(stAsk(c.version, c.start)))
			if err == nil {
				t.Fatalf("Parse loaded it (Start = %+v); want %s at %s", ty.Start, c.code, c.path)
			}
			wantOne(t, problemsOf(t, err), c.code, c.path, "ask", c.version)
		})
	}
}

// Stage 3: a listed version must be loaded. all asks for nothing.
func TestOlderVersions_TheTypesTogether(t *testing.T) {
	// ask@3 lists @2, and only @1 is loaded beside it.
	ps := checkProblems(t, srcs(stAsk(1, ""), stAsk(3, "start:\n  older_versions: [1, 2]\n")), nil)
	wantOne(t, ps, typedef.CodeUnknownType, "start.older_versions[1]", "ask", 3)

	checkClean(t, srcs(stAsk(1, ""), stAsk(2, ""), stAsk(3, "start:\n  older_versions: [1, 2]\n")), nil)
	checkClean(t, srcs(stAsk(1, ""), stAsk(3, "start:\n  older_versions: all\n")), nil)
	checkClean(t, srcs(stAsk(3, "start:\n  older_versions: all\n")), nil)
}

// ValidateTypes (Check with the registered versions): a draft may list a
// registered version; one that is neither registered nor among the drafts
// is the draft's problem.
func TestOlderVersions_ADraftAgainstTheRegistered(t *testing.T) {
	registered := [][]byte{storedJSON(t, stAsk(1, "")), storedJSON(t, stAsk(2, ""))}
	checkClean(t, srcs(stAsk(4, "start:\n  older_versions: [1, 2]\n")), registered)
	ps := checkProblems(t, srcs(stAsk(4, "start:\n  older_versions: [2, 3]\n")), registered)
	wantOne(t, ps, typedef.CodeUnknownType, "start.older_versions[1]", "ask", 4)
}

// The stored form keeps the key as written, and reads back the same: a
// definition registered with it is found unchanged when it is registered
// again (RegisterType compares the stored JSON).
func TestOlderVersions_StoredForm(t *testing.T) {
	for _, c := range []struct{ start, want string }{
		{"start:\n  older_versions: all\n", `"start":{"older_versions":"all"}`},
		{"start:\n  older_versions: [2, 1]\n", `"start":{"older_versions":[2,1]}`},
		{"start:\n  in: [top]\n  older_versions: [1]\n", `"start":{"in":["top"],"older_versions":[1]}`},
	} {
		stored := storedJSON(t, stAsk(3, c.start))
		if !strings.Contains(string(stored), c.want) {
			t.Errorf("%s is stored as %s; want it to hold %s", strings.TrimSpace(c.start), stored, c.want)
			continue
		}
		back, err := typedef.FromJSON(stored)
		if err != nil {
			t.Errorf("FromJSON(%s): %v", stored, err)
			continue
		}
		again, err := json.Marshal(back)
		if err != nil {
			t.Fatal(err)
		}
		if string(again) != string(stored) {
			t.Errorf("stored, read and stored again:\n  %s\nwant the first form:\n  %s", again, stored)
		}
	}
	if without := storedJSON(t, stAsk(3, "start:\n  in: [top]\n")); strings.Contains(string(without), "older_versions") {
		t.Errorf("a block without the key stores %s; want no older_versions key", without)
	}
}

// newLatest are the versions added to types/ by this step: for each, the
// version before it. (build_step is not here: nothing creates
// build_step@1, so build_step@2 stays its latest and @1 is closed.)
// ticket@9, added by step 2, is ticket@8 with the migrate key: it carries
// ticket@8's start block, and is the latest ticket.
var newLatest = map[string]int{
	"bug": 3, "comment": 3, "desk": 3, "incident": 4, "note": 2, "reply": 2, "sla_goal": 3, "sla_policy": 2,
	"sprint": 2, "story": 3, "task": 3, "ticket": 7, "vacation_request": 3,
}

// The repository's Types: each new latest version is the version before
// it with start.older_versions: all, and nothing else changed; and no
// other version in types/ has the key.
func TestOlderVersions_TheRepositoryTypes(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	stored := map[string]map[string]any{} // id@version -> the stored definition
	name := func(id string, v int) string { return id + "@" + string(rune('0'+v)) }
	for _, ty := range types {
		def, err := json.Marshal(ty)
		if err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err := json.Unmarshal(def, &m); err != nil {
			t.Fatal(err)
		}
		stored[name(ty.ID, ty.Version)] = m
	}
	keeps := func(m map[string]any) any {
		start, _ := m["start"].(map[string]any)
		return start["older_versions"] // reading a nil map is fine in Go: it holds nothing
	}
	for id, before := range newLatest {
		was, now := stored[name(id, before)], stored[name(id, before+1)]
		if was == nil || now == nil {
			t.Errorf("%s: want versions %d and %d in types/", id, before, before+1)
			continue
		}
		if got := keeps(now); got != "all" {
			t.Errorf("%s@%d start.older_versions = %v, want all", id, before+1, got)
		}
		// Take the one line away, and the version: the rest is the same.
		start, _ := now["start"].(map[string]any)
		delete(start, "older_versions")
		if len(start) == 0 {
			delete(now, "start")
		}
		delete(now, "version")
		delete(was, "version")
		if !reflect.DeepEqual(was, now) {
			t.Errorf("%s@%d differs from %s@%d in more than start.older_versions", id, before+1, id, before)
		}
	}
	for _, ty := range types {
		if n, isNew := newLatest[ty.ID]; isNew && ty.Version == n+1 {
			continue
		}
		if ty.ID == "ticket" && ty.Version == 9 {
			if got := keeps(stored[name(ty.ID, ty.Version)]); got != "all" {
				t.Errorf("ticket@9 start.older_versions = %v, want all, as ticket@8", got)
			}
			continue
		}
		// incident@6 (track 1 item 5, step 5) is incident@5 with waits_on
		// on one state: it carries incident@5's start block.
		if ty.ID == "incident" && ty.Version == 6 {
			if got := keeps(stored[name(ty.ID, ty.Version)]); got != "all" {
				t.Errorf("incident@6 start.older_versions = %v, want all, as incident@5", got)
			}
			continue
		}
		if got := keeps(stored[name(ty.ID, ty.Version)]); got != nil {
			t.Errorf("%s@%d has start.older_versions = %v; only the new latest versions should", ty.ID, ty.Version, got)
		}
	}
	// The newest version of every Type in types/ is the one this test expects.
	latest := map[string]int{}
	for _, ty := range types {
		latest[ty.ID] = max(latest[ty.ID], ty.Version)
	}
	for id, before := range newLatest {
		want := before + 1
		switch id {
		case "ticket":
			want = 9
		case "incident":
			want = 6
		}
		if latest[id] != want {
			t.Errorf("the latest %s in types/ is @%d, want @%d", id, latest[id], want)
		}
	}
	if latest["build_step"] != 2 {
		t.Errorf("the latest build_step in types/ is @%d, want @2 (it keeps no older version open)", latest["build_step"])
	}
}
