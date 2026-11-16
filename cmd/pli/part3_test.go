// Tests for Step 0, part 3: pli's saved sessions follow XDG_CONFIG_HOME on
// every OS (so a script can keep them out of the user's config); a
// SESSION_INVALID error is followed by a hint; agent approve sends the
// audience it printed, or the one --expect-audience names.
//
//	agent approve SESSION SCOPE [--expect-audience namespace|restricted] [--yes] [--request-id ID]
//
// With --expect-audience pli does not ask the server for the audience: it
// prints the one named, still needs --yes from a script, and the server
// refuses with AUDIENCE_CHANGED if it is not the audience at commit.
package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionPath_FollowsXDGConfigHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	got, err := sessionPath("bot")
	if err != nil || got != filepath.Join(dir, "pli", "sessions", "bot") {
		t.Errorf("sessionPath with XDG_CONFIG_HOME = %q, %v; want it under %s", got, err, dir)
	}

	// Unset, it falls back to the OS's config directory.
	t.Setenv("XDG_CONFIG_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home) // os.UserConfigDir reads it when XDG_CONFIG_HOME is empty
	want, err := os.UserConfigDir()
	if err != nil {
		t.Skipf("no OS config directory here: %v", err)
	}
	if got, err := sessionPath("bot"); err != nil || got != filepath.Join(want, "pli", "sessions", "bot") {
		t.Errorf("sessionPath without XDG_CONFIG_HOME = %q, %v; want it under %s", got, err, want)
	}
}

// A saved session that is gone (or another namespace's, or another user's)
// is SESSION_INVALID; pli says what to do after the error lines.
func TestPli_SessionInvalidHint(t *testing.T) {
	name := create(t, "ana", "--type", "ticket@1")
	r := fails(t, "bot", "UNAUTHENTICATED SESSION_INVALID", "--session", "ses_made-up", "item", "get", name)
	hint := strings.Index(r.stderr, "hint:")
	if hint < 0 || hint < strings.Index(r.stderr, "error:") {
		t.Fatalf("stderr %q has no hint after the error", r.stderr)
	}
	if !strings.Contains(r.stderr[hint:], "pli agent start") {
		t.Errorf("hint %q does not say to run pli agent start", r.stderr[hint:])
	}
	// Other errors have no such hint.
	if r := fails(t, "ana", "NOT_FOUND NOT_FOUND", "item", "get", "namespaces/acme/items/00000000-0000-4000-8000-000000000000"); strings.Contains(r.stderr, "hint:") {
		t.Errorf("NOT_FOUND printed a hint: %q", r.stderr)
	}
}

// --expect-audience: approve only for the audience named. The wrong one is
// AUDIENCE_CHANGED with both audiences printed; nothing is approved.
func TestPli_ApproveExpectAudience(t *testing.T) {
	room := create(t, "ana", "--type", "channel@1", "--set", "f_name=quiet-room", "--narrow", "user:ana,agent:triage-bot")
	private := create(t, "ana", "--type", "ticket@1", "--in", room, "--narrow", "user:ana,agent:triage-bot")
	session := ok(t, "bot", "agent", "start", "--for", "user:ana", "--scopes", room,
		"--powers", "read,create")["session"].(map[string]any)["id"].(string)
	ok(t, "bot", "item", "get", private)
	fails(t, "bot", "PERMISSION_DENIED NEEDS_APPROVAL", "item", "create", "--type", "ticket@1", "--in", room)

	// It still needs --yes from a script, and says the audience it would send.
	r := pli(t, "ana", "agent", "approve", session, room, "--expect-audience", "namespace")
	if r.code != 2 || !strings.Contains(r.stderr, "--yes") ||
		!strings.Contains(r.stderr, "Content from this agent session will become visible to everyone in namespace acme") {
		t.Fatalf("without --yes: exit %d, stderr %q; want exit 2, the audience named, --yes", r.code, r.stderr)
	}

	r = fails(t, "ana", "FAILED_PRECONDITION AUDIENCE_CHANGED", "agent", "approve", session, room,
		"--expect-audience", "namespace", "--yes")
	if !strings.Contains(r.stderr, "audience: restricted") || !strings.Contains(r.stderr, "expected_audience: namespace") {
		t.Errorf("stderr %q does not print both audiences", r.stderr)
	}
	fails(t, "bot", "PERMISSION_DENIED NEEDS_APPROVAL", "item", "create", "--type", "ticket@1", "--in", room)

	r = pli(t, "ana", "agent", "approve", session, room, "--expect-audience", "restricted", "--yes")
	if r.code != 0 || !strings.Contains(r.stderr, "the readers of "+room) {
		t.Fatalf("matching --expect-audience: exit %d, stderr %q; want success naming the readers of %s", r.code, r.stderr, room)
	}
	ok(t, "bot", "item", "create", "--type", "ticket@1", "--in", room)
}

func TestPli_ExpectAudienceUsage(t *testing.T) {
	r := pli(t, "ana", "agent", "approve", "ses_x", "namespace", "--expect-audience", "everyone", "--yes")
	if r.code != 2 || !strings.Contains(r.stderr, "--expect-audience") {
		t.Errorf("--expect-audience everyone: exit %d, stderr %q; want exit 2 naming the flag", r.code, r.stderr)
	}
}
