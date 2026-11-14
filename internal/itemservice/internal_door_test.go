// Test for Step 6, part 1: only the worker and the hook runner may use the
// item service's internal door. Go's internal/ directory stops other modules
// from importing a package, but not other packages of this module, and
// Internal() has to be exported for the worker to reach it. So, like the
// Project test, this test reads the module's source: a call to .Internal()
// outside the allowed packages fails here, by file and line.
package itemservice_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// internalCallers are the packages that may call Internal(), relative to
// the module root, with the reason.
var internalCallers = map[string]string{
	"internal/workflow": "the worker's activities: the timer's write",
	"internal/hooks":    "the hook runner's activities: hook write-backs (Step 6, part 2)",
}

func TestInternalDoor_OnlyTheWorkerAndHookRunnerCallIt(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	callsIn := map[string]int{}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		// Tests may use the door: they stand in for the worker.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, filepath.Dir(path))
		rel = filepath.ToSlash(rel)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 0 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Internal" {
				return true
			}
			callsIn[rel]++
			if _, ok := internalCallers[rel]; !ok {
				t.Errorf("%s calls Internal(); only %v may", fset.Position(call.Pos()), slices.Sorted(maps.Keys(internalCallers)))
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk the module: %v", err)
	}
	// The test must be looking where the real call is: the timer's write.
	if callsIn["internal/workflow"] == 0 {
		t.Error("found no call to Internal() in internal/workflow; the timer must write through it")
	}
}
