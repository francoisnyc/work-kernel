// Tests for proof-of-concept Step 2, part 1: the Type side of approvals as a
// business state. The kernel gains generic primitives only; nothing here is
// named "approval":
//
//   - a date field type (ISO 8601 calendar dates; a CEL timestamp in guards);
//   - writable_by: kernel, a field only the internal door may write;
//   - frozen: [fields] on a state, fields no write may change in it;
//   - field:<id> in `allowed`, matching the principal a principal field holds;
//   - creator in `allowed`, and `except: [creator]` (identity rules are
//     declared, never written in CEL);
//   - today as a guard variable;
//   - hook reads of the kernel-set attributes created_by and
//     created_on_behalf_of.
package typedef_test

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// approvalType is a small Type using every new attribute. Tests replace one
// marker to make it wrong in one way. FIELDS adds fields, STATE adds a
// state, ALLOWED is the decide transition's allowed list, GUARD its guard,
// HOOKS adds hooks.
const approvalType = `
id: leave
version: 1
initial_state: draft
fields:
  - {id: f_start,    type: date}
  - {id: f_end,      type: date}
  - {id: f_owner,    type: principal}
  - {id: f_approver, type: principal, writable_by: kernel}
  - {id: f_title,    type: string}
FIELDS
states:
  - {id: draft,   category: todo}
  - {id: pending, category: waiting, frozen: [f_start, f_end, f_approver]}
  - {id: done,    category: done}
STATE
transitions:
  - {from: draft, to: pending}
  - from: pending
    to: done
    allowed: ALLOWED
    guard: GUARD
hooks:
  - id: who_approves
    on: transition
    from: draft
    to: pending
    kind: check
    action: hr.manager_of
    reads: [created_on_behalf_of, created_by]
    writes: [f_approver]
HOOKS
`

// approval fills the markers; the defaults give a valid Type.
func approval(fields, state, allowed, guard, hooks string) string {
	if allowed == "" {
		allowed = `['field:f_approver']`
	}
	if guard == "" {
		guard = `'f_end >= f_start'`
	}
	r := strings.NewReplacer("FIELDS\n", fields, "STATE\n", state, "ALLOWED", allowed, "GUARD", guard,
		"HOOKS\n", hooks)
	return r.Replace(approvalType)
}

