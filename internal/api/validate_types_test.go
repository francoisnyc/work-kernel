// Tests for Ventoux V2b: ValidateTypes checks draft Types against every
// registered version and registers nothing. A draft with problems is a
// successful call that lists them. Exempt from policy.Project like
// ListTypes; users and agents (in a session) may call it, customers and
// agents acting for one may not. At most 50 sources and 256 KiB of content.
package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	kernelv1 "github.com/francoisnyc/work-kernel/gen/kernel/v1"
	"github.com/francoisnyc/work-kernel/gen/kernel/v1/kernelv1connect"
)

const maxSourceBytes = 256 << 10 // 256 KiB, in all

func validateTypes(c kernelv1connect.ItemServiceClient, t *testing.T, parent string, srcs ...*kernelv1.TypeSource) (*kernelv1.ValidateTypesResponse, error) {
	t.Helper()
	res, err := c.ValidateTypes(testCtx(t), connect.NewRequest(&kernelv1.ValidateTypesRequest{Parent: parent, Sources: srcs}))
	if err != nil {
		return nil, err
	}
	return res.Msg, nil
}

func problemsAs(t *testing.T, who string, srcs ...*kernelv1.TypeSource) []*kernelv1.TypeProblem {
	t.Helper()
	ns := "acme"
	if who == "bea" {
		ns = "globex"
	}
	res, err := validateTypes(as(who), t, "namespaces/"+ns, srcs...)
	if err != nil {
		t.Fatalf("ValidateTypes as %s: %v", who, err)
	}
	return res.Problems
}

func tsrc(name, content string) *kernelv1.TypeSource {
	return &kernelv1.TypeSource{Name: name, Content: content}
}

// repoSources reads every file of types/ (and its subfolders) as a source
// named by its path from the repository's root.
func repoSources(t *testing.T) []*kernelv1.TypeSource {
	t.Helper()
	var out []*kernelv1.TypeSource
	for _, pattern := range []string{"../../types/*.yaml", "../../types/*/*.yaml"} {
		files, _ := filepath.Glob(pattern)
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			out = append(out, tsrc(strings.TrimPrefix(filepath.ToSlash(f), "../../"), string(data)))
		}
	}
	return out
}

func readType(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile("../../types/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func problemLines(ps []*kernelv1.TypeProblem) string {
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "%s:%d:%d %s %s %s %s %q\n", p.Source, p.Line, p.Column, p.Severity, p.Code, p.Path, p.Type, p.Message)
	}
	return b.String()
}

// The repository's own Types are all registered: as drafts they pass. They
// are 50 files, the most one call takes.
func TestValidateTypes_TheRepositoryTypes(t *testing.T) {
	srcs := repoSources(t)
	if len(srcs) != 50 {
		t.Fatalf("test bug: types/ has %d files; this test expects 50, the limit", len(srcs))
	}
	if ps := problemsAs(t, "ana", srcs...); len(ps) != 0 {
		t.Errorf("problems:\n%s", problemLines(ps))
	}
}

// Each problem comes back in full: source, path, position, severity, code,
// message, and the Type version's name.
func TestValidateTypes_ProblemsInFull(t *testing.T) {
	ps := problemsAs(t, "ana",
		tsrc("drafts/memo.yaml", `id: vtmemo
version: 1
initial_state: open
states:
  - {id: open, category: doing}
`),
		tsrc("drafts/broken.yaml", "id: vtbroken\nversion: 1\nfields: [\n"))
	if len(ps) != 2 {
		t.Fatalf("%d problems, want 2:\n%s", len(ps), problemLines(ps))
	}
	broken, memo := ps[0], ps[1] // sorted by source
	if broken.Source != "drafts/broken.yaml" || broken.Code != "YAML_SYNTAX" || broken.Type != "" ||
		broken.Severity != kernelv1.TypeProblemSeverity_TYPE_PROBLEM_SEVERITY_ERROR || broken.Message == "" {
		t.Errorf("syntax error = %s", problemLines(ps[:1]))
	}
	if memo.Source != "drafts/memo.yaml" || memo.Path != "states[0].category" || memo.Line != 5 || memo.Column != 16 ||
		memo.Severity != kernelv1.TypeProblemSeverity_TYPE_PROBLEM_SEVERITY_ERROR || memo.Code != "INVALID_VALUE" ||
		memo.Type != "namespaces/acme/types/vtmemo/versions/1" ||
		!strings.Contains(memo.Message, `unknown category "doing". Use triage, backlog`) {
		t.Errorf("memo's problem = %s", problemLines(ps[1:]))
	}
}

