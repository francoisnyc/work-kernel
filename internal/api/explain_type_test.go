// Tests for Ventoux item 6: ExplainType reads a Type version in plain
// language, a registered one by name or a draft from sources (checked as
// ValidateTypes checks them: a draft with problems gets its problems and
// no reading). It reads no Item, so nothing goes through policy.Project;
// users and agents (in a session) may call it, customers and agents acting
// for one may not. The reading is internal/reading's, carried as it is.
package api_test

import (
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
	"github.com/francoisnyc/work-kernel/internal/contracts"
	"github.com/francoisnyc/work-kernel/internal/reading"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func explainType(c kernelv1connect.ItemServiceClient, t *testing.T, req *kernelv1.ExplainTypeRequest) (*kernelv1.ExplainTypeResponse, error) {
	t.Helper()
	res, err := c.ExplainType(testCtx(t), connect.NewRequest(req))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func approvedReading(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../readings/" + name + ".txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func repoType(t *testing.T, id string, version int) *typedef.Type {
	t.Helper()
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatal(err)
	}
	for _, ty := range types {
		if ty.ID == id && ty.Version == version {
			return ty
		}
	}
	t.Fatalf("no %s@%d in types/", id, version)
	return nil
}

// A registered version: the reading internal/reading makes, every
// sentence with its number, kind, parts and paths, the key, the fields,
// the actions from their contracts, and the text, which is the approved
// one.
func TestExplainType_ARegisteredVersion(t *testing.T) {
	res, err := explainType(as("ana"), t, &kernelv1.ExplainTypeRequest{Name: "namespaces/acme/types/vacation_request/versions/3"})
	if err != nil {
		t.Fatalf("ExplainType: %v", err)
	}
	if len(res.Problems) != 0 {
		t.Errorf("problems = %v, want none", res.Problems)
	}
	got := res.Reading
	if got.GetType() != "namespaces/acme/types/vacation_request/versions/3" {
		t.Errorf("type = %q", got.GetType())
	}
	if got.GetText() != approvedReading(t, "vacation_request@3") {
		t.Errorf("text =\n%s\nwant readings/vacation_request@3.txt", got.GetText())
	}
	want := reading.Read(repoType(t, "vacation_request", 3), contracts.Default())
	if len(got.Sections) != len(want.Sections) {
		t.Fatalf("%d sections, want %d", len(got.Sections), len(want.Sections))
	}
	for i, sec := range want.Sections {
		g := got.Sections[i]
		if g.Title != sec.Title || len(g.Sentences) != len(sec.Sentences) {
			t.Errorf("section %d = %q with %d sentences, want %q with %d", i, g.Title, len(g.Sentences), sec.Title, len(sec.Sentences))
			continue
		}
		for j, s := range sec.Sentences {
			gs := g.Sentences[j]
			parts := make([]string, len(s.Parts))
			for k, p := range s.Parts {
				parts[k] = string(p)
			}
			if int(gs.Number) != s.N || gs.Kind != string(s.Kind) || gs.Text != s.Text ||
				!reflect.DeepEqual(gs.Parts, parts) || !reflect.DeepEqual(gs.Paths, s.Paths) {
				t.Errorf("sentence %d = %v, want %+v", s.N, gs, s)
			}
		}
	}
	if len(got.Key) != len(want.Key) || len(got.Key) == 0 || got.Key[0].Name != want.Key[0].Name || got.Key[0].Meaning != want.Key[0].Meaning {
		t.Errorf("key = %v, want %+v", got.Key, want.Key)
	}
	if !reflect.DeepEqual(got.Fields, want.Fields) || !reflect.DeepEqual(got.NotCovered, want.NotCovered) {
		t.Errorf("fields %v, not covered %v; want %v, %v", got.Fields, got.NotCovered, want.Fields, want.NotCovered)
	}
	if len(got.Actions) != 2 {
		t.Fatalf("actions = %v, want hr.manager_of@1 and email.notify@1", got.Actions)
	}
	hr, mail := got.Actions[0], got.Actions[1]
	contract, _ := contracts.Default().Lookup("hr.manager_of@1")
	if hr.Action != "hr.manager_of@1" || hr.SideEffects || hr.Risk != kernelv1.ActionRisk_ACTION_RISK_LOW ||
		!reflect.DeepEqual(hr.Kinds, []kernelv1.HookKind{kernelv1.HookKind_HOOK_KIND_CHECK}) || hr.Description != contract.Description {
		t.Errorf("the first action = %v", hr)
	}
	if mail.Action != "email.notify@1" || !mail.SideEffects || mail.Risk != kernelv1.ActionRisk_ACTION_RISK_MEDIUM {
		t.Errorf("the second action = %v", mail)
	}
}

// The reading is the same in every namespace; its name is the caller's.
func TestExplainType_EveryNamespace(t *testing.T) {
	res, err := explainType(as("bea"), t, &kernelv1.ExplainTypeRequest{Name: "namespaces/globex/types/incident/versions/3"})
	if err != nil {
		t.Fatalf("ExplainType: %v", err)
	}
	if res.Reading.GetType() != "namespaces/globex/types/incident/versions/3" || res.Reading.GetText() != approvedReading(t, "incident@3") {
		t.Errorf("reading = %s\n%s", res.Reading.GetType(), res.Reading.GetText())
	}
}

// A draft is checked against every registered version, as ValidateTypes
// checks it; a sound one is read, named as it would be.
func TestExplainType_ADraft(t *testing.T) {
	src := strings.Replace(readType(t, "incident.v3.yaml"), "\nversion: 3\n", "\nversion: 9\n", 1)
	res, err := explainType(as("ana"), t, &kernelv1.ExplainTypeRequest{Parent: "namespaces/acme",
		Sources: []*kernelv1.TypeSource{tsrc("drafts/incident.v9.yaml", src)}, TypeId: "incident", Version: 9})
	if err != nil {
		t.Fatalf("ExplainType: %v", err)
	}
	if len(res.Problems) != 0 {
		t.Fatalf("problems:\n%s", problemLines(res.Problems))
	}
	want := strings.Replace(approvedReading(t, "incident@3"), "incident@3\n", "incident@9\n", 1)
	if res.Reading.GetType() != "namespaces/acme/types/incident/versions/9" || res.Reading.GetText() != want {
		t.Errorf("reading = %s\n%s", res.Reading.GetType(), res.Reading.GetText())
	}
	// A draft identical to a registered version is read like it.
	res, err = explainType(as("ana"), t, &kernelv1.ExplainTypeRequest{Parent: "namespaces/acme",
		Sources: []*kernelv1.TypeSource{tsrc("types/vacation_request.v3.yaml", readType(t, "vacation_request.v3.yaml"))},
		TypeId:  "vacation_request", Version: 3})
	if err != nil {
		t.Fatalf("ExplainType: %v", err)
	}
	if res.Reading.GetText() != approvedReading(t, "vacation_request@3") {
		t.Errorf("the identical draft reads\n%s", res.Reading.GetText())
	}
}

// A draft with problems is a successful call that lists them, as
// ValidateTypes lists them, with no reading: a reading is only ever of a
// Type that loads.
func TestExplainType_ADraftWithProblems(t *testing.T) {
	src := strings.Replace(readType(t, "incident.v3.yaml"), "\nversion: 3\n", "\nversion: 9\n", 1)
	src = strings.Replace(src, "customer_fields: [f_title,", "customer_fields: [f_nope, f_title,", 1)
	srcs := []*kernelv1.TypeSource{tsrc("drafts/incident.v9.yaml", src)}
	res, err := explainType(as("ana"), t, &kernelv1.ExplainTypeRequest{Parent: "namespaces/acme", Sources: srcs, TypeId: "incident", Version: 9})
	if err != nil {
		t.Fatalf("ExplainType: %v", err)
	}
	if res.Reading != nil {
		t.Errorf("a reading of a draft with problems:\n%s", res.Reading.Text)
	}
	if want := problemsAs(t, "ana", srcs...); problemLines(res.Problems) != problemLines(want) || len(want) == 0 {
		t.Errorf("problems =\n%s\nwant ValidateTypes's\n%s", problemLines(res.Problems), problemLines(want))
	}
}

// What is asked must be one thing: a registered version by name, or a
// draft version among the sources.
func TestExplainType_Arguments(t *testing.T) {
	draft := []*kernelv1.TypeSource{tsrc("memo.yaml", "id: memo\nversion: 1\n")}
	cases := []struct {
		what  string
		req   *kernelv1.ExplainTypeRequest
		field string
	}{
		{"nothing", &kernelv1.ExplainTypeRequest{}, "name"},
		{"both a name and a draft", &kernelv1.ExplainTypeRequest{Name: "namespaces/acme/types/memo/versions/1",
			Parent: "namespaces/acme", Sources: draft, TypeId: "memo", Version: 1}, "name"},
		{"a malformed name", &kernelv1.ExplainTypeRequest{Name: "types/memo"}, "name"},
		{"a draft without its version", &kernelv1.ExplainTypeRequest{Parent: "namespaces/acme", Sources: draft, TypeId: "memo"}, "version"},
		{"a draft without its Type", &kernelv1.ExplainTypeRequest{Parent: "namespaces/acme", Sources: draft, Version: 1}, "type_id"},
		{"a version the sources do not have", &kernelv1.ExplainTypeRequest{Parent: "namespaces/acme", Sources: draft,
			TypeId: "memo", Version: 2}, "type_id"},
		{"a draft without sources", &kernelv1.ExplainTypeRequest{Parent: "namespaces/acme", TypeId: "memo", Version: 1}, "sources"},
	}
	for _, c := range cases {
		_, err := explainType(as("ana"), t, c.req)
		invalid(t, c.what, err, c.field)
	}
	// The draft limits are ValidateTypes's.
	many := make([]*kernelv1.TypeSource, 51)
	for i := range many {
		many[i] = tsrc(fmt.Sprintf("t%d.yaml", i), fmt.Sprintf("id: t%d\nversion: 1\n", i))
	}
	_, err := explainType(as("ana"), t, &kernelv1.ExplainTypeRequest{Parent: "namespaces/acme", Sources: many, TypeId: "t0", Version: 1})
	invalid(t, "51 sources", err, "sources")
	big := tsrc("big.yaml", "id: big\nversion: 1\n# "+strings.Repeat("x", maxSourceBytes)+"\n")
	_, err = explainType(as("ana"), t, &kernelv1.ExplainTypeRequest{Parent: "namespaces/acme",
		Sources: []*kernelv1.TypeSource{big}, TypeId: "big", Version: 1})
	invalid(t, "more than 256 KiB", err, "sources")
	// A version nobody registered, and another namespace, are not found.
	_, err = explainType(as("ana"), t, &kernelv1.ExplainTypeRequest{Name: "namespaces/acme/types/vacation_request/versions/99"})
	notFound(t, "an unknown version", err)
	_, err = explainType(as("ana"), t, &kernelv1.ExplainTypeRequest{Name: "namespaces/globex/types/vacation_request/versions/3"})
	notFound(t, "another namespace", err)
	_, err = explainType(as("ana"), t, &kernelv1.ExplainTypeRequest{Parent: "namespaces/globex", Sources: draft, TypeId: "memo", Version: 1})
	notFound(t, "a draft in another namespace", err)
}

// Users and agents in a session may call it, with no particular power;
// customers, and agents acting for one, may not: a reading names the
// organisation's integrations, as ListActions does.
func TestExplainType_WhoMayCall(t *testing.T) {
	req := &kernelv1.ExplainTypeRequest{Name: "namespaces/acme/types/vacation_request/versions/3"}
	for what, c := range map[string]kernelv1connect.ItemServiceClient{
		"a user":                            as("bob"),
		"an agent for a user, comment only": asAgent("bot", startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "comment")),
	} {
		if _, err := explainType(c, t, req); err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
	for what, c := range map[string]kernelv1connect.ItemServiceClient{
		"a customer":              asCustomer("kim", ""),
		"an agent for a customer": asCustomer("portal-bot", portalSession(t)),
	} {
		_, err := explainType(c, t, req)
		apiError(t, what, err, connect.CodePermissionDenied, "NOT_ALLOWED")
	}
}
