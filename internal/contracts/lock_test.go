// Tests for Ventoux V1: a published contract never changes.
//
//   - contracts.lock holds one line per contract, "<action> sha256:<hex>",
//     the digest of its machine contract; # starts a comment, blank lines
//     are skipped.
//   - Load checks the files and the lock both ways: a contract without its
//     line, a line without its contract, or a digest that differs is an
//     error naming the action. So editing a contract fails go test at once,
//     with no git needed.
//   - CheckFrozen (run in CI against the base branch's lock, beside buf
//     breaking) refuses a line that was changed or removed. A new line is
//     allowed: that is how a contract is published. The base may have no
//     lock at all.
package contracts_test

import (
	"fmt"
	"testing"
	"testing/fstest"

	"github.com/francoisnyc/work-kernel/internal/contracts"
)

const otherFile = "acme.notify@1.json"

// otherJSON is a second valid contract, an action.
const otherJSON = `{
  "connector": "acme", "major": 1, "name": "notify",
  "description": "Sends one message.",
  "kinds": ["action"],
  "inputs": ["to"],
  "inputSchema": {"type": "object", "properties": {
    "to": {"type": "string", "format": "pli-principal", "description": "Who gets it.", "x-pli-trust": "trusted"}}},
  "sideEffects": "some", "async": "forbidden", "risk": "medium"
}`

func digestOf(t *testing.T, file, data string) string {
	t.Helper()
	return mustParse(t, file, []byte(data)).Digest()
}

// folder builds a contracts folder: the two contracts and a lock.
func folder(lock string, files ...string) fstest.MapFS {
	fsys := fstest.MapFS{"contracts.lock": {Data: []byte(lock)}}
	for _, f := range files {
		switch f {
		case validFile:
			fsys[f] = &fstest.MapFile{Data: []byte(validJSON)}
		case otherFile:
			fsys[f] = &fstest.MapFile{Data: []byte(otherJSON)}
		}
	}
	return fsys
}

func TestLoad_FilesAndLockAgree(t *testing.T) {
	lock := fmt.Sprintf("# published\n\nacme.lookup@1 %s\nacme.notify@1 %s\n",
		digestOf(t, validFile, validJSON), digestOf(t, otherFile, otherJSON))
	fsys := folder(lock, otherFile, validFile)
	fsys["README.md"] = &fstest.MapFile{Data: []byte("not a contract")} // other files are not read
	cat, err := contracts.Load(fsys)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var got []string
	for _, c := range cat.All() {
		got = append(got, c.Action())
	}
	if want := []string{"acme.lookup@1", "acme.notify@1"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("All = %v, want %v (ordered by action)", got, want)
	}
	for _, name := range []string{"acme.lookup", "acme.lookup@1"} {
		if c, ok := cat.Lookup(name); !ok || c.Action() != "acme.lookup@1" {
			t.Errorf("Lookup(%q) = %q, %v; want acme.lookup@1 (no major means 1)", name, c.Action(), ok)
		}
	}
	for _, name := range []string{"acme.lookup@2", "acme.nope", "acme", "acme.lookup@", "acme.lookup@01", ""} {
		if _, ok := cat.Lookup(name); ok {
			t.Errorf("Lookup(%q) found a contract", name)
		}
	}
}

func TestLoad_Refuses(t *testing.T) {
	lookup, notify := digestOf(t, validFile, validJSON), digestOf(t, otherFile, otherJSON)
	cases := []struct {
		name string
		fsys fstest.MapFS
		want []string
	}{
		{"a contract without its lock line",
			folder(fmt.Sprintf("acme.lookup@1 %s\n", lookup), validFile, otherFile), []string{"acme.notify@1"}},
		{"a lock line without its contract",
			folder(fmt.Sprintf("acme.lookup@1 %s\nacme.notify@1 %s\n", lookup, notify), validFile), []string{"acme.notify@1"}},
		{"a contract changed after it was locked",
			folder(fmt.Sprintf("acme.lookup@1 %s\nacme.notify@1 %s\n", notify, notify), validFile, otherFile), []string{"acme.lookup@1"}},
		{"a lock line that is not <action> sha256:<hex>",
			folder("acme.lookup@1\n", validFile), []string{"acme.lookup@1"}},
		{"an action locked twice",
			folder(fmt.Sprintf("acme.lookup@1 %s\nacme.lookup@1 %s\n", lookup, lookup), validFile), []string{"acme.lookup@1"}},
		{"no lock", fstest.MapFS{validFile: {Data: []byte(validJSON)}}, []string{"contracts.lock"}},
		{"a contract that does not parse",
			fstest.MapFS{"contracts.lock": {Data: []byte("")}, "acme.bad@1.json": {Data: []byte("{")}}, []string{"acme.bad@1.json"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := contracts.Load(c.fsys)
			wantError(t, c.name, err, c.want...)
		})
	}
}

func TestParseLock(t *testing.T) {
	d := digestOf(t, validFile, validJSON)
	got, err := contracts.ParseLock([]byte("# comment\n\nacme.lookup@1 " + d + "\n"))
	if err != nil || len(got) != 1 || got["acme.lookup@1"] != d {
		t.Errorf("ParseLock = %v, %v", got, err)
	}
}

func TestCheckFrozen(t *testing.T) {
	a := "acme.lookup@1 sha256:" + fmt.Sprintf("%064x", 1) + "\n"
	b := "acme.notify@1 sha256:" + fmt.Sprintf("%064x", 2) + "\n"
	aChanged := "acme.lookup@1 sha256:" + fmt.Sprintf("%064x", 3) + "\n"
	ok := []struct{ name, base, head string }{
		{"no lock on the base branch", "", a},
		{"unchanged", a + b, a + b},
		{"a contract published", a, a + b},
		{"comments and order", "# old\n" + a + b, "# new\n" + b + "\n" + a},
	}
	for _, c := range ok {
		if err := contracts.CheckFrozen([]byte(c.base), []byte(c.head)); err != nil {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	refused := []struct{ name, base, head, want string }{
		{"a digest changed", a + b, aChanged + b, "acme.lookup@1"},
		{"a line removed", a + b, a, "acme.notify@1"},
		{"a malformed head", a, a + "acme.notify@1\n", "acme.notify@1"},
	}
	for _, c := range refused {
		wantError(t, c.name, contracts.CheckFrozen([]byte(c.base), []byte(c.head)), c.want)
	}
}
