package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A Type file before and after pli fmt (the layout itself is typefmt's
// tests; these are the command's).
const (
	fmtIn   = "id: t\nversion: 1\nstates: [{id: a, category: todo}]\n"
	fmtWant = "id: t\nversion: 1\n\nstates:\n  - id: a\n    category: todo\n"
)

// fmtRun runs pli fmt as a user would, with no --as and a token file that
// does not exist: fmt reads files and needs neither.
func fmtRun(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	t.Setenv("PLI_AS", "")
	var out, errOut bytes.Buffer
	all := append([]string{"--tokens", filepath.Join(t.TempDir(), "no-such-tokens.yaml"), "fmt"}, args...)
	code = run(context.Background(), all, &out, &errOut)
	return code, out.String(), errOut.String()
}

// writeFile writes content to dir/name (making its folder) and returns the
// path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestFmt_PrintsTheFormattedFiles(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.yaml", fmtIn)
	b := writeFile(t, dir, "b.yaml", fmtWant)
	code, stdout, stderr := fmtRun(t, a, b)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != fmtWant+fmtWant {
		t.Errorf("stdout = %q, want each file formatted, in turn: %q", stdout, fmtWant+fmtWant)
	}
	if got := readFile(t, a); got != fmtIn {
		t.Errorf("without -w the file changed: %q", got)
	}
}

func TestFmt_WriteRewritesOnlyTheFilesThatDiffer(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.yaml", fmtIn)
	if err := os.Chmod(bad, 0o600); err != nil {
		t.Fatal(err)
	}
	good := writeFile(t, dir, "good.yaml", fmtWant)
	old := time.Now().Add(-time.Hour).Truncate(time.Second)
	if err := os.Chtimes(good, old, old); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := fmtRun(t, "-w", bad, good)
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and nothing printed", code, stdout, stderr)
	}
	if got := readFile(t, bad); got != fmtWant {
		t.Errorf("bad.yaml = %q, want %q", got, fmtWant)
	}
	if info, err := os.Stat(bad); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("bad.yaml's mode = %v (%v), want it kept: 0600", info.Mode().Perm(), err)
	}
	if info, err := os.Stat(good); err != nil || !info.ModTime().Equal(old) {
		t.Errorf("good.yaml was written though it was formatted already (mtime %v, want %v)", info.ModTime(), old)
	}
}

func TestFmt_ListNamesTheFilesThatDiffer(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.yaml", fmtIn)
	good := writeFile(t, dir, "good.yaml", fmtWant)
	code, stdout, stderr := fmtRun(t, "-l", bad, good)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != bad+"\n" {
		t.Errorf("stdout = %q, want %q", stdout, bad+"\n")
	}
	if got := readFile(t, bad); got != fmtIn {
		t.Errorf("-l changed the file: %q", got)
	}
}

func TestFmt_ListAndWrite(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.yaml", fmtIn)
	code, stdout, stderr := fmtRun(t, "-l", "-w", bad)
	if code != 0 || stderr != "" {
		t.Fatalf("exit %d, stderr %q", code, stderr)
	}
	if stdout != bad+"\n" {
		t.Errorf("stdout = %q, want %q", stdout, bad+"\n")
	}
	if got := readFile(t, bad); got != fmtWant {
		t.Errorf("bad.yaml = %q, want %q", got, fmtWant)
	}
}

func TestFmt_CheckFailsWhenAFileDiffers(t *testing.T) {
	dir := t.TempDir()
	good := writeFile(t, dir, "good.yaml", fmtWant)
	if code, stdout, stderr := fmtRun(t, "--check", good); code != 0 || stdout != "" || stderr != "" {
		t.Errorf("all formatted: exit %d, stdout %q, stderr %q; want 0 and nothing printed", code, stdout, stderr)
	}
	bad := writeFile(t, dir, "bad.yaml", fmtIn)
	code, stdout, _ := fmtRun(t, "--check", good, bad)
	if code != 1 {
		t.Errorf("a file differs: exit %d, want 1", code)
	}
	if stdout != bad+"\n" {
		t.Errorf("stdout = %q, want %q", stdout, bad+"\n")
	}
	if got := readFile(t, bad); got != fmtIn {
		t.Errorf("--check changed the file: %q", got)
	}
}