// A draft that breaks a registered version reports it there: its source is
// the version's name in the caller's namespace, with no position. Here a
// leave balance v2 no longer accepts the vacation request's deduction.
func TestValidateTypes_ARegisteredVersionByName(t *testing.T) {
	v1 := readType(t, "leave_balance.v1.yaml")
	v2 := strings.Replace(strings.Replace(v1, "\nversion: 1\n", "\nversion: 2\n", 1), "  vacation_request:deduct: [f_used]\n", "", 1)
	if !strings.Contains(v2, "\nversion: 2\n") || strings.Contains(v2, "vacation_request:deduct") {
		t.Fatal("test bug: leave_balance.v1.yaml is not as this test expects")
	}
	for who, ns := range map[string]string{"ana": "acme", "bea": "globex"} {
		ps := problemsAs(t, who, tsrc("leave_balance.v2.yaml", v2))
		if len(ps) != 2 {
			t.Fatalf("as %s: %d problems, want 2:\n%s", who, len(ps), problemLines(ps))
		}
		for i, version := range []int{2, 3} {
			name := fmt.Sprintf("namespaces/%s/types/vacation_request/versions/%d", ns, version)
			p := ps[i]
			if p.Source != name || p.Type != name || p.Code != "CONSENT_MISSING" || p.Line != 0 || p.Column != 0 ||
				!strings.HasPrefix(p.Path, "reactions[") {
				t.Errorf("as %s: problem %d = %s; want CONSENT_MISSING on %s, no position", who, i, problemLines(ps[i:i+1]), name)
			}
		}
	}
}

// An identical registered id@version passes; a different one is
// VERSION_CHANGED.
func TestValidateTypes_VersionChanged(t *testing.T) {
	v1 := readType(t, "ticket.v1.yaml")
	if ps := problemsAs(t, "ana", tsrc("ticket.v1.yaml", "# a comment changes nothing\n"+v1)); len(ps) != 0 {
		t.Errorf("ticket v1 as registered: problems\n%s", problemLines(ps))
	}
	changed := strings.Replace(v1, "\nfields:\n", "\nfields:\n  - {id: f_extra, type: string}\n", 1)
	if changed == v1 {
		t.Fatal("test bug: ticket.v1.yaml has no fields: list")
	}
	ps := problemsAs(t, "ana", tsrc("ticket.v1.yaml", changed))
	if len(ps) == 0 {
		t.Fatal("a changed ticket v1: no problems")
	}
	if len(ps) != 1 || ps[0].Code != "VERSION_CHANGED" || ps[0].Path != "version" || ps[0].Source != "ticket.v1.yaml" ||
		ps[0].Type != "namespaces/acme/types/ticket/versions/1" {
		t.Errorf("a changed ticket v1: problems\n%s; want VERSION_CHANGED at version", problemLines(ps))
	}
}

// An action's major is checked as at load (the hook protocol): hr is
// declared at major 1 only.
func TestValidateTypes_ActionMajor(t *testing.T) {
	v3 := readType(t, "vacation_request.v3.yaml")
	v4 := strings.Replace(v3, "\nversion: 3\n", "\nversion: 4\n", 1)
	if ps := problemsAs(t, "ana", tsrc("v4.yaml", strings.Replace(v4, "action: hr.manager_of\n", "action: hr.manager_of@1\n", 1))); len(ps) != 0 {
		t.Errorf("hr.manager_of@1: problems\n%s", problemLines(ps))
	}
	ps := problemsAs(t, "ana", tsrc("v4.yaml", strings.Replace(v4, "action: hr.manager_of\n", "action: hr.manager_of@2\n", 1)))
	if len(ps) != 1 || ps[0].Code != "UNKNOWN_ACTION" || !strings.Contains(ps[0].Message, "hr.manager_of@2") {
		t.Errorf("hr.manager_of@2: problems\n%s; want UNKNOWN_ACTION", problemLines(ps))
	}
}

