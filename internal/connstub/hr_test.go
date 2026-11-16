// Tests for proof-of-concept Step 2, part 1: the stub HR directory. It knows
// each employee's manager, by principal ID, and nothing else; deciding who
// may approve is the hr.manager_of action's job.
//
//	HR   GET  /employees/{id}/manager → {"manager": "user:bob"}; 404 for an unknown employee
//
// Its directory in the compose stack is deploy/hr-directory.json.
package connstub_test

import (
	"maps"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/francoisnyc/work-kernel/internal/connstub"
)

func TestHR_ServesTheManager(t *testing.T) {
	hr := connstub.NewHR()
	defer hr.Close()
	hr.SetManager("user:ana", "user:bob")

	code, body := get(t, hr.URL+"/employees/"+url.PathEscape("user:ana")+"/manager")
	if code != http.StatusOK || body["manager"] != "user:bob" {
		t.Errorf("GET manager of ana = %d %v, want 200 with user:bob", code, body)
	}
	if code, _ := get(t, hr.URL+"/employees/"+url.PathEscape("user:erin")+"/manager"); code != http.StatusNotFound {
		t.Errorf("GET manager of an unknown employee = %d, want 404", code)
	}
	if code, _ := get(t, hr.URL+"/healthz"); code != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", code)
	}
	if n := hr.Calls(); n != 2 {
		t.Errorf("Calls = %d, want 2 (healthz is not counted)", n)
	}
}

// The HR stub has the same knobs as the others.
func TestHR_Outage(t *testing.T) {
	hr := connstub.NewHR()
	defer hr.Close()
	hr.SetManager("user:ana", "user:bob")
	hr.SetOutage(true)
	if code, _ := get(t, hr.URL+"/employees/user:ana/manager"); code != http.StatusServiceUnavailable {
		t.Errorf("during an outage = %d, want 503", code)
	}
}

// The checked-in directory: ana reports to bob, bob to dana, and dana to
// herself (the case HR answers no_eligible_approver for). erin is not in it.
func TestLoadDirectory_TheStacksDirectory(t *testing.T) {
	dir, err := connstub.LoadDirectory("../../deploy/hr-directory.json")
	if err != nil {
		t.Fatalf("LoadDirectory: %v", err)
	}
	want := map[string]string{"user:ana": "user:bob", "user:bob": "user:dana", "user:dana": "user:dana"}
	if !maps.Equal(dir, want) {
		t.Errorf("directory = %v, want %v", dir, want)
	}
}

// A malformed directory is an error, not an empty directory: entries must
// be principal IDs (user:<id>), and the file must be a JSON object of
// strings.
func TestLoadDirectory_Malformed(t *testing.T) {
	for name, body := range map[string]string{
		"not json":         `ana: bob`,
		"not strings":      `{"user:ana": 3}`,
		"untyped employee": `{"ana": "user:bob"}`,
		"untyped manager":  `{"user:ana": "bob"}`,
	} {
		path := filepath.Join(t.TempDir(), "hr.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := connstub.LoadDirectory(path); err == nil {
			t.Errorf("%s: LoadDirectory accepted %s", name, body)
		}
	}
	if _, err := connstub.LoadDirectory(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Error("LoadDirectory accepted a missing file")
	}
}