// TestFmt_AFolder: a folder is read as type validate reads one: its .yaml
// files and its subfolders', one level deep, names starting with a dot
// skipped. A folder deeper than that is reported and left alone.
func TestFmt_AFolder(t *testing.T) {
	dir := t.TempDir()
	a := writeFile(t, dir, "a.yaml", fmtIn)
	b := writeFile(t, dir, "sub/b.yaml", fmtIn)
	skipped := []string{
		writeFile(t, dir, ".hidden.yaml", fmtIn),
		writeFile(t, dir, ".git/c.yaml", fmtIn),
		writeFile(t, dir, "notes.txt", fmtIn),
	}
	deep := writeFile(t, dir, "sub/deeper/d.yaml", fmtIn)

	code, stdout, stderr := fmtRun(t, "-l", "-w", dir)
	if want := a + "\n" + b + "\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	for _, p := range []string{a, b} {
		if got := readFile(t, p); got != fmtWant {
			t.Errorf("%s = %q, want it formatted", p, got)
		}
	}
	for _, p := range append(skipped, deep) {
		if got := readFile(t, p); got != fmtIn {
			t.Errorf("%s was formatted; want it skipped", p)
		}
	}
	if code != 1 || !strings.Contains(stderr, filepath.Join(dir, "sub", "deeper")) {
		t.Errorf("exit %d, stderr %q; want 1 and the folder too deep named", code, stderr)
	}
}

func TestFmt_StdinToStdout(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runFmt(nil, strings.NewReader(fmtIn), &out, &errOut); code != 0 || errOut.String() != "" {
		t.Fatalf("exit %d, stderr %q", code, errOut.String())
	}
	if out.String() != fmtWant {
		t.Errorf("stdout = %q, want %q", out.String(), fmtWant)
	}

	out.Reset()
	if code := runFmt([]string{"--check"}, strings.NewReader(fmtIn), &out, &errOut); code != 1 {
		t.Errorf("--check on stdin that differs: exit %d, want 1", code)
	}
	if out.String() != "<standard input>\n" {
		t.Errorf("--check on stdin: stdout = %q, want %q", out.String(), "<standard input>\n")
	}
}

// TestFmt_AFileThatIsNotYAML is reported and left alone; the other files
// are still formatted.
func TestFmt_AFileThatIsNotYAML(t *testing.T) {
	dir := t.TempDir()
	broken := writeFile(t, dir, "broken.yaml", "id: [t\n")
	bad := writeFile(t, dir, "bad.yaml", fmtIn)
	code, _, stderr := fmtRun(t, "-w", broken, bad)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	if !strings.HasPrefix(stderr, broken+":") || strings.Count(stderr, "\n") != 1 {
		t.Errorf("stderr = %q, want one line starting %q", stderr, broken+":")
	}
	if got := readFile(t, broken); got != "id: [t\n" {
		t.Errorf("broken.yaml changed: %q", got)
	}
	if got := readFile(t, bad); got != fmtWant {
		t.Errorf("bad.yaml = %q, want it formatted all the same", got)
	}
}

// TestFmt_ACommentWithNoPlace refuses the file: PATH:LINE, nothing written.
func TestFmt_ACommentWithNoPlace(t *testing.T) {
	dir := t.TempDir()
	const in = "id: t\nsummary: [f_a,  # first\n  f_b]\n"
	p := writeFile(t, dir, "c.yaml", in)
	code, stdout, stderr := fmtRun(t, "-w", p)
	if code != 1 || stdout != "" {
		t.Errorf("exit %d, stdout %q; want 1 and nothing on stdout", code, stdout)
	}
	if want := p + `:2: cannot place comment "# first"` + "\n"; stderr != want {
		t.Errorf("stderr = %q, want %q", stderr, want)
	}
	if got := readFile(t, p); got != in {
		t.Errorf("the file changed: %q", got)
	}
}

// TestFmt_UsageErrors exit 2 and do nothing.
func TestFmt_UsageErrors(t *testing.T) {
	dir := t.TempDir()
	bad := writeFile(t, dir, "bad.yaml", fmtIn)
	for name, args := range map[string][]string{
		"an unknown flag":          {"-x", bad},
		"-w with no PATH":          {"-w"},
		"--check with -w":          {"--check", "-w", bad},
		"a PATH that is not there": {"-w", bad, filepath.Join(dir, "missing.yaml")},
	} {
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := fmtRun(t, args...)
			if code != exitUsage {
				t.Errorf("exit %d, want %d", code, exitUsage)
			}
			if stdout != "" || stderr == "" {
				t.Errorf("stdout %q, stderr %q; want nothing on stdout, the reason on stderr", stdout, stderr)
			}
			if got := readFile(t, bad); got != fmtIn {
				t.Errorf("a usage error wrote a file: %q", got)
			}
		})
	}
}