func mustParse(t *testing.T, src string) *typedef.Type {
	t.Helper()
	ty, err := typedef.Parse([]byte(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return ty
}

func mustRefuse(t *testing.T, what, src, mention string) {
	t.Helper()
	_, err := typedef.Parse([]byte(src))
	if err == nil {
		t.Fatalf("%s: accepted, want refused at load", what)
	}
	if mention != "" && !strings.Contains(err.Error(), mention) {
		t.Errorf("%s: error %q does not mention %q", what, err, mention)
	}
}

func TestApprovalType_Loads(t *testing.T) {
	ty := mustParse(t, approval("", "", "", "", ""))
	f, _ := ty.Field("f_approver")
	if f.WritableBy != typedef.WritableByKernel {
		t.Errorf("f_approver writable_by = %q, want kernel", f.WritableBy)
	}
	if f, _ := ty.Field("f_owner"); f.WritableBy != typedef.WritableByAnyone {
		t.Errorf("f_owner writable_by = %q, want anyone (empty)", f.WritableBy)
	}
	if f, _ := ty.Field("f_start"); f.Type != typedef.Date {
		t.Errorf("f_start type = %q, want date", f.Type)
	}
	s, _ := ty.State("pending")
	if !slices.Equal(s.Frozen, []string{"f_start", "f_end", "f_approver"}) {
		t.Errorf("pending frozen = %v", s.Frozen)
	}
}

// The Type is stored in Postgres as JSON and read back with FromJSON: the
// new attributes survive the trip.
func TestApprovalType_JSONRoundTrip(t *testing.T) {
	ty := mustParse(t, approval("", "", "", "", ""))
	data, err := json.Marshal(ty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	back, err := typedef.FromJSON(data)
	if err != nil {
		t.Fatalf("FromJSON: %v", err)
	}
	if f, _ := back.Field("f_approver"); f.WritableBy != typedef.WritableByKernel {
		t.Errorf("after the round trip, f_approver writable_by = %q", f.WritableBy)
	}
	if s, _ := back.State("pending"); len(s.Frozen) != 3 {
		t.Errorf("after the round trip, pending frozen = %v", s.Frozen)
	}
	if tr, _ := back.Transition("pending", "done"); !slices.Equal(tr.Allowed, []string{"field:f_approver"}) {
		t.Errorf("after the round trip, allowed = %v", tr.Allowed)
	}
}

// --- date ---

// A date is an ISO 8601 calendar date, YYYY-MM-DD, and a real one. Nothing
// else passes: not a timestamp, not another format, not a number.
func TestDateField_Values(t *testing.T) {
	ty := mustParse(t, approval("", "", "", "", ""))
	for _, ok := range []string{"2026-12-01", "2099-07-01", "2028-02-29"} {
		if err := ty.CheckFields(map[string]any{"f_start": ok}, false); err != nil {
			t.Errorf("date %q refused: %v", ok, err)
		}
	}
	for _, bad := range []any{"2026-13-01", "2026-02-30", "2027-02-29", "12/01/2026", "2026-12-1",
		"2026-12-01T00:00:00Z", "", float64(20261201), true} {
		err := ty.CheckFields(map[string]any{"f_start": bad}, false)
		var fe *typedef.FieldError
		if err == nil {
			t.Errorf("date %v accepted", bad)
			continue
		}
		if !errors.As(err, &fe) || fe.Field != "f_start" {
			t.Errorf("date %v: error %v does not name f_start", bad, err)
		}
	}
	// Clearing is an update's null, as for every type.
	if err := ty.CheckFields(map[string]any{"f_start": nil}, true); err != nil {
		t.Errorf("clearing a date refused: %v", err)
	}
}

// Guards compare dates with dates (CEL timestamps), and today is a date.
// Comparing a date with a string is a type error, caught at load.
func TestDateField_InGuards(t *testing.T) {
	mustParse(t, approval("", "", "", `'f_end >= f_start && f_start >= today'`, ""))
	mustRefuse(t, "date compared with a string", approval("", "", "", `'f_start >= "2026-01-01"'`, ""), "")
	mustRefuse(t, "today compared with an int", approval("", "", "", `'today > 3'`, ""), "")
}

// --- writable_by ---

func TestWritableBy_UnknownValueRefused(t *testing.T) {
	src := approval("  - {id: f_x, type: string, writable_by: hooks}\n", "", "", "", "")
	mustRefuse(t, "writable_by: hooks", src, "f_x")
}

// A field only the kernel may write, that no hook writes, could never be
// set: refused at load, naming the field. (Part 2: link reactions will count
// as writers too.)
func TestWritableBy_KernelFieldNothingWritesRefused(t *testing.T) {
	src := approval("  - {id: f_orphan, type: principal, writable_by: kernel}\n", "", "", "", "")
	mustRefuse(t, "kernel-written field without a writer", src, "f_orphan")

	// An enrich hook counts as a writer.
	src = approval("  - {id: f_plan, type: string, writable_by: kernel}\n", "", "", "",
		`  - {id: plan, on: enter, state: draft, kind: enrich, action: crm.get_plan, reads: [f_title], writes: [f_plan]}
`)
	mustParse(t, src)
}

// --- frozen ---

func TestFrozen_MustNameDeclaredFields(t *testing.T) {
	src := approval("", "  - {id: closed, category: canceled, frozen: [f_ghost]}\n", "", "", "")
	mustRefuse(t, "frozen unknown field", src, "f_ghost")
	src = approval("", "  - {id: closed, category: canceled, frozen: [f_start, f_start]}\n", "", "", "")
	mustRefuse(t, "frozen field listed twice", src, "f_start")
}

// --- field: in allowed ---

func TestAllowedField_MustNameAPrincipalField(t *testing.T) {
	mustParse(t, approval("", "", `['field:f_owner']`, "", ""))
	mustParse(t, approval("", "", `['field:f_approver', 'role:hr']`, "", ""))
	mustRefuse(t, "field: on a string field", approval("", "", `['field:f_title']`, "", ""), "f_title")
	mustRefuse(t, "field: on an undeclared field", approval("", "", `['field:f_ghost']`, "", ""), "f_ghost")
	mustRefuse(t, "field: naming nothing", approval("", "", `['field:']`, "", ""), "field:")
}

// A transition's patch may not write a field its own `allowed` names (at
// the door, FIELD_NOT_WRITABLE). A check's correction is that transition's
// patch, applied by ResolveHold: so a check on a transition may not write a
// field that transition's `allowed` names. Refused at load.
func TestAllowedField_CheckMayNotWriteIt(t *testing.T) {
	src := strings.Replace(approval("", "", "", "", ""), "transitions:\n  - {from: draft, to: pending}",
		"transitions:\n  - {from: draft, to: pending, allowed: ['field:f_approver']}", 1)
	mustRefuse(t, "check writes the field its transition's allowed names", src, "f_approver")
}

// --- reserved names and guard variables ---

// The guard variables that are not fields, and links, may not be field IDs.
func TestReservedNames_NotFieldIDs(t *testing.T) {
	for _, name := range []string{"caller", "caller_on_behalf_of", "created_by", "created_on_behalf_of", "today", "links"} {
		src := approval("  - {id: "+name+", type: string}\n", "", "", "", "")
		mustRefuse(t, "field named "+name, src, name)
	}
}

// today is the one guard variable that is not a field. Who acts is not a
// guard variable: caller, caller_on_behalf_of, created_by and
// created_on_behalf_of are refused in guards (and stay reserved names).
func TestGuardVariables_OnlyToday(t *testing.T) {
	mustParse(t, approval("", "", "", `'f_start >= today'`, ""))
	for _, guard := range []string{
		`caller != created_by`,
		`caller_on_behalf_of == null`,
		`created_on_behalf_of != null`,
		`created_by == "user:ana"`,
	} {
		mustRefuse(t, "guard "+guard, approval("", "", "", "'"+guard+"'", ""), "")
	}
}

// --- creator and except ---

// creator is an `allowed` entry, alone or with others. except takes only
// creator for now: anything else is refused at load, naming except.
func TestAllowedCreatorAndExcept(t *testing.T) {
	mustParse(t, approval("", "", `[creator]`, "", ""))
	mustParse(t, approval("", "", `[creator, 'role:hr', 'field:f_owner']`, "", ""))
	ty := mustParse(t, strings.Replace(approval("", "", "", "", ""), "allowed: ['field:f_approver']",
		"allowed: ['field:f_approver']\n    except: [creator]", 1))
	if tr, _ := ty.Transition("pending", "done"); !slices.Equal(tr.Except, []string{"creator"}) {
		t.Errorf("except = %v, want [creator]", tr.Except)
	}
	for _, entry := range []string{`'role:hr'`, `'principal:user:ana'`, `'field:f_owner'`, `creators`, `''`} {
		src := strings.Replace(approval("", "", "", "", ""), "allowed: ['field:f_approver']",
			"allowed: ['field:f_approver']\n    except: ["+entry+"]", 1)
		mustRefuse(t, "except "+entry, src, "except")
	}
	mustRefuse(t, "creator with a suffix in allowed", approval("", "", `['creator:ana']`, "", ""), "creator:ana")
}

// --- hook reads of the creator ---

// A hook may read created_by and created_on_behalf_of besides fields; a
// check reading only them is valid (they never change, so nothing needs
// freezing). It may not write them, and may not read an unknown name.
func TestHookReads_CreatorAttributes(t *testing.T) {
	ty := mustParse(t, approval("", "", "", "", ""))
	h, _ := ty.Hook("who_approves")
	if !slices.Equal(h.Reads, []string{typedef.AttrCreatedOnBehalfOf, typedef.AttrCreatedBy}) {
		t.Errorf("reads = %v", h.Reads)
	}
	writesCreator := `  - {id: bad, on: enter, state: done, kind: enrich, action: crm.get_plan, reads: [f_title], writes: [created_by]}
`
	mustRefuse(t, "a hook writing created_by", approval("", "", "", "", writesCreator), "created_by")
	readsGhost := `  - {id: bad, on: enter, state: done, kind: enrich, action: crm.get_plan, reads: [caller], writes: [f_title]}
`
	mustRefuse(t, "a hook reading caller", approval("", "", "", "", readsGhost), "caller")
}

// --- the vacation request ---

func TestVacationRequestV1(t *testing.T) {
	data, err := os.ReadFile("../../types/vacation_request.v1.yaml")
	if err != nil {
		t.Fatalf("read vacation_request.v1.yaml: %v", err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse vacation_request v1: %v", err)
	}
	if ty.ID != "vacation_request" || ty.Version != 1 || ty.InitialState != "draft" {
		t.Errorf("Type = %s v%d starting in %q", ty.ID, ty.Version, ty.InitialState)
	}
	wantFields := map[string]typedef.FieldType{
		"f_start": typedef.Date, "f_end": typedef.Date, "f_days": typedef.Int,
		"f_leave_type": typedef.Enum, "f_approver": typedef.Principal, "f_comment": typedef.String,
	}
	for id, ft := range wantFields {
		f, ok := ty.Field(id)
		if !ok || f.Type != ft {
			t.Errorf("field %s = %+v, want type %s", id, f, ft)
		}
		want := typedef.WritableByAnyone
		if id == "f_approver" {
			want = typedef.WritableByKernel
		}
		if f.WritableBy != want {
			t.Errorf("field %s writable_by = %q, want %q", id, f.WritableBy, want)
		}
	}
	if f, _ := ty.Field("f_leave_type"); !slices.Equal(f.Options, []string{"annual", "unpaid", "other"}) {
		t.Errorf("f_leave_type options = %v", f.Options)
	}

	terms := []string{"f_start", "f_end", "f_days", "f_leave_type", "f_approver"}
	wantStates := map[string]string{
		"draft": "todo", "submitted": "waiting", "approved": "done", "rejected": "canceled", "withdrawn": "canceled",
	}
	for id, cat := range wantStates {
		s, ok := ty.State(id)
		if !ok || s.Category != cat {
			t.Errorf("state %s = %+v, want category %s", id, s, cat)
		}
		var want []string
		if id == "submitted" || id == "approved" || id == "rejected" {
			want = terms
		}
		if !slices.Equal(s.Frozen, want) {
			t.Errorf("state %s frozen = %v, want %v", id, s.Frozen, want)
		}
	}

	// Identity is declared; guards hold data rules only.
	for _, to := range []string{"approved", "rejected"} {
		tr, ok := ty.Transition("submitted", to)
		if !ok || !slices.Equal(tr.Allowed, []string{"field:f_approver"}) ||
			!slices.Equal(tr.Except, []string{"creator"}) || tr.Guard != "" {
			t.Errorf("submitted -> %s = %+v, want allowed [field:f_approver], except [creator], no guard", to, tr)
		}
	}
	submit, _ := ty.Transition("draft", "submitted")
	if !slices.Equal(submit.Required, []string{"f_start", "f_end", "f_days", "f_leave_type"}) ||
		!slices.Equal(submit.Allowed, []string{"creator"}) || len(submit.Except) != 0 ||
		submit.Guard != "f_end >= f_start && f_start >= today && f_days > 0" {
		t.Errorf("submit = %+v, want allowed [creator], the terms required, the date and days guard", submit)
	}
	if tr, ok := ty.Transition("draft", "withdrawn"); !ok || !tr.Escape {
		t.Errorf("draft -> withdrawn = %+v, want an escape", tr)
	}
	for _, mv := range [][2]string{{"submitted", "draft"}, {"submitted", "withdrawn"}, {"draft", "withdrawn"}} {
		tr, ok := ty.Transition(mv[0], mv[1])
		if !ok || !slices.Equal(tr.Allowed, []string{"creator"}) || tr.Guard != "" {
			t.Errorf("%s -> %s = %+v, want allowed [creator], no guard", mv[0], mv[1], tr)
		}
	}
	if _, ok := ty.Transition("rejected", "draft"); ok {
		t.Error("rejected -> draft declared; rejected is terminal")
	}

	h, ok := ty.Hook("hr_check")
	if !ok || h.Kind != typedef.HookCheck || h.From != "draft" || h.To != "submitted" || h.Action != "hr.manager_of" ||
		!slices.Equal(h.Reads, []string{"created_on_behalf_of", "created_by"}) ||
		!slices.Equal(h.Writes, []string{"f_approver"}) {
		t.Errorf("hr_check = %+v", h)
	}
}
