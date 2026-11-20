// Test for Step A2: every exported read of the item service goes through
// policy.Project, the one door out. Go cannot ask at run time which functions
// a method calls, so this test reads the package's own source: every
// exported method of *Service that returns more than a WriteResult or an
// error must call policy.Project in its body, unless it is on the exemption
// list below with a reason.
//
// A new read method that skips Project fails here, by name.
package itemservice_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"
)

// projectExempt lists the exported reads that do not reach a principal.
var projectExempt = map[string]string{
	"StateEntry": "feeds the workflow, not a principal: state names and opaque IDs only",
	// Jira part 2, step 1: the one declared exception to "the workflow never
	// sees a field value" (docs/design.md, "Decisions for Jira part 2").
	"AtInstant":      "feeds the workflow, not a principal: the instant of the datetime field the Item's current state names in `at`, and nothing else; a timer's length is in the workflow's history anyway",
	"ActingInstants": "feeds the workflow, not a principal: the instants of the datetime fields the acting timers of the Item's current state name in `at`, and nothing else; as AtInstant",
	"Type":           "feeds the workflow and the relay, and GetType: Types are readable by anyone in the namespace; a Type definition holds no Item data",
	// Step A2: returns a new session ID only.
	"StartAgentSession": "returns the minted session ID, no Item data",
	// Step 6: the kernel's write door.
	"Internal": "returns the kernel's write door; its methods return WriteResult and error only, no Item data",
	// Step 0, part 1: the public API reads Types.
	"ListTypes": "Types are readable by anyone in the namespace: a Type definition holds no Item data",
	// Step 0, part 2: the API learns who an agent acts for.
	"ApprovalAudience": "returns whether a scope is open, no Item data",
	"AgentSession":     "returns the session's agent, user, namespace, scopes (opaque Item IDs the session was granted) and powers; no Item data",
	// Step 0, part 3: the item service's start in dev-token mode.
	"EnsureRoles": "returns roles (namespace, principal ID, role name) the server's own config named; no Item data",
}

// noItemData are the result types that carry no Item data. A
// TransitionResult is a WriteResult plus an outcome, a hold ID and a reason;
// the reason is a code, never a field value, so it holds no Item data either
// (Step 6, part 2).
var noItemData = []string{"WriteResult", "TransitionResult", "error"}

func TestEveryExportedReadCallsProject(t *testing.T) {
	fset := token.NewFileSet()
	// ParseDir is the go/parser equivalent of reading every .java file in a
	// package; the filter skips the tests themselves.
	pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse package: %v", err)
	}
	pkg, ok := pkgs["itemservice"]
	if !ok {
		t.Fatal("package itemservice not found")
	}

	checked := map[string]bool{}
	for _, f := range pkg.Files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || !fn.Name.IsExported() || !onService(fn) {
				continue
			}
			if !returnsItemData(fn) {
				continue
			}
			name := fn.Name.Name
			checked[name] = true
			if _, ok := projectExempt[name]; ok {
				continue
			}
			if !callsProject(fn) {
				t.Errorf("(*Service).%s returns data to a caller but does not call policy.Project", name)
			}
		}
	}

	// The test must have looked at the real reads, and the exemptions must
	// still name real methods.
	for _, want := range []string{"Get", "Search"} {
		if !checked[want] {
			t.Errorf("did not find (*Service).%s; the test is not looking where it should", want)
		}
	}
	for name := range projectExempt {
		if !checked[name] {
			t.Errorf("exemption %q names no exported read; remove it", name)
		}
	}
}

// onService reports whether the method's receiver is *Service.
func onService(fn *ast.FuncDecl) bool {
	star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
	if !ok {
		return false
	}
	id, ok := star.X.(*ast.Ident)
	return ok && id.Name == "Service"
}

// returnsItemData reports whether any result is something other than a
// WriteResult or an error.
func returnsItemData(fn *ast.FuncDecl) bool {
	if fn.Type.Results == nil {
		return false
	}
	for _, r := range fn.Type.Results.List {
		if id, ok := r.Type.(*ast.Ident); ok && slices.Contains(noItemData, id.Name) {
			continue
		}
		return true
	}
	return false
}

// callsProject reports whether the body calls policy.Project.
func callsProject(fn *ast.FuncDecl) bool {
	found := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "policy" && sel.Sel.Name == "Project" {
			found = true
		}
		return !found
	})
	return found
}
