// Tests for a claim that binds, at load (docs/roadmap.md, track 1, item 5,
// step 2; the owner's decisions of its Phase A): a state's
// `claim: <field>` names the principal field that holds who claimed the
// Item, and a transition's `release: true` marks the move that gives the
// claim up. Refused at load, where nobody could claim, where the claim
// would strand an Item, or where the Type contradicts itself:
//
//   - the claim field: not declared (UNKNOWN_FIELD); not a principal, or
//     holding several (WRONG_FIELD_TYPE); only the kernel writes it
//     (WRITES_KERNEL_FIELD); the same state also freezes it or keeps it
//     for the requester (CONFLICT);
//   - the claimed state: the initial state (CONFLICT: nobody moves an Item
//     into it); no release out of it (MISSING_COUNTERPART: a claimant who
//     leaves would strand the Item), and a move that only clears the
//     field is no release;
//   - a move into it, which a person must make: held by a check or a held
//     reaction (HELD_TRANSITION, Later); a timer's or an at-timer's
//     (TIMER_NOT_ALLOWED); naming the claim field in given or clears
//     (CONFLICT: the kernel records the mover); a reaction's, the Type's
//     own or another Type's (CONFLICT, on the reaction, as for a held
//     target);
//   - a release: out of a state with no claim (KEY_NOT_APPLICABLE); that
//     does not clear the claim field (MISSING_COUNTERPART).
//
// The stored definition keeps both keys; a Type without them stores
// nothing new.
package typedef_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// claimSrc is a Type with a queue and a review. Each marker takes extra
// keys (YAML flow, inside the braces): DRAFT, QUEUE and REVIEW on the
// states; CLAIM on queue -> review, RELEASE on review -> queue, DECIDE on
// review -> done.
const claimSrc = `
id: cm
version: 1
initial_state: draft
fields:
  - {id: f_amount,     type: int}
  - {id: f_note,       type: string}
  - {id: f_claimed_by, type: principal}
  - {id: f_reviewers,  type: principal, multiple: true}
  - {id: f_stamp,      type: principal, writable_by: kernel}
  - {id: f_end_at,     type: datetime}
states:
  - {id: draft,  category: todo DRAFT}
  - {id: queue,  category: waiting QUEUE}
  - {id: review, category: active REVIEW}
  - {id: done,   category: done}
transitions:
  - {from: draft,  to: queue, sets: {f_stamp: 'f_claimed_by'}}
  - {from: queue,  to: review CLAIM}
  - {from: review, to: queue RELEASE}
  - {from: review, to: done DECIDE}
`

const (
	claimed  = ", claim: f_claimed_by"
	released = ", release: true, clears: [f_claimed_by]"
)

// claim fills claimSrc: the review state's keys, then the claiming move's,
// the release's and the decision's.
func claim(review, claimMove, release, decide string) string {
	return strings.NewReplacer(" DRAFT", "", " QUEUE", "", " REVIEW", review, " CLAIM", claimMove,
		" RELEASE", release, " DECIDE", decide).Replace(claimSrc)
}

func TestClaim_Parse(t *testing.T) {
	ty := mustParse(t, claim(claimed, "", released, ""))
	if st, _ := ty.State("review"); st.Claim != "f_claimed_by" {
		t.Errorf("review's claim = %q, want f_claimed_by", st.Claim)
	}
	if tr, _ := ty.Transition("review", "queue"); !tr.Release {
		t.Errorf("review -> queue is not a release")
	}
	if tr, _ := ty.Transition("review", "done"); tr.Release {
		t.Errorf("review -> done is a release")
	}
	data, err := json.Marshal(ty) // as the item service stores it
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"claim":"f_claimed_by"`, `"release":true`} {
		if !strings.Contains(string(data), want) {
			t.Errorf("stored definition %s does not keep %s", data, want)
		}
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

// A Type without claim or release stores nothing new: every registered
// version keeps its stored definition byte for byte.
func TestClaim_AbsentStoresNothingNew(t *testing.T) {
	data, err := json.Marshal(mustParse(t, claim("", "", ", clears: [f_claimed_by]", "")))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"claim"`) || strings.Contains(string(data), `"release"`) {
		t.Errorf("stored definition %s names claim or release", data)
	}
}

