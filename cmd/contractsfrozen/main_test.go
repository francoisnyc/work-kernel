package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lockFile(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestContractsFrozen(t *testing.T) {
	a := fmt.Sprintf("acme.lookup@1 sha256:%064x\n", 1)
	b := fmt.Sprintf("acme.notify@1 sha256:%064x\n", 2)
	changed := fmt.Sprintf("acme.lookup@1 sha256:%064x\n", 3)
	cases := []struct {
		name, base, head string
		code             int
		stderr           string
	}{
		{"no lock on the base branch", "", a, 0, ""},
		{"a contract published", a, a + b, 0, ""},
		{"a published contract changed", a + b, changed + b, 1, "acme.lookup@1"},
		{"a published contract removed", a + b, b, 1, "acme.lookup@1"},
	}
	for _, c := range cases {
		var stderr bytes.Buffer
		code := run([]string{lockFile(t, "base.lock", c.base), lockFile(t, "head.lock", c.head)}, &stderr)
		if code != c.code || !strings.Contains(stderr.String(), c.stderr) {
			t.Errorf("%s: exit %d, stderr %q; want %d naming %q", c.name, code, stderr.String(), c.code, c.stderr)
		}
	}
	var stderr bytes.Buffer
	if code := run([]string{"only-one"}, &stderr); code != 2 {
		t.Errorf("one argument: exit %d, want 2", code)
	}
	if code := run([]string{lockFile(t, "base.lock", a), filepath.Join(t.TempDir(), "missing")}, &stderr); code != 2 {
		t.Errorf("a missing head lock: exit %d, want 2", code)
	}
}
