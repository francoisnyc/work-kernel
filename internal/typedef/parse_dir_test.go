// Tests for Step 0, part 1: the item service loads every Type file in a
// directory at start (types/), so GetType and ListTypes serve them.
package typedef_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// ParseDir reads every .yaml file, ordered by Type ID then version.
func TestParseDir_ReadsTheRepositorysTypes(t *testing.T) {
	types, err := typedef.ParseDir("../../types")
	if err != nil {
		t.Fatalf("ParseDir: %v", err)
	}
	var got []string
	for _, ty := range types {
		got = append(got, fmt.Sprintf("%s@%d", ty.ID, ty.Version))
	}
	want := "bug@1 bug@2 channel@1 comment@1 epic@1 leave_balance@1 message@1 space@1 sprint@1 sprint@2 " +
		"story@1 story@2 subscription@1 task@1 task@2 " +
		"ticket@1 ticket@2 ticket@3 ticket@4 ticket@5 vacation_request@1 vacation_request@2 vacation_request@3"
	if strings.Join(got, " ") != want {
		t.Errorf("ParseDir = %v, want %s", got, want)
	}
}

func writeFiles(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const noteV1 = "id: note\nversion: 1\nfields:\n  - {id: f_text, type: string}\n"

// Files that are not .yaml are skipped (a README next to the Types).
func TestParseDir_SkipsOtherFiles(t *testing.T) {
	dir := writeFiles(t, map[string]string{"note.v1.yaml": noteV1, "README.md": "# Types\n"})
	types, err := typedef.ParseDir(dir)
	if err != nil || len(types) != 1 {
		t.Fatalf("ParseDir = %d Types, %v; want 1, nil", len(types), err)
	}
}

// A bad file stops the load and the error names it: the server must not
// start with half its Types.
func TestParseDir_BadFileIsNamed(t *testing.T) {
	dir := writeFiles(t, map[string]string{"note.v1.yaml": noteV1, "broken.v1.yaml": "id: broken\nversion: one\n"})
	_, err := typedef.ParseDir(dir)
	if err == nil || !strings.Contains(err.Error(), "broken.v1.yaml") {
		t.Fatalf("ParseDir error = %v, want one naming broken.v1.yaml", err)
	}
}

// Two files declaring the same Type version are refused.
func TestParseDir_DuplicateVersionIsRefused(t *testing.T) {
	dir := writeFiles(t, map[string]string{"a.yaml": noteV1, "b.yaml": noteV1})
	_, err := typedef.ParseDir(dir)
	if err == nil || !strings.Contains(err.Error(), "note") {
		t.Fatalf("ParseDir error = %v, want one naming the duplicate note v1", err)
	}
}