// What loads.
func TestClaim_Loads(t *testing.T) {
	for name, src := range map[string]string{
		"a claim and its release":                       claim(claimed, "", released, ""),
		"the claiming move requires the field":          claim(claimed, ", required: [f_claimed_by]", released, ""),
		"the claiming move names who may":               claim(claimed, ", allowed: ['role:finance'], except: [creator]", released, ""),
		"a release that others may make":                claim(claimed, "", released+", allowed: ['role:finance', 'role:lead']", ""),
		"a decision that clears the claim: no release":  claim(claimed, "", released, ", clears: [f_claimed_by]"),
		"a decision that names the claimant in allowed": claim(claimed, "", released, ", allowed: ['field:f_claimed_by']"),
		"the claim field written by a role":             strings.Replace(claim(claimed, "", released, ""), "{id: f_claimed_by, type: principal}", "{id: f_claimed_by, type: principal, writable_by: ['role:finance']}", 1),
		"a timer out of the claimed state":              claim(claimed+", timer: {after: 48h, to: done}", "", released, ", allowed: ['principal:system:timer']"),
		"a timer's release out of the claimed state":    claim(claimed+", timer: {after: 48h, to: queue}", "", released+", allowed: ['role:finance', 'principal:system:timer']", ""),
		"the claimed state freezes other fields":        claim(claimed+", frozen: [f_amount]", "", released, ""),
		"clears the field out of a state with no claim": claim("", "", ", clears: [f_claimed_by]", ""),
	} {
		t.Run(name, func(t *testing.T) { mustParse(t, src) })
	}
}

func TestClaim_Problems(t *testing.T) {
	cases := []struct {
		name, src, path string
		code            typedef.Code
		says            []string
	}{
		// The field.
		{"a field it does not declare", claim(", claim: f_nope", "", released, ""),
			"states[2].claim", typedef.CodeUnknownField, []string{"f_nope", "review"}},
		{"a field that is no principal", claim(", claim: f_note", "", ", release: true, clears: [f_note]", ""),
			"states[2].claim", typedef.CodeWrongFieldType, []string{"f_note", "principal"}},
		{"a field holding several people", claim(", claim: f_reviewers", "", ", release: true, clears: [f_reviewers]", ""),
			"states[2].claim", typedef.CodeWrongFieldType, []string{"f_reviewers", "one person"}},
		{"a field only the kernel writes", claim(", claim: f_stamp", "", ", release: true", ""),
			"states[2].claim", typedef.CodeWritesKernelField, []string{"f_stamp", "kernel"}},
		{"a field the state also keeps for the requester", claim(claimed+", requester_only: [f_claimed_by]", "", released, ""),
			"states[2].claim", typedef.CodeConflict, []string{"f_claimed_by", "requester_only"}},

		// The state.
		{"the initial state", strings.Replace(claim(claimed, "", released, ""), "{id: draft,  category: todo}", "{id: draft,  category: todo, claim: f_claimed_by}", 1),
			"states[0].claim", typedef.CodeConflict, []string{"draft", "initial"}},
		{"no release out of it", claim(claimed, "", "", ""),
			"states[2].claim", typedef.CodeMissingCounterpart, []string{"review", "release"}},
		{"a move that only clears the field is no release", claim(claimed, "", ", clears: [f_claimed_by]", ""),
			"states[2].claim", typedef.CodeMissingCounterpart, []string{"review", "release"}},

		// A release.
		{"a release that does not clear the claim field", claim(claimed, "", ", release: true", ""),
			"transitions[2].release", typedef.CodeMissingCounterpart, []string{"f_claimed_by", "clear"}},
		{"a release that clears another field", claim(claimed, "", ", release: true, clears: [f_note]", ""),
			"transitions[2].release", typedef.CodeMissingCounterpart, []string{"f_claimed_by", "clear"}},
		{"a release out of a state with no claim", claim(claimed, ", release: true", released, ""),
			"transitions[1].release", typedef.CodeKeyNotApplicable, []string{"queue", "claim"}},
		{"a release in a Type with no claim", claim("", "", released, ""),
			"transitions[2].release", typedef.CodeKeyNotApplicable, []string{"review", "claim"}},

		// The move that claims.
		{"it asks for the claim field to be given", claim(claimed, ", required: [f_claimed_by], given: [f_claimed_by]", released, ""),
			"transitions[1].given[0]", typedef.CodeConflict, []string{"f_claimed_by", "records"}},
		{"it clears the claim field", claim(claimed, ", clears: [f_claimed_by]", released, ""),
			"transitions[1].clears[0]", typedef.CodeConflict, []string{"f_claimed_by", "records"}},
		{"the state's timer makes it",
			strings.Replace(claim(claimed, ", allowed: ['principal:system:timer']", released, ""),
				"{id: queue,  category: waiting}", "{id: queue,  category: waiting, timer: {after: 48h, to: review}}", 1),
			"transitions[1]", typedef.CodeTimerNotAllowed, []string{"timer", "review", "claim"}},
		{"the state's at-timer makes it",
			strings.Replace(claim(claimed, ", allowed: ['principal:system:timer']", released, ""),
				"{id: queue,  category: waiting}", "{id: queue,  category: waiting, at: {field: f_end_at, to: review}}", 1),
			"transitions[1]", typedef.CodeTimerNotAllowed, []string{"timer", "review", "claim"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := typedef.Parse([]byte(c.src))
			wantOneProblem(t, err, c.code, c.path, c.says...)
		})
	}
}

