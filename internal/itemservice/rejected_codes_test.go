// Test for Step 0, part 2 (PR #18 review): every rule refusal carries a code.
// The API answers an ErrRejected with the rule's code as its reason, and an
// uncoded one as INTERNAL, so a rejection built without a code is a bug. Go
// cannot list at run time where errors are made, so this test reads the
// source of this package and of internal/lifecycle:
//
//   - ErrRejected itself appears only where it is declared and in
//     RejectedError's Error and Unwrap: nobody wraps it bare
//     (fmt.Errorf("%w", ...)).
//   - Every RejectedError and lifecycle.Rejection literal sets Code.
//   - Every call of rejected(...) passes a Code* constant first.
package itemservice_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"
)

func parsePackage(t *testing.T, dir, name string) map[string]*ast.File {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", dir, err)
	}
	pkg, ok := pkgs[name]
	if !ok {
		t.Fatalf("package %s not found in %s", name, dir)
	}
	return pkg.Files
}

// hasKey reports whether a composite literal sets the key to something.
func hasKey(lit *ast.CompositeLit, key string) bool {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == key {
			if b, ok := kv.Value.(*ast.BasicLit); ok && b.Value == `""` {
				return false
			}
			return true
		}
	}
	return false
}

func typeName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

func TestEveryRejectionHasACode(t *testing.T) {
	literals, rejectedCalls := 0, 0
	for file, f := range parsePackage(t, ".", "itemservice") {
		// The function each node is in, to allow ErrRejected where it belongs.
		var fn string
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				fn = n.Name.Name
			case *ast.Ident:
				// RejectedError's own Error and Unwrap name the sentinel.
				if n.Name == "ErrRejected" && fn != "" && fn != "Unwrap" && fn != "Error" {
					t.Errorf("%s: %s uses ErrRejected bare; build a RejectedError with a code (rejected(Code..., ...))", file, fn)
				}
			case *ast.CompositeLit:
				if typeName(n.Type) == "RejectedError" {
					literals++
					if !hasKey(n, "Code") {
						t.Errorf("%s: %s builds a RejectedError without a Code", file, fn)
					}
				}
			case *ast.CallExpr:
				if id, ok := n.Fun.(*ast.Ident); ok && id.Name == "rejected" {
					rejectedCalls++
					if len(n.Args) == 0 || !strings.HasPrefix(typeName(n.Args[0]), "Code") {
						t.Errorf("%s: %s calls rejected without a Code* constant first", file, fn)
					}
				}
			}
			return true
		})
	}
	if literals == 0 || rejectedCalls == 0 {
		t.Errorf("found %d RejectedError literals and %d rejected calls; the test is not looking where it should",
			literals, rejectedCalls)
	}

	rejections := 0
	for file, f := range parsePackage(t, "../lifecycle", "lifecycle") {
		ast.Inspect(f, func(n ast.Node) bool {
			if lit, ok := n.(*ast.CompositeLit); ok && typeName(lit.Type) == "Rejection" {
				rejections++
				if !hasKey(lit, "Code") {
					t.Errorf("%s: a lifecycle.Rejection without a Code", file)
				}
			}
			return true
		})
	}
	if rejections == 0 {
		t.Error("found no lifecycle.Rejection literals; the test is not looking where it should")
	}
}
