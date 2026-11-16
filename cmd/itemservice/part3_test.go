// Tests for Step 0, part 3: what the item service needs to run in the
// compose stack.
//
//   - GET /healthz answers 200 when the database answers a ping and 503 when
//     it does not, with no body either way (no error text, no database
//     details).
//   - --probe URL checks a running server's /healthz and exits: nil on 200,
//     an error otherwise. The distroless images have no shell or curl, so
//     the container's health check runs the binary itself. It needs no
//     --dev-tokens and no database.
//   - In dev-token mode the roles the token file names are added at start,
//     per namespace, each one logged; roles in the database that the file no
//     longer names are kept and warned about. Without --dev-tokens nothing is
//     added: the server refuses to start before it touches the database.
package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/francoisnyc/work-kernel/internal/testdb"
)

// The database is started on first use only: most tests here need none.
var (
	dbOnce sync.Once
	dbPool *pgxpool.Pool
	dbStop func()
	dbErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if dbStop != nil {
		dbStop()
	}
	os.Exit(code)
}

// database returns a migrated database's pool and URL.
func database(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dbOnce.Do(func() { dbPool, dbStop, dbErr = testdb.Start(context.Background()) })
	if dbErr != nil {
		t.Fatalf("start database: %v", dbErr)
	}
	return dbPool, dbPool.Config().ConnString()
}

// syncBuffer is a bytes.Buffer safe for the server's goroutine to write
// while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func freeAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// --- /healthz ---

func TestHealthz_OKHasNoBody(t *testing.T) {
	rec := httptest.NewRecorder()
	healthz(func(context.Context) error { return nil }).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("healthy: %d, body %q; want 200 and no body", rec.Code, rec.Body.String())
	}
}

func TestHealthz_DownIs503WithNoDetail(t *testing.T) {
	rec := httptest.NewRecorder()
	down := func(context.Context) error {
		return errors.New("dial tcp 10.1.2.3:5432: password authentication failed for user kernel")
	}
	healthz(down).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable || rec.Body.Len() != 0 {
		t.Errorf("database down: %d, body %q; want 503 and no body", rec.Code, rec.Body.String())
	}
}

// --- --probe ---

func TestRun_ProbeHealthy(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer up.Close()
	// No --dev-tokens and no database: probing needs neither.
	err := run(context.Background(), []string{"--probe", up.URL + "/healthz"},
		env(map[string]string{"DATABASE_URL": noDatabase}), io.Discard)
	if err != nil {
		t.Errorf("probe of a healthy server = %v, want nil", err)
	}
}

func TestRun_ProbeUnhealthy(t *testing.T) {
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer down.Close()
	for name, url := range map[string]string{
		"503":         down.URL + "/healthz",
		"unreachable": "http://" + freeAddr(t) + "/healthz",
	} {
		t.Run(name, func(t *testing.T) {
			err := run(context.Background(), []string{"--probe", url}, env(nil), io.Discard)
			if err == nil {
				t.Errorf("probe of %s = nil, want an error", url)
			}
		})
	}
}

// --- a running server: /healthz and roles ---

// serve runs the item service until the test ends and waits for /healthz.
func serve(t *testing.T, dsn, tokens string, stderr *syncBuffer) string {
	t.Helper()
	addr := freeAddr(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"--dev-tokens", tokens, "--types", "../../types", "--listen", addr},
			env(map[string]string{"DATABASE_URL": dsn}), stderr)
	}()
	stopped := false // run returned and its answer was read
	t.Cleanup(func() {
		cancel()
		if stopped {
			return
		}
		if err := <-done; err != nil {
			t.Errorf("run after cancel = %v, want nil", err)
		}
	})
	url := "http://" + addr + "/healthz"
	deadline := time.Now().Add(20 * time.Second)
	for {
		select {
		case err := <-done:
			stopped = true
			t.Fatalf("server stopped at start: %v; stderr %q", err, stderr.String())
		default:
		}
		if res, err := http.Get(url); err == nil {
			body, _ := io.ReadAll(res.Body)
			res.Body.Close()
			if res.StatusCode == http.StatusOK {
				if len(body) != 0 {
					t.Errorf("/healthz body = %q, want none", body)
				}
				return url
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("/healthz not 200 after 20 s")
		}
		time.Sleep(50 * time.Millisecond) // the test waits for the server; not part of the stack
	}
}

