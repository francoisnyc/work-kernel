// Tests for proof-of-concept Step 2, part 1: cmd/connstub also serves the
// stub HR directory, on --hr-listen, from the JSON file --hr-directory
// (deploy/hr-directory.json in the compose stack).
package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRun_ServesTheHRDirectory(t *testing.T) {
	hrAddr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--crm-listen", freeAddr(t), "--erp-listen", freeAddr(t),
			"--hr-listen", hrAddr, "--hr-directory", "../../deploy/hr-directory.json"}, io.Discard)
	}()
	stopped := false // run returned and its answer was read
	t.Cleanup(func() {
		cancel()
		if stopped {
			return
		}
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("run did not stop after cancel")
		}
	})
	hr := "http://" + hrAddr
	deadline := time.Now().Add(5 * time.Second)
	for {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("connstub stopped at start: %v", err)
		default:
		}
		if res, err := http.Get(hr + "/healthz"); err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s/healthz not 200 after 5 s", hr)
		}
		time.Sleep(20 * time.Millisecond)
	}

	for employee, manager := range map[string]string{"user:ana": "user:bob", "user:dana": "user:dana"} {
		if code, out := getJSON(t, "GET", hr+"/employees/"+employee+"/manager", ""); code != 200 || out["manager"] != manager {
			t.Errorf("manager of %s = %d %v, want 200 %s", employee, code, out, manager)
		}
	}
	if code, _ := getJSON(t, "GET", hr+"/employees/user:erin/manager", ""); code != 404 {
		t.Errorf("manager of erin = %d, want 404", code)
	}
}

func TestRun_BadHRDirectoryIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hr.json")
	if err := os.WriteFile(path, []byte(`{"ana": "user:bob"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), []string{"--crm-listen", freeAddr(t), "--erp-listen", freeAddr(t),
		"--hr-listen", freeAddr(t), "--hr-directory", path}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("run with a bad HR directory = %v, want an error naming %s", err, path)
	}
}
