// End-to-end test for track 1, item 6, step 2: after an Item is migrated,
// the workflow runs it by the new version.
//
// A migrate is refused for an Item with a timer on its state, so a
// migrated Item has no run alive. The next state it enters is entered
// under the new version, and the run that starts for it loads the new
// version's timer table: a shift migrated while open, then moved to
// in_progress, goes back to open after version 2's two seconds, not after
// version 1's eight.
package workflow_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

func mshiftSrc(version, after, rest string) string {
	return `
id: mshift
version: ` + version + `
initial_state: open
states:
  - {id: open, category: todo}
  - {id: in_progress, category: active, timer: {after: ` + after + `, to: open}}
  - {id: done, category: done}
transitions:
  - {from: open, to: in_progress}
  - {from: in_progress, to: open}
  - {from: in_progress, to: done}
` + rest
}

func TestMigrate_E2E_TheNextTimerIsTheNewVersions(t *testing.T) {
	s := newStack(t)
	for _, def := range []string{mshiftSrc("1", "8s", ""),
		mshiftSrc("2", "2s", "start:\n  older_versions: all\nmigrate:\n  allowed: ['principal:"+alice.Actor+"']\n")} {
		ty, err := typedef.Parse([]byte(def))
		if err != nil {
			t.Fatalf("parse mshift: %v", err)
		}
		if err := s.k.RegisterType(serverCtx(t), ty); err != nil {
			t.Fatalf("register mshift v%d: %v", ty.Version, err)
		}
	}
	res, err := s.k.Create(serverCtx(t), itemservice.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, TypeID: "mshift", TypeVersion: 1,
	})
	if err != nil {
		t.Fatalf("create mshift v1: %v", err)
	}
	id := res.ItemID
	// The relay delivers the create: until then the Item is not quiet.
	eventually(t, 20*time.Second, "the create delivered", func() bool {
		var n int
		err := pool.QueryRow(serverCtx(t), `select count(*) from outbox where item_id = $1 and delivered_at is null`, id).Scan(&n)
		return err == nil && n == 0
	})

	mig, err := s.k.Migrate(serverCtx(t), itemservice.MigrateRequest{
		IdempotencyKey: "key-" + uuid.NewString(), Principal: alice, ItemID: id, TypeID: "mshift", TypeVersion: 2,
	})
	if err != nil {
		t.Fatalf("migrate the open shift to version 2: %v", err)
	}
	s.move(id, "in_progress")
	entered := mig.ItemSeq + 1

	s.waitState(id, "open", shiftWaitForTimer)
	gap, hook := timerGap(t, id, entered, entered+1)
	if gap < shiftV2Timer || gap > shiftV2Timer+shiftTimerSlack-time.Second {
		t.Errorf("the timer wrote %v after entering in_progress; want version 2's %v (version 1's is %v)", gap, shiftV2Timer, shiftV1Timer)
	}
	if want := "timer:mshift@v2:in_progress"; hook != want {
		t.Errorf("originating_hook = %q, want %q", hook, want)
	}
	s.waitCompleted(id)
}
