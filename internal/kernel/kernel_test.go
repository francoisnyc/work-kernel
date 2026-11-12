// Tests for Step 1: Create, Get and idempotency keys.
//
// The package name ends in _test, so these tests see only the exported API of
// kernel, like a Java test living in a different package.
package kernel_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/jackc/pgx/v5/stdlib" // registers the "pgx" driver for database/sql (used by goose)
	"github.com/pressly/goose/v3"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/kernel"
	"github.com/francoisnyc/work-kernel/migrations"
)

// pool is shared by all tests in this package. Tests never count whole tables;
// they count rows for their own item ID or idempotency key, so they can share
// one database.
var pool *pgxpool.Pool

// TestMain runs once for the package, like JUnit's @BeforeAll/@AfterAll.
// It starts a throwaway Postgres, applies the real migrations, runs the tests.
func TestMain(m *testing.M) {
	os.Exit(runTests(m))
}

func runTests(m *testing.M) int {
	ctx := context.Background()

	pg, err := postgres.Run(ctx, "postgres:17-alpine",
		postgres.WithDatabase("kernel"),
		postgres.WithUsername("kernel"),
		postgres.WithPassword("kernel"),
		postgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("start postgres: %v", err)
		return 1
	}
	// defer runs when the function returns, like a finally block.
	defer func() { _ = testcontainers.TerminateContainer(pg) }()

	dsn, err := pg.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		log.Printf("connection string: %v", err)
		return 1
	}
	if err := migrate(ctx, dsn); err != nil {
		log.Printf("migrate: %v", err)
		return 1
	}

	pool, err = pgxpool.New(ctx, dsn)
	if err != nil {
		log.Printf("connect: %v", err)
		return 1
	}
	defer pool.Close()

	return m.Run()
}

