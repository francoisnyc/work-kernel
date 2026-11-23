// Tests for Options.Clock (Ventoux item 5): stories run in simulated time,
// so the item service's clock must be the story's. With a Clock, every
// write is stamped with its time instead of the database's (the event's
// created_at and cause.at), and `today` follows it when Now is not set.
// Like ClockSkew, nothing a deployment can set reaches it.
package itemservice_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

// storyClock is a clock a test moves by hand, as a story's simulated time
// moves.
type storyClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *storyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *storyClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = t
}

// Every write is stamped with the clock's time, to the microsecond, and a
// later time on the clock is a later stamp.
func TestClock_StampsEveryWrite(t *testing.T) {
	day := time.Date(2026, 12, 7, 9, 0, 0, 0, time.UTC)
	clock := &storyClock{now: day}
	k := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: true, Clock: clock.Now})
	id := createTicket(t, k, map[string]any{"f_title": "clock " + word()})

	clock.set(day.Add(49*time.Hour + 30*time.Second))
	if _, err := k.Update(testCtx(t), itemservice.UpdateRequest{
		IdempotencyKey: newKey(), Principal: alice, ItemID: id, Fields: map[string]any{"f_title": "later"},
	}); err != nil {
		t.Fatalf("update: %v", err)
	}

	at, _ := eventTimes(t, id)
	if len(at) != 2 {
		t.Fatalf("%d events, want 2", len(at))
	}
	if !at[0].Equal(day) {
		t.Errorf("the create is stamped %v, want the clock's %v", at[0].UTC(), day)
	}
	if want := day.Add(49*time.Hour + 30*time.Second); !at[1].Equal(want) {
		t.Errorf("the update is stamped %v, want the clock's %v", at[1].UTC(), want)
	}
	var itemAt time.Time
	if err := pool.QueryRow(testCtx(t), `select created_at from items where id = $1`, id).Scan(&itemAt); err != nil {
		t.Fatalf("read the Item's time: %v", err)
	}
	if !itemAt.Equal(day) {
		t.Errorf("the Item is stamped %v, want its create's %v", itemAt.UTC(), day)
	}
}

// cause.at is the clock's time, and so is `today`, for a guard and for an
// effect alike, when Now is not set: the guard of midnightTypeYAML holds
// only on the clock's day, years away from the database's.
func TestClock_CauseAtAndTodayFollowIt(t *testing.T) {
	at := time.Date(2031, 3, 14, 15, 9, 26, 0, time.UTC)
	k := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: true, Clock: func() time.Time { return at }})
	typ := "storyclock-" + uuid.NewString()[:8]
	ty, err := typedef.Parse([]byte(strings.Replace(midnightTypeYAML, "TYPE", typ, 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register: %v", err)
	}
	ana := in(person("ana"), newNamespace(t, k))
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: ana, TypeID: typ, TypeVersion: 1,
		Fields: map[string]any{"f_day": "2031-03-14"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: res.ItemID, ToState: "s_done",
	}); err != nil {
		t.Fatalf("close (the guard wants today to be the clock's day, 14 March 2031): %v", err)
	}
	fields := fieldsOf(t, k, ana, res.ItemID)
	if want := at.Format(typedef.DateTimeLayout); fields["f_closed_at"] != want {
		t.Errorf("f_closed_at = %v, want the clock's %s", fields["f_closed_at"], want)
	}
	if fields["f_closed_on"] != "2031-03-14" {
		t.Errorf("f_closed_on = %v, want the clock's day 2031-03-14", fields["f_closed_on"])
	}
}

// Now, when set, still decides `today` for guards; the stamp is the
// clock's. (Only stories set Clock, and they leave Now unset.)
func TestClock_NowStillDecidesTheGuardsDay(t *testing.T) {
	clockAt := time.Date(2031, 3, 14, 15, 0, 0, 0, time.UTC)
	k := itemservice.NewWithOptions(pool, itemservice.Options{CacheTypes: true,
		Clock: func() time.Time { return clockAt }, Now: func() time.Time { return clockAt.AddDate(0, 0, 1) }})
	typ := "storyclock-" + uuid.NewString()[:8]
	ty, err := typedef.Parse([]byte(strings.Replace(midnightTypeYAML, "TYPE", typ, 1)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register: %v", err)
	}
	ana := in(person("ana"), newNamespace(t, k))
	res, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: ana, TypeID: typ, TypeVersion: 1,
		Fields: map[string]any{"f_day": "2031-03-15"},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := k.Transition(testCtx(t), itemservice.TransitionRequest{
		IdempotencyKey: newKey(), Principal: ana, ItemID: res.ItemID, ToState: "s_done",
	}); err != nil {
		t.Fatalf("close (the guard reads Now's day, 15 March): %v", err)
	}
	at, _ := eventTimes(t, res.ItemID)
	if !at[1].Equal(clockAt) {
		t.Errorf("the closing event is stamped %v, want the clock's %v", at[1].UTC(), clockAt)
	}
}

// Options.Clock is for tests only. Go cannot make a struct field
// test-only, so this test reads the module's source: outside this package
// and the story runner (internal/story, a test harness no binary links,
// which internal/story's own test checks), no non-test file sets a field
// named Clock of itemservice.Options.
func TestClock_NoProductionCodeSetsIt(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (strings.HasPrefix(name, ".") || name == "node_modules" || name == "vendor") {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
			return nil
		}
		if strings.HasPrefix(rel, "internal/itemservice/") || strings.HasPrefix(rel, "internal/story/") {
			return nil
		}
		checked++
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// A file that does not import the item service cannot reach its
		// Options; one that does must not name a Clock field in them.
		f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		imports := false
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "github.com/francoisnyc/work-kernel/internal/itemservice" {
				imports = true
			}
		}
		if imports && (strings.Contains(string(src), "Clock:") || strings.Contains(string(src), ".Clock =")) {
			t.Errorf("%s sets a Clock and imports the item service: only tests and internal/story may set Options.Clock", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 50 {
		t.Errorf("read %d Go files; the test is not looking where it should", checked)
	}
}
