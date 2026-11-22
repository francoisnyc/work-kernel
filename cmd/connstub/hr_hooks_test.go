// Tests for the hook protocol spike: cmd/connstub serves the HR directory
// as a pli.hooks/v1 connector on --hr-listen, signed with the secret in the
// environment variable --hr-hook-secret-env names (HR_HOOK_SECRET in the
// compose stack, which gives the worker the same one). Without the flag
// the connector refuses every hook request (401): it never answers
// unsigned. A flag naming an unset variable stops connstub at start.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/hookproto"
)

// startHR runs connstub with the stack's HR directory and extra flags,
// and waits for the HR directory's /healthz.
func startHR(t *testing.T, extra ...string) string {
	t.Helper()
	hrAddr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	args := append([]string{"--crm-listen", freeAddr(t), "--erp-listen", freeAddr(t),
		"--hr-listen", hrAddr, "--hr-directory", "../../deploy/hr-directory.json"}, extra...)
	go func() { done <- run(ctx, args, io.Discard) }()
	t.Cleanup(func() {
		cancel()
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
			done <- err // for the cleanup
			t.Fatalf("connstub stopped at start: %v", err)
		default:
		}
		if res, err := http.Get(hr + "/healthz"); err == nil {
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return hr
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s/healthz not 200 after 5 s", hr)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// postManagerOf sends one manager_of check for createdBy, signed with
// secret (unsigned when empty).
func postManagerOf(t *testing.T, base, secret, createdBy string) (int, hookproto.Response) {
	t.Helper()
	req := hookproto.Request{
		Protocol: hookproto.Protocol, Kind: hookproto.KindCheck, RequestID: "req-1", IdempotencyKey: "hold:cmd-" + createdBy,
		Attempt: 1, Hook: hookproto.HookRef{Binding: "vacation_request@3#hr_check", Connector: "hr@1", Operation: "manager_of"},
		Event:      hookproto.Event{ID: "item-1:2", Type: hookproto.EventTransitionRequested, Subject: "item/item-1"},
		Item:       hookproto.ItemRef{ID: "item-1", Type: "vacation_request", TypeVersion: 3},
		Transition: &hookproto.Transition{From: "draft", To: "submitted"},
		Inputs:     map[string]any{"onBehalfOf": nil, "createdBy": createdBy},
		Trace:      hookproto.Trace{CausationID: "item-1:2"},
		Connection: hookproto.ConnectionRef{Slot: "hr", Ref: "hr-directory"},
	}
	body, _ := json.Marshal(req)
	r, _ := http.NewRequest(http.MethodPost, base+hookproto.HooksPath, bytes.NewReader(body))
	r.Header.Set("Content-Type", hookproto.ContentType)
	if secret != "" {
		s, err := hookproto.ParseSecret(secret)
		if err != nil {
			t.Fatalf("secret: %v", err)
		}
		hookproto.SetSignatureHeaders(r.Header, s, hookproto.WebhookID(req.IdempotencyKey), time.Now(), body)
	}
	res, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatalf("POST hooks: %v", err)
	}
	defer res.Body.Close()
	var out hookproto.Response
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestRun_ServesTheHRConnector(t *testing.T) {
	secret := hookproto.GenerateSecret()
	t.Setenv("TEST_HR_HOOK_SECRET", secret)
	hr := startHR(t, "--hr-hook-secret-env", "TEST_HR_HOOK_SECRET")

	code, res := postManagerOf(t, hr, secret, "user:ana")
	if code != 200 || res.Outcome != hookproto.OutcomeCorrect || res.Patch["manager"] != "user:bob" {
		t.Errorf("manager_of ana = %d %+v, want correct user:bob", code, res)
	}
	if code, _ := postManagerOf(t, hr, hookproto.GenerateSecret(), "user:ana"); code != http.StatusUnauthorized {
		t.Errorf("signed with another secret = %d, want 401", code)
	}
	// The REST endpoint is still served.
	if code, out := getJSON(t, "GET", hr+"/employees/user:ana/manager", ""); code != 200 || out["manager"] != "user:bob" {
		t.Errorf("REST manager of ana = %d %v", code, out)
	}
}

func TestRun_HRConnectorWithoutASecretRefusesEverything(t *testing.T) {
	hr := startHR(t)
	if code, _ := postManagerOf(t, hr, "", "user:ana"); code != http.StatusUnauthorized {
		t.Errorf("unsigned, no secret configured = %d, want 401", code)
	}
	if code, _ := postManagerOf(t, hr, hookproto.GenerateSecret(), "user:ana"); code != http.StatusUnauthorized {
		t.Errorf("signed, no secret configured = %d, want 401", code)
	}
}

func TestRun_HRSecretFromAnUnsetVariableIsRefused(t *testing.T) {
	t.Setenv("TEST_HR_HOOK_SECRET_UNSET", "")
	err := run(context.Background(), []string{"--crm-listen", freeAddr(t), "--erp-listen", freeAddr(t),
		"--hr-listen", freeAddr(t), "--hr-hook-secret-env", "TEST_HR_HOOK_SECRET_UNSET"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "TEST_HR_HOOK_SECRET_UNSET") {
		t.Errorf("run with an unset secret variable = %v, want an error naming it", err)
	}
	t.Setenv("TEST_HR_HOOK_SECRET_BAD", "not-a-secret")
	err = run(context.Background(), []string{"--crm-listen", freeAddr(t), "--erp-listen", freeAddr(t),
		"--hr-listen", freeAddr(t), "--hr-hook-secret-env", "TEST_HR_HOOK_SECRET_BAD"}, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "TEST_HR_HOOK_SECRET_BAD") {
		t.Errorf("run with a malformed secret = %v, want an error naming the variable", err)
	}
}