// A claimed state already keeps its claim field from every write: listing
// it in frozen as well is refused. (The release, which must clear the
// field, is then refused too, as any move that clears a field the state it
// leaves freezes: two problems, so this case stands apart.)
func TestClaim_TheClaimFieldMayNotAlsoBeFrozen(t *testing.T) {
	_, err := typedef.Parse([]byte(claim(claimed+", frozen: [f_amount, f_claimed_by]", "", released, "")))
	found := false
	for _, p := range problemsOf(t, err) {
		if p.Code == typedef.CodeConflict && p.Path == "states[2].claim" &&
			strings.Contains(p.Message, "f_claimed_by") && strings.Contains(p.Message, "frozen") {
			found = true
		}
	}
	if !found {
		t.Errorf("problems = %v, want a CONFLICT at states[2].claim saying f_claimed_by is also frozen", err)
	}
}

// A held move may not claim: by a check, or by a held reaction (supporting
// it is Later, as for given).
func TestClaim_RefusedOnAHeldMove(t *testing.T) {
	check := claim(claimed, "", released, "") +
		"hooks:\n  - {id: recheck, on: transition, from: queue, to: review, kind: check, action: erp.check_credit, reads: [f_note, f_amount], writes: [f_note]}\n"
	_, err := typedef.Parse([]byte(check))
	wantOneProblem(t, err, typedef.CodeHeldTransition, "transitions[1]", "recheck", "claim")

	// pledge's filed -> paid is held by the reaction take: paid may not be
	// claimed.
	held := swap(t, pledgeSrc, "  - {id: f_note,   type: string}", "  - {id: f_note,   type: string}\n  - {id: f_who,    type: principal}")
	held = swap(t, held, "  - {id: paid,    category: done}", "  - {id: paid,    category: done, claim: f_who}")
	held = swap(t, held, "  - {from: paid,  to: dropped}", "  - {from: paid,  to: dropped, release: true, clears: [f_who]}")
	_, err = typedef.Parse([]byte(held))
	wantOneProblem(t, err, typedef.CodeHeldTransition, "transitions[1]", "take", "claim")
}

// A reaction claims nothing: it may not move an Item into a claimed state.
// The Type's own (created_inside, moving the container) and another Type's
// (moving its target), each refused on the reaction, where a held target
// transition is.
func TestClaim_RefusedOnAReactionsMove(t *testing.T) {
	good := map[string]string{
		"desk.yaml": desk(reopenR, checklistR), "remark.yaml": remarkSrc, "todo.yaml": todoSrc,
		"part.yaml": part(finishR), "whole.yaml": whole(wholeAccepts),
	}
	if err := parseSet(t, good); err != nil {
		t.Fatalf("the five Types together: %v", err)
	}
	// desk's working state claimed by f_assignee, released on the way to
	// waiting: the reaction reopen moves a desk from waiting into working.
	claimedDesk := swap(t, desk(reopenR, checklistR), "  - {id: working, category: active}", "  - {id: working, category: active, claim: f_assignee}")
	claimedDesk = swap(t, claimedDesk, "  - {from: working, to: waiting}", "  - {from: working, to: waiting, release: true, clears: [f_assignee]}")
	// whole's done state claimed: part's reaction finish moves a whole into it.
	claimedWhole := swap(t, whole(wholeAccepts), "  - {id: f_name, type: string}", "  - {id: f_name, type: string}\n  - {id: f_who, type: principal}")
	claimedWhole = swap(t, claimedWhole, "  - {id: done,     category: done}", "  - {id: done,     category: done, claim: f_who}")
	claimedWhole = swap(t, claimedWhole, "  - {from: building, to: done, allowed: ['role:lead']}",
		"  - {from: building, to: done, allowed: ['role:lead']}\n  - {from: done, to: building, release: true, clears: [f_who]}")
	cases := []struct {
		name, file, src, problemFile, path string
		says                               []string
	}{
		{"the Type's own reaction moves it", "desk.yaml", claimedDesk, "desk.yaml", "reactions[0].to", []string{"reopen", "working", "claim"}},
		{"another Type's reaction moves it", "whole.yaml", claimedWhole, "part.yaml", "reactions[0].to", []string{"finish", "done", "claim"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			files := map[string]string{}
			for k, v := range good {
				files[k] = v
			}
			files[c.file] = c.src
			ps := problemsOf(t, parseSet(t, files))
			if len(ps) != 1 {
				t.Fatalf("problems = %v, want one", ps)
			}
			p := ps[0]
			if p.Code != typedef.CodeConflict || p.Path != c.path || !strings.HasSuffix(p.Source, c.problemFile) {
				t.Errorf("problem = %s at %s in %s, want CONFLICT at %s in %s (%s)", p.Code, p.Path, p.Source, c.path, c.problemFile, p.Message)
			}
			for _, s := range c.says {
				if !strings.Contains(p.Message, s) {
					t.Errorf("message %q does not say %q", p.Message, s)
				}
			}
		})
	}
}