func migrate(ctx context.Context, dsn string) error {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

// --- helpers ---

func testCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

var alice = item.Principal{Actor: "user-alice"}

// newRequest returns a valid request with a fresh idempotency key.
func newRequest() kernel.CreateRequest {
	return kernel.CreateRequest{
		IdempotencyKey: "key-" + uuid.NewString(),
		Principal:      alice,
		TypeID:         "ticket",
		TypeVersion:    1,
		State:          "open",
		Fields:         map[string]any{"f_title": "Printer on fire", "f_priority": float64(2)},
	}
}

// count runs a "select count(*)" query and returns the number.
func count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(testCtx(t), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// assertOneWrite checks that exactly one Item, one event and one outbox row
// exist for the Item, and one idempotency record for the key.
func assertOneWrite(t *testing.T, itemID uuid.UUID, key string) {
	t.Helper()
	if n := count(t, `select count(*) from items where id = $1`, itemID); n != 1 {
		t.Errorf("items rows = %d, want 1", n)
	}
	if n := count(t, `select count(*) from item_events where item_id = $1`, itemID); n != 1 {
		t.Errorf("item_events rows = %d, want 1", n)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1`, itemID); n != 1 {
		t.Errorf("outbox rows = %d, want 1", n)
	}
	if n := count(t, `select count(*) from idempotency where key = $1`, key); n != 1 {
		t.Errorf("idempotency rows = %d, want 1", n)
	}
}

// --- Create ---

func TestCreate_WritesItemEventAndOutbox(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	req := newRequest()
	req.Principal = item.Principal{Actor: "agent-7", OnBehalfOf: "user-alice"}

	res, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if res.ItemID == uuid.Nil {
		t.Error("ItemID is the zero UUID")
	}
	if res.ItemSeq != 1 {
		t.Errorf("ItemSeq = %d, want 1", res.ItemSeq)
	}
	if res.CommitSeq <= 0 {
		t.Errorf("CommitSeq = %d, want > 0", res.CommitSeq)
	}
	assertOneWrite(t, res.ItemID, req.IdempotencyKey)

	// The event carries the same sequence numbers as the result, and who acted.
	var (
		itemSeq, commitSeq int64
		actor, op          string
		onBehalfOf         *string
	)
	err = pool.QueryRow(ctx,
		`select item_seq, commit_seq, actor, on_behalf_of, op from item_events where item_id = $1`,
		res.ItemID).Scan(&itemSeq, &commitSeq, &actor, &onBehalfOf, &op)
	if err != nil {
		t.Fatalf("read event: %v", err)
	}
	if itemSeq != res.ItemSeq || commitSeq != res.CommitSeq {
		t.Errorf("event seqs = (%d, %d), want (%d, %d)", itemSeq, commitSeq, res.ItemSeq, res.CommitSeq)
	}
	if actor != "agent-7" || onBehalfOf == nil || *onBehalfOf != "user-alice" {
		t.Errorf("event actor = %q, on_behalf_of = %v", actor, onBehalfOf)
	}
	if op != "create" {
		t.Errorf("event op = %q, want %q", op, "create")
	}

	// The outbox row points at that event and is not delivered yet.
	if n := count(t,
		`select count(*) from outbox where item_id = $1 and item_seq = 1 and delivered_at is null`,
		res.ItemID); n != 1 {
		t.Errorf("undelivered outbox rows for item_seq 1 = %d, want 1", n)
	}
}

// This is the step's "done when".
func TestCreate_SameKeyTwice_ReturnsSameResultAndWritesOnce(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	req := newRequest()

	first, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}

	if second != first {
		t.Errorf("second result = %+v, want %+v", second, first)
	}
	assertOneWrite(t, first.ItemID, req.IdempotencyKey)
}

func TestCreate_SameKeyDifferentRequest_IsRejected(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	req := newRequest()

	first, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}

	changed := req
	changed.Fields = map[string]any{"f_title": "Something else"}
	_, err = k.Create(ctx, changed)
	if !errors.Is(err, kernel.ErrIdempotencyKeyReuse) {
		t.Fatalf("second Create error = %v, want ErrIdempotencyKeyReuse", err)
	}

	// Nothing more was written.
	assertOneWrite(t, first.ItemID, req.IdempotencyKey)
}

func TestCreate_ConcurrentSameKey_WritesOnce(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	req := newRequest()

	const callers = 8
	results := make([]kernel.WriteResult, callers)
	errs := make([]error, callers)

	// Goroutines are lightweight threads; WaitGroup plays the role of a
	// CountDownLatch. Each goroutine writes only its own slice index.
	var wg sync.WaitGroup
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], errs[i] = k.Create(ctx, req)
		}()
	}
	wg.Wait()

	for i := range callers {
		if errs[i] != nil {
			t.Fatalf("caller %d: %v", i, errs[i])
		}
		if results[i] != results[0] {
			t.Errorf("caller %d result = %+v, want %+v", i, results[i], results[0])
		}
	}
	assertOneWrite(t, results[0].ItemID, req.IdempotencyKey)
}

func TestCreate_DifferentKeys_CreateDifferentItems(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)

	first, err := k.Create(ctx, newRequest())
	if err != nil {
		t.Fatalf("first Create: %v", err)
	}
	second, err := k.Create(ctx, newRequest())
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}

	if first.ItemID == second.ItemID {
		t.Error("two keys produced the same ItemID")
	}
	if second.CommitSeq <= first.CommitSeq {
		t.Errorf("CommitSeq did not increase: %d then %d", first.CommitSeq, second.CommitSeq)
	}
	if first.ItemSeq != 1 || second.ItemSeq != 1 {
		t.Errorf("ItemSeq = %d and %d, want 1 and 1", first.ItemSeq, second.ItemSeq)
	}
}

func TestCreate_WithoutIdempotencyKey_IsRejected(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	req := newRequest()
	req.IdempotencyKey = ""
	req.TypeID = "type-" + uuid.NewString() // unique, so we can check nothing was written

	_, err := k.Create(ctx, req)
	if !errors.Is(err, kernel.ErrInvalid) {
		t.Fatalf("Create error = %v, want ErrInvalid", err)
	}
	if n := count(t, `select count(*) from items where type_id = $1`, req.TypeID); n != 0 {
		t.Errorf("items rows = %d, want 0", n)
	}
}

// Kernel rule: no personal data in item_events.payload. Field values can be
// free text, so the create event records which fields were set, not their values.
func TestCreate_EventPayloadHasFieldIDsButNoValues(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	req := newRequest()
	req.Fields = map[string]any{
		"f_title":    "Call Jane Doe about her refund",
		"f_reporter": "jane.doe@example.com",
	}

	res, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var raw []byte
	err = pool.QueryRow(ctx,
		`select payload from item_events where item_id = $1`, res.ItemID).Scan(&raw)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}

	for _, secret := range []string{"Jane", "refund", "jane.doe@example.com"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("payload contains field value %q: %s", secret, raw)
		}
	}

	var payload struct {
		FieldIDs []string `json:"field_ids"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode payload %s: %v", raw, err)
	}
	want := []string{"f_reporter", "f_title"} // sorted, so the payload is stable
	if !reflect.DeepEqual(payload.FieldIDs, want) {
		t.Errorf("payload field_ids = %v, want %v", payload.FieldIDs, want)
	}
}

// --- Get ---

func TestGet_ReturnsCreatedItem(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)
	req := newRequest()

	res, err := k.Create(ctx, req)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := k.Get(ctx, alice, res.ItemID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	want := item.Item{
		ID:          res.ItemID,
		TypeID:      "ticket",
		TypeVersion: 1,
		State:       "open",
		Fields:      map[string]any{"f_title": "Printer on fire", "f_priority": float64(2)},
		ItemSeq:     1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Get = %+v\nwant  %+v", got, want)
	}
}

func TestGet_UnknownID_ReturnsNotFound(t *testing.T) {
	ctx := testCtx(t)
	k := kernel.New(pool)

	_, err := k.Get(ctx, alice, uuid.New())
	if !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("Get error = %v, want ErrNotFound", err)
	}
}
