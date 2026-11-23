// The tests' shared setup. Postgres starts only when a test asks for it
// (testEnv), so the tests of parsing, checking, comparing and rendering run
// without Docker.
package story

import (
	"context"
	"log"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/francoisnyc/work-kernel/internal/testdb"
)

var (
	envOnce sync.Once
	envErr  error
	shared  *Env
	stopDB  = func() {}
)

func TestMain(m *testing.M) {
	code := m.Run()
	stopDB()
	os.Exit(code)
}

// testEnv is the environment every run of these tests shares: one
// Postgres, with every Type of types/ registered.
func testEnv(t *testing.T) *Env {
	t.Helper()
	envOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		pool, stop, err := testdb.Start(ctx)
		if err != nil {
			envErr = err
			return
		}
		stopDB = stop
		shared, envErr = NewEnv(ctx, pool, "../../types")
		if envErr != nil {
			log.Printf("NewEnv: %v", envErr)
		}
	})
	if envErr != nil {
		t.Fatalf("the test environment: %v", envErr)
	}
	return shared
}

// runCtx bounds one run: a story that waits on real time fails here
// instead of hanging the suite.
func runCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)
	return ctx
}
