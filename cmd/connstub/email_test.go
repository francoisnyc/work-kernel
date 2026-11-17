// Tests for proof-of-concept Step 2, part 3: cmd/connstub also serves the
// email stub on --email-listen (inside the stack: the worker sends there),
// and its read-only reader on --email-read-listen (the port the dev compose
// file publishes for the scenario).
package main

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestRun_ServesTheEmailStubAndItsReader(t *testing.T) {
	emailAddr, readerAddr := freeAddr(t), freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--crm-listen", freeAddr(t), "--erp-listen", freeAddr(t),
			"--email-listen", emailAddr, "--email-read-listen", readerAddr}, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("run did not stop after cancel")
		}
	})
	waitUp := func(url string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if res, err := http.Get(url); err == nil {
				res.Body.Close()
				if res.StatusCode == http.StatusOK {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("%s did not come up", url)
	}
	waitUp("http://" + emailAddr + "/healthz")
	waitUp("http://" + readerAddr + "/messages")

	body := []byte(`{"to":"user:ana","template":"notify_approved","item":"i-1","key":"k-1"}`)
	res, err := http.Post("http://"+emailAddr+"/messages", "application/json", bytes.NewReader(body))
	if err != nil || res.StatusCode != http.StatusOK {
		t.Fatalf("send = %v, %v", res, err)
	}
	res.Body.Close()
	res, err = http.Post("http://"+readerAddr+"/messages", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Error("the reader accepted a message: it must be read-only")
	}
	res, err = http.Get("http://" + readerAddr + "/messages?item=i-1")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !bytes.Contains(got, []byte(`"template":"notify_approved"`)) {
		t.Errorf("the reader shows %s, want the message", got)
	}
}