func hasRole(t *testing.T, pool *pgxpool.Pool, ns, principal, role string) bool {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(),
		`select count(*) from principal_roles where namespace_id = $1 and principal = $2 and role = $3`,
		ns, principal, role).Scan(&n); err != nil {
		t.Fatalf("read roles: %v", err)
	}
	return n == 1
}

// lineWith reports whether one line of out contains every one of words.
func lineWith(out string, words ...string) bool {
	for _, line := range strings.Split(out, "\n") {
		all := true
		for _, w := range words {
			all = all && strings.Contains(line, w)
		}
		if all {
			return true
		}
	}
	return false
}

func TestRun_AddsRolesFromTheTokenFile(t *testing.T) {
	pool, dsn := database(t)
	ns, other := "ns-"+uuid.NewString()[:8], "ns-"+uuid.NewString()[:8]
	// A role the file no longer names: kept, and warned about.
	if _, err := pool.Exec(context.Background(),
		`insert into principal_roles (namespace_id, principal, role) values ($1, 'user:old-approver', 'approver')`, ns); err != nil {
		t.Fatal(err)
	}
	tokens := tokensFile(t, fmt.Sprintf(`tokens:
  - {name: ana, token: dev-ana-%[1]s, principal: "user:ana", namespace: %[1]s}
  - {name: bob, token: dev-bob-%[1]s, principal: "user:bob", namespace: %[1]s, roles: [approver]}
  - {name: bob-other, token: dev-bob-%[2]s, principal: "user:bob", namespace: %[2]s}
`, ns, other))

	var stderr syncBuffer
	serve(t, dsn, tokens, &stderr)
	out := stderr.String()

	if !hasRole(t, pool, ns, "user:bob", "approver") {
		t.Errorf("bob is not an approver in %s", ns)
	}
	// Roles are per namespace: the same principal elsewhere has none.
	if hasRole(t, pool, other, "user:bob", "approver") {
		t.Errorf("bob is an approver in %s, which the file does not say", other)
	}
	if !lineWith(out, "role", ns, "user:bob", "approver") {
		t.Errorf("stderr does not log the role it added (%s user:bob approver):\n%s", ns, out)
	}
	if !hasRole(t, pool, ns, "user:old-approver", "approver") {
		t.Errorf("the unlisted role was removed; EnsureRoles only adds")
	}
	if !lineWith(out, "WARNING", ns, "user:old-approver", "approver") {
		t.Errorf("stderr does not warn about the role the file no longer names:\n%s", out)
	}

	// A second start adds nothing, so it logs no role as added.
	var again syncBuffer
	serve(t, dsn, tokens, &again)
	if lineWith(again.String(), "role", "user:bob", "approver") {
		t.Errorf("second start logs bob's role again:\n%s", again.String())
	}
}

// Roles come from the token file in --dev-tokens mode only. Without the flag
// the server refuses to start, before it touches the database.
func TestRun_NoRolesWithoutDevTokens(t *testing.T) {
	pool, dsn := database(t)
	ns := "ns-" + uuid.NewString()[:8]
	tokens := tokensFile(t, fmt.Sprintf(
		"tokens:\n  - {name: bob, token: dev-bob-%[1]s, principal: \"user:bob\", namespace: %[1]s, roles: [approver]}\n", ns))
	_ = tokens // on disk, but not passed

	err := run(context.Background(), []string{"--types", "../../types", "--listen", freeAddr(t)},
		env(map[string]string{"DATABASE_URL": dsn}), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--dev-tokens") {
		t.Fatalf("run without --dev-tokens = %v, want the refusal", err)
	}
	if hasRole(t, pool, ns, "user:bob", "approver") {
		t.Errorf("a role was added without --dev-tokens")
	}
}
