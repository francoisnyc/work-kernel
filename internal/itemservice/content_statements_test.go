// The etag step: no write path may forget content_seq. The rule's decision
// travels with the event (appendEvent requires it), but the counter itself
// is written by each path's own statement, the one that writes item_seq:
// a second UPDATE of the row would make Postgres re-check the row's
// foreign keys and wait on the container's row lock (docs/design.md,
// "Writes"). So, like the internal-door test, this test reads the module's
// source: every SQL statement that writes items.item_seq must write
// content_seq too, by file and line.
package itemservice_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var (
	// An UPDATE of items that sets item_seq, or an INSERT into items that
	// names it. (A SELECT, or item_events' own item_seq, is neither.)
	writesItemSeq = regexp.MustCompile(`^update items set .*\bitem_seq\s*=|^insert into items \([^)]*\bitem_seq\b`)
	// The same statement writing content_seq: set in an UPDATE's SET list
	// (before its WHERE), or named in an INSERT's column list.
	writesContentSeq = regexp.MustCompile(`^update items set [^;]*?\bcontent_seq\s*=.*\bwhere\b|^insert into items \([^)]*\bcontent_seq\b`)
)

// itemSeqWrites returns, for every string literal in src that is a
// statement writing items.item_seq, whether it also writes content_seq.
func itemSeqWrites(t *testing.T, fset *token.FileSet, path string, src any) map[string]bool {
	t.Helper()
	f, err := parser.ParseFile(fset, path, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	found := map[string]bool{}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		text, err := strconv.Unquote(lit.Value)
		if err != nil {
			return true
		}
		sql := strings.ToLower(strings.Join(strings.Fields(text), " "))
		if writesItemSeq.MatchString(sql) {
			found[fset.Position(lit.Pos()).String()] = writesContentSeq.MatchString(sql)
		}
		return true
	})
	return found
}

func TestContentSeq_EveryStatementThatWritesItemSeqWritesItToo(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	statements := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "vendor" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		// Tests seed rows by hand, some of them before this migration.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		for at, ok := range itemSeqWrites(t, fset, path, nil) {
			statements++
			if !ok {
				rel, _ := filepath.Rel(root, at)
				t.Errorf("%s: this statement writes items.item_seq and not content_seq: add the etag rule's decision "+
					"(decideContent) to it, in the same statement", rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// Thirteen UPDATEs and two INSERTs today. Fewer means the patterns
	// above stopped recognising them, and the test proves nothing.
	if statements < 15 {
		t.Errorf("found %d statements that write items.item_seq, want at least 15: the test no longer sees them", statements)
	}
}

// The test's own eyes: it sees a statement that forgets content_seq, in
// each of the shapes the write paths use, and passes the ones that do not.
func TestContentSeq_TheStatementCheck_SeesAForgottenCounter(t *testing.T) {
	const src = "package x\n" +
		"var bad1 = `update items set item_seq = $2, updated_at = now() where id = $1`\n" +
		"var bad2 = `update items set state = $2, fields = $3,\n\t\titem_seq = $4 where id = $1`\n" +
		"var bad3 = `insert into items (id, state, item_seq, created_at) values ($1, $2, $3, $4)`\n" +
		"var bad4 = `update items set item_seq = $2 where id = $1 and content_seq = 3`\n" +
		"var good1 = `update items set item_seq = $2, content_seq = content_seq + $3, updated_at = now() where id = $1`\n" +
		"var good2 = `insert into items (id, state, item_seq,\n\t\tcontent_seq) values ($1, $2, $3, $4)`\n" +
		"var other1 = `select item_seq from items where id = $1`\n" +
		"var other2 = `update items set scope_id = $2 where id = $1`\n" +
		"var other3 = `insert into item_events (item_id, item_seq) values ($1, $2)`\n"
	fset := token.NewFileSet()
	got := itemSeqWrites(t, fset, "x.go", src)
	want := map[string]bool{"x.go:2:12": false, "x.go:3:12": false, "x.go:5:12": false, "x.go:6:12": false,
		"x.go:7:13": true, "x.go:8:13": true}
	if len(got) != len(want) {
		t.Fatalf("saw %v, want %v", got, want)
	}
	for at, ok := range want {
		if seen, has := got[at]; !has || seen != ok {
			t.Errorf("%s: seen %v (found %v), want %v", at, seen, has, ok)
		}
	}
}
