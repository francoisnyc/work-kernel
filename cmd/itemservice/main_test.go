// Tests for Step 0, part 1: how the item service starts. Dev-token auth is
// the only auth there is, so the server refuses to start without an explicit
// --dev-tokens flag, and warns on every start that uses it. These checks come
// before the database, so the tests need none.
package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noDatabase is a URL nothing listens on: run must fail after its start-up
// checks, at the connection.
const noDatabase = "postgres://kernel:kernel@127.0.0.1:1/kernel?sslmode=disable&connect_timeout=1"

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func tokensFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dev-tokens.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const oneToken = "tokens:\n  - {name: ana, token: dev-ana, principal: \"user:ana\", namespace: acme}\n"

func TestRun_RefusesWithoutDevTokens(t *testing.T) {
	var stderr bytes.Buffer
	err := run(context.Background(), []string{"--types", "../../types"},
		env(map[string]string{"DATABASE_URL": noDatabase}), &stderr)
	if err == nil || !strings.Contains(err.Error(), "--dev-tokens") {
		t.Fatalf("run without --dev-tokens = %v, want a refusal naming the flag", err)
	}
}

func TestRun_WarnsOnEveryStartWithDevTokens(t *testing.T) {
	var stderr bytes.Buffer
	err := run(context.Background(),
		[]string{"--dev-tokens", tokensFile(t, oneToken), "--types", "../../types"},
		env(map[string]string{"DATABASE_URL": noDatabase}), &stderr)
	if err == nil {
		t.Fatal("run with no database succeeded")
	}
	if out := stderr.String(); !strings.Contains(out, "WARNING") || !strings.Contains(out, "dev tokens") {
		t.Errorf("stderr = %q, want a WARNING about dev tokens", out)
	}
}

func TestRun_BadTokensFileIsNamed(t *testing.T) {
	path := tokensFile(t, "tokens:\n  - {name: ana, token: t, principal: ana, namespace: acme}\n")
	err := run(context.Background(), []string{"--dev-tokens", path, "--types", "../../types"},
		env(map[string]string{"DATABASE_URL": noDatabase}), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("run = %v, want an error naming %s", err, path)
	}
}

func TestRun_BadTypesDirIsRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "broken.yaml"), []byte("id: broken\nversion: one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := run(context.Background(), []string{"--dev-tokens", tokensFile(t, oneToken), "--types", dir},
		env(map[string]string{"DATABASE_URL": noDatabase}), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "broken.yaml") {
		t.Fatalf("run = %v, want an error naming broken.yaml", err)
	}
}
