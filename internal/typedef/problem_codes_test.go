// Tests for Ventoux item 2a: every problem carries a code, every code is
// documented, and every code has a test. Like itemservice's
// TestEveryRejectionHasACode, this reads the package's source, since Go
// cannot list at run time where problems are made.
//
//   - Every call of fail(...) (a function or a method) passes a Code*
//     constant first, a non-empty "what to do" third (a string literal or a
//     todo* constant), and never wraps a whole error as its message
//     (format "%v"), which would hide the kind of problem. The signature
//     this pins: fail(code, path, todo, format, args...).
//   - Every Problem literal sets Code.
//   - The Code constants are exactly the codes of the table under
//     "### Type problems" in docs/design.md.
//   - Every code has a case in problems_test.go or problems_dir_test.go.
//
// CheckFields checks an Item's values, not a Type: its helpers are not
// problems, and are left out.
package typedef_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func typedefSource(t *testing.T) map[string]*ast.File {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), ".", func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parse typedef: %v", err)
	}
	pkg, ok := pkgs["typedef"]
	if !ok {
		t.Fatal("package typedef not found")
	}
	return pkg.Files
}

func exprName(e ast.Expr) string {
	switch e := e.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	}
	return ""
}

// declaredCodes returns the values of the constants of type Code.
func declaredCodes(t *testing.T) []string {
	t.Helper()
	var codes []string
	for _, f := range typedefSource(t) {
		for _, d := range f.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, s := range gd.Specs {
				vs := s.(*ast.ValueSpec)
				if exprName(vs.Type) != "Code" {
					continue
				}
				for _, v := range vs.Values {
					if lit, ok := v.(*ast.BasicLit); ok {
						code, _ := strconv.Unquote(lit.Value)
						codes = append(codes, code)
					}
				}
			}
		}
	}
	slices.Sort(codes)
	return codes
}

func TestEveryProblemHasACode(t *testing.T) {
	calls, literals := 0, 0
	for file, f := range typedefSource(t) {
		var fn string
		ast.Inspect(f, func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.FuncDecl:
				fn = n.Name.Name
			case *ast.CompositeLit:
				if exprName(n.Type) == "Problem" {
					literals++
					if !hasKeyValue(n, "Code") {
						t.Errorf("%s: %s builds a Problem without a Code", file, fn)
					}
				}
			case *ast.CallExpr:
				if exprName(n.Fun) != "fail" || fn == "CheckFields" {
					return true
				}
				calls++
				if len(n.Args) < 4 {
					t.Errorf("%s: %s calls fail with %d arguments; want fail(code, path, todo, format, args...)", file, fn, len(n.Args))
					return true
				}
				if !strings.HasPrefix(exprName(n.Args[0]), "Code") {
					t.Errorf("%s: %s calls fail without a Code* constant first", file, fn)
				}
				switch todo := n.Args[2].(type) {
				case *ast.BasicLit:
					if s, _ := strconv.Unquote(todo.Value); strings.TrimSpace(s) == "" {
						t.Errorf("%s: %s calls fail with an empty what-to-do", file, fn)
					}
				case *ast.Ident:
					if !strings.HasPrefix(todo.Name, "todo") {
						t.Errorf("%s: %s calls fail with %s as what-to-do; want a string literal or a todo* constant", file, fn, todo.Name)
					}
				default:
					t.Errorf("%s: %s calls fail without a what-to-do (a string literal or a todo* constant) third", file, fn)
				}
				if lit, ok := n.Args[3].(*ast.BasicLit); ok && lit.Value == `"%v"` {
					t.Errorf("%s: %s calls fail with format %%v: say what is wrong, not just the wrapped error", file, fn)
				}
			}
			return true
		})
	}
	// About 400 places refused a Type before item 2a.
	if calls < 300 {
		t.Errorf("found %d fail calls (and %d Problem literals); the test is not looking where it should", calls, literals)
	}
}

// hasKeyValue reports whether a composite literal sets the key to something
// other than "".
func hasKeyValue(lit *ast.CompositeLit, key string) bool {
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		if id, ok := kv.Key.(*ast.Ident); ok && id.Name == key {
			b, ok := kv.Value.(*ast.BasicLit)
			return !ok || b.Value != `""`
		}
	}
	return false
}

// The codes table in docs/design.md, under "### Type problems": rows whose
// first cell is a code in backticks, up to the next heading.
func documentedCodes(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile("../../docs/design.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(data), "\n### Type problems\n")
	if !ok {
		t.Fatal(`docs/design.md has no "### Type problems" section`)
	}
	if i := strings.Index(section, "\n#"); i >= 0 {
		section = section[:i]
	}
	var codes []string
	for _, m := range regexp.MustCompile("(?m)^\\| `([A-Z_]+)` \\|").FindAllStringSubmatch(section, -1) {
		codes = append(codes, m[1])
	}
	slices.Sort(codes)
	return codes
}

func TestProblemCodes_DocumentedInDesign(t *testing.T) {
	declared, documented := declaredCodes(t), documentedCodes(t)
	if len(declared) < 30 {
		t.Errorf("found %d Code constants; the test is not looking where it should", len(declared))
	}
	if !slices.Equal(declared, documented) {
		t.Errorf("Code constants and the design.md table differ:\n declared:   %v\n documented: %v", declared, documented)
	}
}

func TestProblemCodes_EachHasACase(t *testing.T) {
	tested := map[string]bool{string(typedef.CodeYAMLSyntax): true} // TestProblems_SyntaxErrorAlone
	for _, c := range oneTypeCases {
		tested[string(c.code)] = true
	}
	for _, c := range dirCases {
		tested[string(c.code)] = true
	}
	for _, code := range declaredCodes(t) {
		if !tested[code] {
			t.Errorf("code %s has no case", code)
		}
	}
}
