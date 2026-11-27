// Tests for presentation hints over the wire (the Inbox for people, PR 1a;
// the owner's decisions of its Phase A, 2026-11-27). kernel.v1 grows by
// additive fields only: Field.label (9) and help (10); State.label (8),
// help (9) and due (10); Transition.label (11), help (12) and primary
// (13); Type.summary (20). Empty means none. GetType and ListTypes carry
// them; a hint a Type does not declare is sent empty.
//
// The file's name sorts after types_test.go: it registers Types when it
// runs, after TestListTypes_EveryVersionInOrder has listed them.
package api_test

import (
	"slices"
	"sync"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const apiWordsSrc = `
id: apiwords
version: 1
initial_state: open
summary: [f_title, f_approver]
fields:
  - {id: f_title,    type: string,    label: Request, help: What the person asks for}
  - {id: f_approver, type: principal, label: Approver}
  - {id: f_due_at,   type: datetime}
states:
  - {id: open,     category: waiting, label: Waiting for a decision, help: The approver decides, due: f_due_at}
  - {id: approved, category: done,    label: Approved}
  - {id: refused,  category: done}
transitions:
  - {from: open, to: approved, allowed: ['field:f_approver'], label: Approve, help: The request goes ahead, primary: true}
  - {from: open, to: refused,  allowed: ['field:f_approver']}
`

const apiWordsV1 = "namespaces/acme/types/apiwords/versions/1"

var apiWordsOnce sync.Once

func apiWordsSetup(t *testing.T) {
	t.Helper()
	apiWordsOnce.Do(func() {
		ty, err := typedef.Parse([]byte(apiWordsSrc))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := k.RegisterType(testCtx(t), ty); err != nil {
			t.Fatalf("register %s: %v", ty.ID, err)
		}
	})
}

// wantWords checks every hint of apiwords@1 on the Type as sent.
func wantWords(t *testing.T, who string, ty *kernelv1.Type) {
	t.Helper()
	if !slices.Equal(ty.Summary, []string{"f_title", "f_approver"}) {
		t.Errorf("%s: summary = %v, want [f_title f_approver]", who, ty.Summary)
	}
	fields := map[string][2]string{
		"f_title":    {"Request", "What the person asks for"},
		"f_approver": {"Approver", ""},
		"f_due_at":   {"", ""},
	}
	for _, f := range ty.Fields {
		if got, want := [2]string{f.Label, f.Help}, fields[f.Id]; got != want {
			t.Errorf("%s: field %s label, help = %q, want %q", who, f.Id, got, want)
		}
	}
	states := map[string][3]string{
		"open":     {"Waiting for a decision", "The approver decides", "f_due_at"},
		"approved": {"Approved", "", ""},
		"refused":  {"", "", ""},
	}
	for _, s := range ty.States {
		if got, want := [3]string{s.Label, s.Help, s.Due}, states[s.Id]; got != want {
			t.Errorf("%s: state %s label, help, due = %q, want %q", who, s.Id, got, want)
		}
	}
	type move struct {
		label, help string
		primary     bool
	}
	moves := map[string]move{
		"open>approved": {"Approve", "The request goes ahead", true},
		"open>refused":  {},
	}
	for _, tr := range ty.Transitions {
		if got, want := (move{tr.Label, tr.Help, tr.Primary}), moves[tr.FromState+">"+tr.ToState]; got != want {
			t.Errorf("%s: move %s -> %s label, help, primary = %+v, want %+v", who, tr.FromState, tr.ToState, got, want)
		}
	}
}

func TestGetType_CarriesHints(t *testing.T) {
	apiWordsSetup(t)
	res, err := as("ana").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: apiWordsV1}))
	if err != nil {
		t.Fatalf("GetType: %v", err)
	}
	wantWords(t, "ana's GetType", res.Msg.Type)
	// A customer reads it too: Types are not secret, and hints are words
	// of the Type, never of an Item.
	res, err = asCustomer("kim", "").GetType(testCtx(t), connect.NewRequest(&kernelv1.GetTypeRequest{Name: apiWordsV1}))
	if err != nil {
		t.Fatalf("kim's GetType: %v", err)
	}
	wantWords(t, "kim's GetType", res.Msg.Type)
}

func TestListTypes_CarriesHints(t *testing.T) {
	apiWordsSetup(t)
	res, err := as("ana").ListTypes(testCtx(t), connect.NewRequest(&kernelv1.ListTypesRequest{Parent: "namespaces/acme"}))
	if err != nil {
		t.Fatalf("ListTypes: %v", err)
	}
	i := slices.IndexFunc(res.Msg.Types, func(ty *kernelv1.Type) bool { return ty.Name == apiWordsV1 })
	if i < 0 {
		t.Fatalf("ListTypes does not list %s", apiWordsV1)
	}
	wantWords(t, "ListTypes", res.Msg.Types[i])
}