// The limits and a malformed request are INVALID_ARGUMENT on sources; the
// limits themselves are allowed.
func TestValidateTypes_Limits(t *testing.T) {
	memo := func(i int) *kernelv1.TypeSource {
		return tsrc(fmt.Sprintf("m%d.yaml", i), fmt.Sprintf("id: vtlimit%d\nversion: 1\n", i))
	}
	many := func(n int) []*kernelv1.TypeSource {
		var out []*kernelv1.TypeSource
		for i := range n {
			out = append(out, memo(i))
		}
		return out
	}
	// padded is a valid Type whose content is exactly n bytes (a comment
	// fills it up).
	padded := func(name string, n int) *kernelv1.TypeSource {
		head := "id: vt" + strings.TrimSuffix(name, ".yaml") + "\nversion: 1\n#"
		return tsrc(name, head+strings.Repeat("x", n-len(head)-1)+"\n")
	}
	half := maxSourceBytes / 2

	for name, srcs := range map[string][]*kernelv1.TypeSource{
		"50 sources":            many(50),
		"256 KiB":               {padded("a.yaml", half), padded("b.yaml", half)},
		"one source of 256 KiB": {padded("a.yaml", maxSourceBytes)},
	} {
		if _, err := validateTypes(as("ana"), t, "namespaces/acme", srcs...); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, srcs := range map[string][]*kernelv1.TypeSource{
		"no source":               nil,
		"51 sources":              many(51),
		"256 KiB and one byte":    {padded("a.yaml", half), padded("b.yaml", half+1)},
		"a source without a name": {memo(1), tsrc("", "id: vtnoname\nversion: 1\n")},
		"two sources of one name": {memo(1), tsrc("m1.yaml", "id: vtother\nversion: 1\n")},
	} {
		_, err := validateTypes(as("ana"), t, "namespaces/acme", srcs...)
		invalid(t, name, err, "sources")
	}
}

// The parent is the caller's namespace, as for ListTypes.
func TestValidateTypes_Parent(t *testing.T) {
	src := tsrc("memo.yaml", "id: vtmemo\nversion: 1\n")
	_, err := validateTypes(as("ana"), t, "namespaces/globex", src)
	notFound(t, "another namespace", err)
	_, err = validateTypes(as("ana"), t, "acme", src)
	invalid(t, "a malformed parent", err, "parent")
}

// Users and agents in a session may call it, with no particular power;
// customers, and agents acting for one, may not.
func TestValidateTypes_WhoMayCall(t *testing.T) {
	src := tsrc("memo.yaml", "id: vtmemo\nversion: 1\n")
	for what, c := range map[string]kernelv1connect.ItemServiceClient{
		"a user":                            as("bob"),
		"an agent for a user, comment only": asAgent("bot", startSession(t, "bot", "user:ana", []string{"namespaces/acme"}, "comment")),
	} {
		if _, err := validateTypes(c, t, "namespaces/acme", src); err != nil {
			t.Errorf("%s: %v", what, err)
		}
	}
	for what, c := range map[string]kernelv1connect.ItemServiceClient{
		"a customer": asCustomer("kim", ""),
		// The portal bot's sessions have fixed powers (Jira 2.4a).
		"an agent for a customer": asCustomer("portal-bot", portalSession(t)),
	} {
		_, err := validateTypes(c, t, "namespaces/acme", src)
		apiError(t, what, err, connect.CodePermissionDenied, "NOT_ALLOWED")
	}
}

// portalSession starts the portal bot's session for kim.
func portalSession(t *testing.T) string {
	t.Helper()
	res, err := asCustomer("portal-bot", "").StartAgentSession(testCtx(t), connect.NewRequest(&kernelv1.StartAgentSessionRequest{
		Parent: "namespaces/acme", ActingFor: "customer:kim", Scopes: []string{"namespaces/acme"},
		Powers: []string{"read", "create", "comment", "transition:*"},
	}))
	if err != nil {
		t.Fatalf("test bug: the portal bot starts no session for kim: %v", err)
	}
	return res.Msg.Session.GetId()
}
