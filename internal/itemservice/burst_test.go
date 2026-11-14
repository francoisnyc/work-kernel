// Tests for Step 5: a small burst of chat messages into one channel. The
// full 10,000-message burst is cmd/bench (make bench); this keeps the two
// properties it reports protected in CI: no outbox rows when nothing listens,
// and a fixed number of rows per message.
package itemservice_test

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/typedef"
)

const (
	burstMessages = 200
	burstWriters  = 8
)

// registerFromFile registers a Type from types/. It is the same definition
// the bench uses, so the test and the bench measure the same thing.
func registerFromFile(t *testing.T, k *itemservice.Service, name string) {
	t.Helper()
	data, err := os.ReadFile("../../types/" + name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	ty, err := typedef.Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	if err := k.RegisterType(testCtx(t), ty); err != nil {
		t.Fatalf("register %s: %v", name, err)
	}
}

// tableRows counts every row of every table a write can touch. Tests in this
// package run one at a time, so the difference across a test is that test's.
func tableRows(t *testing.T) map[string]int {
	t.Helper()
	rows := map[string]int{}
	for _, table := range []string{
		"items", "item_events", "item_event_values", "outbox", "idempotency",
		"scopes", "scope_readers", "grants", "principal_roles", "types",
	} {
		rows[table] = count(t, "select count(*) from "+table)
	}
	return rows
}

// 200 messages from 8 writers into one channel: each message costs exactly
// one Item row, one event, one values row and one idempotency row, and
// nothing else. In particular: no outbox row, and no new scope.
func TestBurst_MessagesIntoOneChannel_RowsPerMessage(t *testing.T) {
	k := itemservice.New(pool)
	registerFromFile(t, k, "channel.v1.yaml")
	registerFromFile(t, k, "message.v1.yaml")

	channel, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "channel", TypeVersion: 1,
		Fields: map[string]any{"f_name": "general"},
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, channel.ItemID); n != 0 {
		t.Errorf("channel outbox rows = %d, want 0", n)
	}

	// The channel's first message gives it its scope node (Step A). Send it
	// before counting, so the burst measures the steady state: 4 rows per
	// message and no new scope.
	if _, err := k.Create(testCtx(t), itemservice.CreateRequest{
		IdempotencyKey: newKey(), Principal: alice, TypeID: "message", TypeVersion: 1,
		ContainerID: &channel.ItemID, Fields: map[string]any{"f_body": "first"},
	}); err != nil {
		t.Fatalf("first message: %v", err)
	}

	before := tableRows(t)

	// Each writer sends every burstWriters-th message. A buffered channel
	// collects errors from the goroutines; the test reads them afterwards.
	errs := make(chan error, burstMessages)
	var wg sync.WaitGroup
	for w := range burstWriters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := w; i < burstMessages; i += burstWriters {
				_, err := k.Create(testCtx(t), itemservice.CreateRequest{
					IdempotencyKey: "burst-" + uuid.NewString(), Principal: alice,
					TypeID: "message", TypeVersion: 1, ContainerID: &channel.ItemID,
					Fields: map[string]any{"f_body": fmt.Sprintf("message %d", i)},
				})
				if err != nil {
					errs <- fmt.Errorf("message %d: %w", i, err)
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	after := tableRows(t)
	want := map[string]int{"items": 1, "item_events": 1, "item_event_values": 1, "idempotency": 1}
	total := 0
	for table, b := range before {
		added := after[table] - b
		total += added
		if added != want[table]*burstMessages {
			t.Errorf("%s: %d rows added, want %d (%d per message)",
				table, added, want[table]*burstMessages, want[table])
		}
	}
	if total != 4*burstMessages {
		t.Errorf("rows added = %d, want %d: 4 per message", total, 4*burstMessages)
	}

	// Every message is in the channel, in the channel's scope, at item_seq 1.
	n := count(t, `select count(*) from items m join items c on c.id = m.container_id
	               where c.id = $1 and m.scope_id = c.scope_id and m.item_seq = 1`, channel.ItemID)
	if n != burstMessages+1 { // + the first message, sent before counting
		t.Errorf("messages in the channel = %d, want %d", n, burstMessages+1)
	}
}
