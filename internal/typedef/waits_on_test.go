// Tests for "sent back to the requester", at load (docs/roadmap.md, track
// 1, item 5, step 5; the owner's decisions of its Phase A): a state's
// `waits_on: requester` says that an Item in it waits on its requester,
// besides whoever its moves name. Refused at load:
//
//   - a value other than requester (INVALID_VALUE);
//   - on a state in category done or canceled, which never wait (CONFLICT);
//   - on a claimed state: a claimed Item waits on its claimant only
//     (CONFLICT);
//   - on the initial state: every new Item would wait on the person who
//     just made it (CONFLICT).
//
// The stored definition keeps the key; a Type without it stores nothing
// new.
package typedef_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// sentBackSrc is a Type whose request goes to review and may come back to
// its requester. BACK takes extra keys on the state `back`.
const sentBackSrc = `
id: sb
version: 1
initial_state: draft
fields:
  - {id: f_note,       type: string}
  - {id: f_claimed_by, type: principal}
states:
  - {id: draft,    category: todo}
  - {id: review,   category: waiting}
  - {id: back,     category: waiting BACK}
  - {id: approved, category: done}
  - {id: dropped,  category: canceled}
transitions:
  - {from: draft,  to: review,   allowed: [creator]}
  - {from: review, to: back,     allowed: ['role:sb_team']}
  - {from: review, to: approved, allowed: ['role:sb_team']}
  - {from: back,   to: review,   allowed: [creator]}
  - {from: back,   to: dropped,  allowed: [creator]}
  - {from: back,   to: draft,    allowed: ['role:sb_team'], release: RELEASE}
`

func sentBack(back string) string {
	release := "false"
	if strings.Contains(back, "claim:") {
		release = "true, clears: [f_claimed_by]"
	}
	return strings.NewReplacer(" BACK", back, "RELEASE", release).Replace(sentBackSrc)
}

func TestWaitsOn_Parse(t *testing.T) {
	ty := mustParse(t, sentBack(", waits_on: requester"))
	if st, _ := ty.State("back"); st.WaitsOn != typedef.WaitsOnRequester || typedef.WaitsOnRequester != "requester" {
		t.Errorf("back's waits_on = %q, want requester", st.WaitsOn)
	}
	if st, _ := ty.State("review"); st.WaitsOn != "" {
		t.Errorf("review's waits_on = %q, want none", st.WaitsOn)
	}
	data, err := json.Marshal(ty) // as the item service stores it
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if !strings.Contains(string(data), `"waits_on":"requester"`) || strings.Count(string(data), `"waits_on"`) != 1 {
		t.Errorf("stored definition %s does not keep waits_on once, on back", data)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON(%s): %v", data, err)
	}
	again, err := json.Marshal(back)
	if err != nil || string(again) != string(data) {
		t.Errorf("after the round trip the stored definition is\n  %s\nwant\n  %s", again, data)
	}
}

// A Type without the key stores nothing new: every registered version
// keeps its stored definition byte for byte.
func TestWaitsOn_AbsentStoresNothingNew(t *testing.T) {
	data, err := json.Marshal(mustParse(t, sentBack("")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"waits_on"`) {
		t.Errorf("stored definition %s names waits_on", data)
	}
}

// What loads: beside moves that name a role, on a state with no move for
// the requester at all (the incident's case: a reaction brings it back),
// with frozen and requester_only fields.
func TestWaitsOn_Loads(t *testing.T) {
	for name, src := range map[string]string{
		"on a waiting state":                         sentBack(", waits_on: requester"),
		"on an active state":                         strings.Replace(sentBack(", waits_on: requester"), "{id: back,     category: waiting", "{id: back,     category: active", 1),
		"with frozen and requester_only fields":      sentBack(", waits_on: requester, frozen: [f_claimed_by], requester_only: [f_note]"),
		"where no move names the requester":          strings.NewReplacer("{from: back,   to: review,   allowed: [creator]}", "{from: back,   to: review,   allowed: ['role:sb_team']}", "{from: back,   to: dropped,  allowed: [creator]}", "{from: back,   to: dropped}").Replace(sentBack(", waits_on: requester")),
		"in a Type whose other states wait on roles": sentBack(", waits_on: requester"),
	} {
		t.Run(name, func(t *testing.T) { mustParse(t, src) })
	}
}

func TestWaitsOn_Problems(t *testing.T) {
	cases := []struct {
		name, src, path string
		code            typedef.Code
		says            []string
	}{
		{"a value it does not know", sentBack(", waits_on: manager"),
			"states[2].waits_on", typedef.CodeInvalidValue, []string{"manager", "requester"}},
		{"on a done state", strings.Replace(sentBack(""), "{id: approved, category: done}", "{id: approved, category: done, waits_on: requester}", 1),
			"states[3].waits_on", typedef.CodeConflict, []string{"approved", "done"}},
		{"on a canceled state", strings.Replace(sentBack(""), "{id: dropped,  category: canceled}", "{id: dropped,  category: canceled, waits_on: requester}", 1),
			"states[4].waits_on", typedef.CodeConflict, []string{"dropped", "canceled"}},
		{"on a claimed state", sentBack(", waits_on: requester, claim: f_claimed_by"),
			"states[2].waits_on", typedef.CodeConflict, []string{"back", "claim"}},
		{"on the initial state", strings.Replace(sentBack(""), "{id: draft,    category: todo}", "{id: draft,    category: todo, waits_on: requester}", 1),
			"states[0].waits_on", typedef.CodeConflict, []string{"draft", "initial"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(c.src))
			wantOneProblem(t, err, c.code, c.path, c.says...)
		})
	}
}
