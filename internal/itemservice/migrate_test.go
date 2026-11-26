// Tests for track 1, item 6, step 2: Migrate moves one open, quiet Item
// from the Type version it is on to the latest one, checked against it.
//
// Both bake-offs found the hole: after purchase@2 added the CFO's
// approval, the purchases already in flight on purchase@1 were approved
// without the CFO, and nothing could move them.
//
//   - Who may: those the latest version's migrate.allowed names (a role, a
//     named user). Absent: nobody. Never an agent, never a customer. An
//     Item the caller cannot see is ErrNotFound; someone who may not is
//     ErrForbidden, before any check runs.
//   - The target is the version the caller names, which must be the latest
//     registered version of the Item's Type, read in the write's own
//     transaction, and newer than the Item's: else target_not_latest.
//   - The Item fits (lifecycle.Fits, with the facts read under its lock),
//     no sibling holds the same value of a field the new version declares
//     unique, and it is quiet, under BOTH versions: no open hold, no
//     action, enrich hook, reaction or undo unfinished, no timer on its
//     state, no event still in the outbox.
//   - What does not fit is a *NotMigratableError, an ErrRejected, holding
//     every problem. With ValidateOnly the same problems come back with no
//     error, nothing is written and no idempotency key is used.
//   - The write is one transaction: type_version and item_seq on the Item
//     row, one event `migrated` with {from_version, to_version}, an outbox
//     row if either version has listeners, the idempotency record. Fields,
//     state, readers, container and content_seq (the etag) do not change.
//   - No enter reaction, hook or timer of the new version runs for the
//     state the Item is in.
//   - Readers see the new version at once, through policy.Project.
//
// Every test registers Types of its own (startWorld's suffix).
package itemservice_test

import (
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/francoisnyc/work-kernel/internal/item"
	"github.com/francoisnyc/work-kernel/internal/itemservice"
	"github.com/francoisnyc/work-kernel/internal/lifecycle"
)

// migWorld is a startWorld with adm, who holds the role mig_admin.
type migWorld struct {
	startWorld
	adm item.Principal
}

func newMigWorld(t *testing.T) migWorld {
	t.Helper()
	w := migWorld{startWorld: newStartWorld(t)}
	w.adm = in(person("adm"), w.ns)
	grantRoleIn(t, w.ns, w.adm, "mig_admin")
	return w
}

const migAllowed = "migrate:\n  allowed: ['role:mig_admin']\n"

// migPurchaseSrc is a purchase with no hook and no timer: finance approves
// it; from version 2 on, over 5,000 the CFO does. rest is appended.
func migPurchaseSrc(version int, rest string) string {
	head := "\nid: pur_{s}\nversion: " + string(rune('0'+version)) + `
initial_state: draft
fields:
  - {id: f_amount, type: int}
  - {id: f_note, type: string}
  - {id: f_claimed_by, type: principal}
customer_fields: [f_note]
states:
  - {id: draft, category: todo}
  - {id: review, category: active}
`
	if version == 1 {
		return head + `  - {id: approved, category: done}
transitions:
  - {from: draft, to: review}
  - {from: review, to: approved, allowed: ['role:finance'], except: [creator]}
` + rest
	}
	return head + `  - {id: cfo_review, category: waiting}
  - {id: approved, category: done}
transitions:
  - {from: draft, to: review}
  - {from: review, to: approved, allowed: ['role:finance'], except: [creator], guard: 'f_amount <= 5000'}
  - {from: review, to: cfo_review, allowed: ['role:finance'], except: [creator], guard: 'f_amount > 5000'}
  - {from: cfo_review, to: approved, allowed: ['role:cfo'], except: [creator]}
` + rest
}

func (w migWorld) move(t *testing.T, p item.Principal, id uuid.UUID, to string) error {
	t.Helper()
	_, err := w.k.Transition(testCtx(t), itemservice.TransitionRequest{IdempotencyKey: newKey(), Principal: p, ItemID: id, ToState: to})
	return err
}

// inFlight is a 6,000 purchase@1 that eve created and sent to review.
func (w migWorld) inFlight(t *testing.T) uuid.UUID {
	t.Helper()
	res, err := w.create(t, w.eve, w.id("pur"), 1, nil, map[string]any{"f_amount": 6000, "f_note": "servers"})
	id := mustCreate(t, "a purchase@1", res, err)
	if err := w.move(t, w.eve, id, "review"); err != nil {
		t.Fatalf("eve sends the purchase to review: %v", err)
	}
	return id
}

func (w migWorld) migrate(t *testing.T, p item.Principal, id uuid.UUID, typ string, version int) (itemservice.MigrateResult, error) {
	t.Helper()
	return w.k.Migrate(testCtx(t), itemservice.MigrateRequest{IdempotencyKey: newKey(), Principal: p, ItemID: id, TypeID: typ, TypeVersion: version})
}

// dryRun asks what keeps the Item from being migrated, and fails the test
// if asking is itself refused.
func (w migWorld) dryRun(t *testing.T, id uuid.UUID, typ string, version int) []lifecycle.MigrationCheck {
	t.Helper()
	res, err := w.k.Migrate(testCtx(t), itemservice.MigrateRequest{Principal: w.adm, ItemID: id, TypeID: typ, TypeVersion: version, ValidateOnly: true})
	if err != nil {
		t.Fatalf("the dry run: %v", err)
	}
	return checks(res.Problems)
}

func checks(ms []lifecycle.Misfit) []lifecycle.MigrationCheck {
	out := []lifecycle.MigrationCheck{}
	for _, m := range ms {
		out = append(out, m.Check)
	}
	return out
}

// assertNotMigratable checks a refused migrate: an ErrRejected, a
// *NotMigratableError with exactly these checks, in this order.
func assertNotMigratable(t *testing.T, what string, err error, want ...lifecycle.MigrationCheck) []lifecycle.Misfit {
	t.Helper()
	var nm *itemservice.NotMigratableError
	if !errors.Is(err, itemservice.ErrRejected) || !errors.As(err, &nm) {
		t.Errorf("%s: error = %v, want a *NotMigratableError (ErrRejected) with %v", what, err, want)
		return nil
	}
	if got := checks(nm.Problems); !slices.Equal(got, want) {
		t.Errorf("%s: problems %v, want %v", what, got, want)
	}
	return nm.Problems
}

func versionOf(t *testing.T, id uuid.UUID) int {
	t.Helper()
	return count(t, `select type_version from items where id = $1`, id)
}

// deliverAll marks the Item's outbox rows delivered, as the relay would.
func deliverAll(t *testing.T, id uuid.UUID) {
	t.Helper()
	if _, err := pool.Exec(testCtx(t), `update outbox set delivered_at = now() where item_id = $1 and delivered_at is null`, id); err != nil {
		t.Fatalf("mark the outbox delivered: %v", err)
	}
}

// --- the bake-off's row ---

// A purchase in flight on purchase@1, migrated to purchase@2, goes to the
// CFO: finance can no longer approve it alone.
func TestMigrate_TheBakeOffRow_APurchaseInFlightGoesToTheCFO(t *testing.T) {
	w := newMigWorld(t)
	fay, cora := w.bob, w.carl
	grantRoleIn(t, w.ns, fay, "finance")
	grantRoleIn(t, w.ns, cora, "cfo")
	w.reg(t, migPurchaseSrc(1, ""))
	p := w.inFlight(t)
	w.reg(t, migPurchaseSrc(2, migAllowed)) // the change: over 5,000, the CFO too
	seq := itemSeqOf(t, p)

	res, err := w.migrate(t, w.adm, p, w.id("pur"), 2)
	if err != nil {
		t.Fatalf("adm migrates the purchase in flight to purchase@2: %v", err)
	}
	if res.ItemID != p || res.ItemSeq != seq+1 || res.CommitSeq == 0 || len(res.Problems) != 0 {
		t.Errorf("the migrate answered %+v; want the Item at item_seq %d, a commit_seq, no problems", res, seq+1)
	}
	got, err := w.k.Get(testCtx(t), w.eve, p)
	if err != nil || got.TypeVersion != 2 || got.State != "review" || got.Fields["f_note"] != "servers" {
		t.Fatalf("the purchase after the migrate: version %d, state %q, fields %v, %v; want purchase@2, still in review, its fields kept",
			got.TypeVersion, got.State, got.Fields, err)
	}
	if err := w.move(t, fay, p, "approved"); !errors.Is(err, itemservice.ErrRejected) {
		t.Errorf("fay approves the migrated 6,000 purchase: %v, want ErrRejected (the CFO's step)", err)
	}
	if err := w.move(t, fay, p, "cfo_review"); err != nil {
		t.Errorf("fay sends it to the CFO: %v", err)
	}
	if err := w.move(t, cora, p, "approved"); err != nil {
		t.Errorf("cora (cfo) approves: %v", err)
	}
}

// --- who may ---

func TestMigrate_WhoMay(t *testing.T) {
	w := newMigWorld(t)
	nia := in(person("nia"), w.ns)
	w.reg(t, migPurchaseSrc(1, ""))
	// purchase@2 keeps purchase@1 open here, so that each attempt below
	// can have a fresh purchase@1 to migrate.
	named := "migrate:\n  allowed: ['role:mig_admin', 'principal:" + nia.Actor + "']\nstart:\n  older_versions: all\n"
	w.reg(t, migPurchaseSrc(2, named))
	try := func(p item.Principal) error {
		_, err := w.migrate(t, p, w.inFlight(t), w.id("pur"), 2)
		return err
	}
	if err := try(w.adm); err != nil {
		t.Errorf("adm (role mig_admin): %v", err)
	}
	if err := try(nia); err != nil {
		t.Errorf("nia, named by principal:user: %v", err)
	}
	for name, p := range map[string]item.Principal{"eve, the requester": w.eve, "lea (another role)": w.lea} {
		if err := try(p); !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("%s: %v, want ErrForbidden", name, err)
		}
	}
	// Never an agent: not for a role holder, not holding the role itself.
	bot := item.Principal{Actor: "agent:mig-" + word(), OnBehalfOf: w.adm.Actor, Namespace: w.ns}
	grantRoleIn(t, w.ns, item.Principal{Actor: bot.Actor}, "mig_admin")
	if err := try(withSession(t, w.k, bot)); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("an agent with the role, acting for adm: %v, want ErrForbidden", err)
	}
	// Never a customer: one who sees nothing of it gets not found.
	if err := try(w.kim); !errors.Is(err, itemservice.ErrNotFound) && !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("a customer: %v, want ErrNotFound or ErrForbidden", err)
	}

	// An Item the caller cannot see does not exist for them, role or not.
	res, err := w.k.Create(testCtx(t), itemservice.CreateRequest{IdempotencyKey: newKey(), Principal: w.eve, TypeID: w.id("pur"),
		TypeVersion: 2, Narrow: &itemservice.Narrow{Readers: readers(w.eve)}, Fields: map[string]any{"f_amount": 1}})
	hidden := mustCreate(t, "eve's private purchase", res, err)
	_, err = w.migrate(t, w.adm, hidden, w.id("pur"), 2)
	assertNotFound(t, "adm migrates a purchase he cannot see", err)

	// Someone who may not is told so before any check: a dry run too, and
	// for an Item that would not fit.
	p := w.inFlight(t)
	for _, validateOnly := range []bool{false, true} {
		_, err := w.k.Migrate(testCtx(t), itemservice.MigrateRequest{IdempotencyKey: newKey(), Principal: w.eve, ItemID: p,
			TypeID: w.id("pur"), TypeVersion: 1, ValidateOnly: validateOnly})
		if !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("eve, with a target that is not the latest (validate only: %v): %v, want ErrForbidden", validateOnly, err)
		}
	}
}

// Absent key: nobody. And it is the latest version's key that decides, not
// the key of the version the Item is on.
func TestMigrate_WhoMay_TheLatestVersionsKey_AbsentIsNobody(t *testing.T) {
	w := newMigWorld(t)
	old := in(person("old"), w.ns)
	grantRoleIn(t, w.ns, old, "old_admin")
	w.reg(t, migPurchaseSrc(1, "migrate:\n  allowed: ['role:old_admin', 'role:mig_admin']\n"))
	p := w.inFlight(t)
	w.reg(t, migPurchaseSrc(2, "")) // no key
	for name, who := range map[string]item.Principal{"adm": w.adm, "old (named by @1's key)": old} {
		if _, err := w.migrate(t, who, p, w.id("pur"), 2); !errors.Is(err, itemservice.ErrForbidden) {
			t.Errorf("%s migrates to purchase@2, which has no migrate key: %v, want ErrForbidden", name, err)
		}
	}
	w.reg(t, migPurchaseSrc(3, migAllowed))
	if _, err := w.migrate(t, old, p, w.id("pur"), 3); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("old, named by @1's key only, migrates to purchase@3: %v, want ErrForbidden", err)
	}
	if _, err := w.migrate(t, w.adm, p, w.id("pur"), 3); err != nil {
		t.Errorf("adm, named by @3's key, migrates to purchase@3: %v", err)
	}
	if v := versionOf(t, p); v != 3 {
		t.Errorf("the purchase is on version %d, want 3", v)
	}
}

// --- the target ---

func TestMigrate_TheTargetIsTheLatestVersion(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, migPurchaseSrc(1, ""))
	p := w.inFlight(t)
	w.reg(t, migPurchaseSrc(2, migAllowed))

	// A version registered since the caller looked: by another process.
	other := itemservice.New(pool)
	registerInline(t, other, strings.ReplaceAll(migPurchaseSrc(3, migAllowed), "{s}", w.s))
	_, err := w.migrate(t, w.adm, p, w.id("pur"), 2)
	assertNotMigratable(t, "to purchase@2, once @3 is registered elsewhere", err, lifecycle.TargetNotLatest)
	if got := w.dryRun(t, p, w.id("pur"), 2); !slices.Equal(got, []lifecycle.MigrationCheck{lifecycle.TargetNotLatest}) {
		t.Errorf("the dry run to purchase@2: %v, want target_not_latest alone", got)
	}
	// The Item's own version, an older one, one that is not registered.
	for _, v := range []int{1, 4} {
		_, err := w.migrate(t, w.adm, p, w.id("pur"), v)
		assertNotMigratable(t, "to purchase@"+string(rune('0'+v)), err, lifecycle.TargetNotLatest)
	}
	if v := versionOf(t, p); v != 1 {
		t.Fatalf("the purchase is on version %d after four refusals, want 1", v)
	}
	// Another Type is not a target at all.
	if _, err := w.migrate(t, w.adm, p, w.id("other"), 3); !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("to another Type: %v, want ErrInvalid", err)
	}
	if _, err := w.migrate(t, w.adm, p, w.id("pur"), 3); err != nil {
		t.Errorf("to purchase@3, the latest: %v", err)
	}
	// Already on the latest: nothing to migrate to.
	_, err = w.migrate(t, w.adm, p, w.id("pur"), 3)
	assertNotMigratable(t, "an Item already on the latest version", err, lifecycle.TargetNotLatest)
}

// --- the dry run, and every problem ---

func TestMigrate_ValidateOnly_ListsEveryProblem_AndWritesNothing(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, migPurchaseSrc(1, ""))
	p := w.inFlight(t)
	// purchase@2 here drops f_note (which the purchase holds), adds an
	// invariant it breaks, and no longer declares draft -> review.
	v2 := strings.Replace(migPurchaseSrc(2, migAllowed+"invariants:\n  - {id: small, rule: 'f_amount == null || f_amount < 100'}\n"),
		"  - {id: f_note, type: string}\n", "", 1)
	v2 = strings.Replace(v2, "customer_fields: [f_note]\n", "", 1)
	v2 = strings.Replace(v2, "  - {from: draft, to: review}\n", "  - {from: draft, to: cfo_review}\n", 1)
	w.reg(t, v2)
	seq := itemSeqOf(t, p)
	events := count(t, `select count(*) from item_events where item_id = $1`, p)
	keys := count(t, `select count(*) from idempotency where actor = $1`, w.adm.Actor)

	res, err := w.k.Migrate(testCtx(t), itemservice.MigrateRequest{Principal: w.adm, ItemID: p, TypeID: w.id("pur"), TypeVersion: 2, ValidateOnly: true})
	if err != nil {
		t.Fatalf("the dry run: %v (a dry run that finds problems is an answer, not an error)", err)
	}
	want := []lifecycle.MigrationCheck{lifecycle.FieldMissing, lifecycle.MigrateInvariant, lifecycle.PathNotDeclared}
	if got := checks(res.Problems); !slices.Equal(got, want) {
		t.Fatalf("the dry run's problems = %v, want %v", got, want)
	}
	if m := res.Problems[0]; !slices.Equal(m.Fields, []string{"f_note"}) {
		t.Errorf("field_missing names %v, want [f_note]", m.Fields)
	}
	if m := res.Problems[1]; m.Invariant != "small" {
		t.Errorf("invariant_failed names %q, want small", m.Invariant)
	}
	if m := res.Problems[2]; m.State != "draft" || m.ToState != "review" {
		t.Errorf("path_not_declared names %q -> %q, want draft -> review", m.State, m.ToState)
	}
	if res.ItemID != p || res.CommitSeq != 0 {
		t.Errorf("the dry run answered Item %s at commit_seq %d; want the Item and no commit_seq", res.ItemID, res.CommitSeq)
	}

	// The same call for real is refused with the same problems.
	key := newKey()
	_, err = w.k.Migrate(testCtx(t), itemservice.MigrateRequest{IdempotencyKey: key, Principal: w.adm, ItemID: p, TypeID: w.id("pur"), TypeVersion: 2})
	assertNotMigratable(t, "the migrate itself", err, want...)

	// Neither wrote anything, and the refused key is free.
	if got := itemSeqOf(t, p); got != seq {
		t.Errorf("item_seq went from %d to %d", seq, got)
	}
	if got := count(t, `select count(*) from item_events where item_id = $1`, p); got != events {
		t.Errorf("events went from %d to %d", events, got)
	}
	if got := count(t, `select count(*) from idempotency where actor = $1`, w.adm.Actor); got != keys {
		t.Errorf("adm's idempotency rows went from %d to %d", keys, got)
	}
	if v := versionOf(t, p); v != 1 {
		t.Errorf("the purchase is on version %d, want 1", v)
	}
}

// A dry run on an Item that fits says so, writes nothing, and needs no key.
func TestMigrate_ValidateOnly_AnItemThatFits(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, migPurchaseSrc(1, ""))
	p := w.inFlight(t)
	w.reg(t, migPurchaseSrc(2, migAllowed))
	seq := itemSeqOf(t, p)
	if got := w.dryRun(t, p, w.id("pur"), 2); len(got) != 0 {
		t.Errorf("the dry run: %v, want no problem", got)
	}
	if v, s := versionOf(t, p), itemSeqOf(t, p); v != 1 || s != seq {
		t.Errorf("after the dry run: version %d at item_seq %d, want version 1 at %d", v, s, seq)
	}
	// A real migrate without a key is malformed.
	_, err := w.k.Migrate(testCtx(t), itemservice.MigrateRequest{Principal: w.adm, ItemID: p, TypeID: w.id("pur"), TypeVersion: 2})
	if !errors.Is(err, itemservice.ErrInvalid) {
		t.Errorf("a migrate without an idempotency key: %v, want ErrInvalid", err)
	}
}

// --- the write ---

// One transaction: the Item row, one event, the outbox row when either
// version has listeners. Nothing a caller could overwrite changes.
func TestMigrate_TheWrite(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, migPurchaseSrc(1, ""))
	p := w.inFlight(t)
	w.reg(t, migPurchaseSrc(2, migAllowed))
	before, err := w.k.Get(testCtx(t), w.eve, p)
	if err != nil {
		t.Fatal(err)
	}

	res, err := w.migrate(t, w.adm, p, w.id("pur"), 2)
	if err != nil {
		t.Fatalf("the migrate: %v", err)
	}
	var (
		op, actor string
		payload   []byte
		commit    int64
	)
	if err := pool.QueryRow(testCtx(t), `select op, actor, payload, commit_seq from item_events where item_id = $1 and item_seq = $2`,
		p, res.ItemSeq).Scan(&op, &actor, &payload, &commit); err != nil {
		t.Fatalf("read the migrate's event: %v", err)
	}
	if op != lifecycle.OpMigrated || actor != w.adm.Actor || commit != res.CommitSeq {
		t.Errorf("the event: op %q by %q at commit_seq %d; want migrated by adm at %d", op, actor, commit, res.CommitSeq)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	// JSON numbers decode as float64 in Go.
	if len(got) != 2 || got["from_version"] != float64(1) || got["to_version"] != float64(2) {
		t.Errorf("the event's payload = %s, want exactly {from_version: 1, to_version: 2}", payload)
	}
	if n := count(t, `select count(*) from item_events where item_id = $1 and item_seq > $2`, p, before.ItemSeq); n != 1 {
		t.Errorf("the migrate wrote %d events, want 1", n)
	}
	// Neither version has listeners: no outbox row.
	if n := count(t, `select count(*) from outbox where item_id = $1`, p); n != 0 {
		t.Errorf("outbox rows = %d, want 0: neither version has anything listening", n)
	}

	after, err := w.k.Get(testCtx(t), w.eve, p)
	if err != nil {
		t.Fatal(err)
	}
	if after.TypeVersion != 2 || after.ItemSeq != before.ItemSeq+1 {
		t.Errorf("the Item: version %d at item_seq %d; want 2 at %d", after.TypeVersion, after.ItemSeq, before.ItemSeq+1)
	}
	// The etag is content_seq: nothing a caller could overwrite changed.
	if after.ContentSeq != before.ContentSeq {
		t.Errorf("content_seq went from %d to %d: a migrate changes nothing a caller could overwrite", before.ContentSeq, after.ContentSeq)
	}
	if after.State != before.State || !jsonEqual(after.Fields, before.Fields) || *after.ScopeID != *before.ScopeID {
		t.Errorf("the Item changed beyond its version: state %q -> %q, fields %v -> %v", before.State, after.State, before.Fields, after.Fields)
	}
	// History shows it.
	hist, err := w.k.ListEvents(testCtx(t), itemservice.ListEventsRequest{Principal: w.eve, ItemID: p})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	last := hist.Events[len(hist.Events)-1]
	if last.Op != lifecycle.OpMigrated || last.TypeVersion != 2 || hist.TypeVersion != 2 {
		t.Errorf("the last event: op %q, version %d (the history's: %d); want migrated, 2", last.Op, last.TypeVersion, hist.TypeVersion)
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

// An outbox row is written when the new version has listeners, though the
// old one had none.
func TestMigrate_TheWrite_AnOutboxRowWhenEitherVersionListens(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, migPurchaseSrc(1, ""))
	p := w.inFlight(t)
	// purchase@2 with a timer on cfo_review: it has listeners. The
	// purchase is in review, which has none.
	v2 := strings.Replace(migPurchaseSrc(2, migAllowed), "  - {id: cfo_review, category: waiting}",
		"  - {id: cfo_review, category: waiting, timer: {after: 48h, to: approved}}", 1)
	v2 = strings.Replace(v2, "allowed: ['role:cfo'], except: [creator]}", "allowed: ['role:cfo', 'principal:system:timer']}", 1)
	w.reg(t, v2)
	res, err := w.migrate(t, w.adm, p, w.id("pur"), 2)
	if err != nil {
		t.Fatalf("the migrate: %v", err)
	}
	if n := count(t, `select count(*) from outbox where item_id = $1 and item_seq = $2`, p, res.ItemSeq); n != 1 {
		t.Errorf("outbox rows for the migrate's event = %d, want 1: purchase@2 has a timer", n)
	}
}

func TestMigrate_Idempotency(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, migPurchaseSrc(1, ""))
	p, q := w.inFlight(t), w.inFlight(t)
	w.reg(t, migPurchaseSrc(2, migAllowed))
	req := itemservice.MigrateRequest{IdempotencyKey: newKey(), Principal: w.adm, ItemID: p, TypeID: w.id("pur"), TypeVersion: 2}
	first, err := w.k.Migrate(testCtx(t), req)
	if err != nil {
		t.Fatalf("the migrate: %v", err)
	}
	// A retry answers as the first did, though the Item is now on the
	// latest version and a new migrate of it would be refused.
	again, err := w.k.Migrate(testCtx(t), req)
	if err != nil || again.ItemSeq != first.ItemSeq || again.CommitSeq != first.CommitSeq {
		t.Errorf("the retry answered %+v, %v; want the first answer %+v", again.WriteResult, err, first.WriteResult)
	}
	if n := count(t, `select count(*) from item_events where item_id = $1 and op = 'migrated'`, p); n != 1 {
		t.Errorf("migrated events = %d, want 1", n)
	}
	// The same key for another Item is a reuse.
	req.ItemID = q
	if _, err := w.k.Migrate(testCtx(t), req); !errors.Is(err, itemservice.ErrIdempotencyKeyReuse) {
		t.Errorf("the same key for another Item: %v, want ErrIdempotencyKeyReuse", err)
	}
	if v := versionOf(t, q); v != 1 {
		t.Errorf("the other purchase is on version %d, want 1", v)
	}
}

// --- quiet ---

// migHooked is a request with a check on its submission, an order placed
// on entering ordered, and a timer on waiting. with says which of these a
// version declares, so that each can be absent from one version.
func migHooked(version int, check, order, timer bool, rest string) string {
	src := "\nid: hk_{s}\nversion: " + string(rune('0'+version)) + `
initial_state: draft
fields:
  - {id: f_vendor, type: string}
  - {id: f_amount, type: int}
`
	if check {
		src += "  - {id: f_manager, type: principal, writable_by: kernel}\n"
	}
	if order {
		src += "  - {id: f_order_id, type: string, writable_by: kernel}\n"
	}
	src += `states:
  - {id: draft, category: todo}
  - {id: review, category: active}
  - {id: ordered, category: active}
`
	if timer {
		src += "  - {id: waiting, category: waiting, timer: {after: 48h, to: review}}\n"
	} else {
		src += "  - {id: waiting, category: waiting}\n"
	}
	src += `  - {id: done, category: done}
transitions:
  - {from: draft, to: review}
  - {from: review, to: ordered}
  - {from: review, to: waiting}
  - {from: waiting, to: review}
  - {from: ordered, to: done}
`
	if check || order {
		src += "hooks:\n"
	}
	if check {
		src += "  - {id: manager_check, on: transition, from: draft, to: review, kind: check, action: hr.manager_of, " +
			"reads: [created_on_behalf_of, created_by], writes: [f_manager]}\n"
	}
	if order {
		src += "  - {id: place_order, on: enter, state: ordered, kind: action, action: erp.create_invoice, " +
			"reads: [f_vendor, f_amount], writes: [f_order_id]}\n"
	}
	return src + rest
}

func (w migWorld) hooked(t *testing.T, version int, path ...string) uuid.UUID {
	t.Helper()
	res, err := w.create(t, w.eve, w.id("hk"), version, nil, map[string]any{"f_vendor": "cloudhost", "f_amount": 10})
	id := mustCreate(t, "a request", res, err)
	for _, to := range path {
		if err := w.move(t, w.eve, id, to); err != nil {
			t.Fatalf("eve moves the request to %s: %v", to, err)
		}
	}
	return id
}

// Each thing that makes an Item not quiet, under the version it is on and
// under the version it would move to. Nothing runs the workflow here, so a
// hold stays open and an action stays unfinished.
func TestMigrate_Quiet(t *testing.T) {
	cases := []struct {
		name string
		// what version 1 and version 2 declare: a check, an order, a timer
		v1, v2 [3]bool
		path   []string
		want   lifecycle.MigrationCheck
	}{
		{"an open hold", [3]bool{true, false, false}, [3]bool{true, false, false}, []string{"review"}, lifecycle.MigrateHeld},
		{"an action unfinished", [3]bool{false, true, false}, [3]bool{false, true, false}, []string{"review", "ordered"}, lifecycle.ActionUnfinished},
		{"an action only the new version declares, on the state the Item is in",
			[3]bool{false, false, false}, [3]bool{false, true, false}, []string{"review", "ordered"}, lifecycle.ActionUnfinished},
		{"a timer on its state", [3]bool{false, false, true}, [3]bool{false, false, true}, []string{"review", "waiting"}, lifecycle.HasTimer},
		{"a timer only the new version declares", [3]bool{false, false, false}, [3]bool{false, false, true}, []string{"review", "waiting"}, lifecycle.HasTimer},
		{"a timer only the old version declares", [3]bool{false, false, true}, [3]bool{false, false, false}, []string{"review", "waiting"}, lifecycle.HasTimer},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newMigWorld(t)
			w.reg(t, migHooked(1, c.v1[0], c.v1[1], c.v1[2], ""))
			id := w.hooked(t, 1, c.path...)
			w.reg(t, migHooked(2, c.v2[0], c.v2[1], c.v2[2], migAllowed))
			deliverAll(t, id) // so that undelivered events are not what is found
			if got := w.dryRun(t, id, w.id("hk"), 2); !slices.Equal(got, []lifecycle.MigrationCheck{c.want}) {
				t.Errorf("the dry run: %v, want %s alone", got, c.want)
			}
			_, err := w.migrate(t, w.adm, id, w.id("hk"), 2)
			assertNotMigratable(t, "the migrate", err, c.want)
			if v := versionOf(t, id); v != 1 {
				t.Errorf("the request is on version %d, want 1", v)
			}
		})
	}
}

// An action the new version adds on a state the Item entered and has
// since left would run for that old entry: such an Item is refused too.
// One the Item never entered holds nothing back.
func TestMigrate_Quiet_AnActionTheNewVersionAddsOnAStateAlreadyLeft(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, migHooked(1, false, false, false, ""))
	left := w.hooked(t, 1, "review", "ordered", "done")
	never := w.hooked(t, 1, "review")
	w.reg(t, migHooked(2, false, true, false, migAllowed))
	// The one that is done is closed, and that is said first.
	if got := w.dryRun(t, left, w.id("hk"), 2); !slices.Equal(got, []lifecycle.MigrationCheck{lifecycle.MigrateClosed, lifecycle.ActionUnfinished}) {
		t.Errorf("an Item that entered ordered under version 1 and is done: %v, want closed, action_unfinished", got)
	}
	if got := w.dryRun(t, never, w.id("hk"), 2); len(got) != 0 {
		t.Errorf("an Item that never entered ordered: %v, want no problem", got)
	}
	if _, err := w.migrate(t, w.adm, never, w.id("hk"), 2); err != nil {
		t.Fatalf("the migrate: %v", err)
	}
	// Migrated in review, it now enters ordered under version 2: the order
	// is pending for that entry, and for no earlier one.
	if err := w.move(t, w.eve, never, "ordered"); err != nil {
		t.Fatalf("eve moves the migrated request to ordered: %v", err)
	}
	se, err := w.k.StateEntry(testCtx(t), never)
	if err != nil {
		t.Fatalf("StateEntry: %v", err)
	}
	if len(se.PendingActions) != 1 || se.PendingActions[0].Hook != "place_order" || se.PendingActions[0].CauseSeq != se.EnteredSeq {
		t.Errorf("pending actions = %+v, want place_order for the entry into ordered (event %d)", se.PendingActions, se.EnteredSeq)
	}
}

// A create reaction not yet run, under either version.
func TestMigrate_Quiet_AReactionUnfinished(t *testing.T) {
	bench := func(version int, reacts bool, rest string) string {
		src := "\nid: bench_{s}\nversion: " + string(rune('0'+version)) + `
initial_state: open
states:
  - {id: open, category: todo}
  - {id: working, category: active}
transitions:
  - {from: open, to: working}
`
		if reacts {
			src += "reactions:\n  - {id: make, kind: create, on: enter, state: working, type: auto_{s}, version: 1, fields: {f_title: '\"made\"'}}\n"
		}
		return src + rest
	}
	const auto = "\nid: auto_{s}\nversion: 1\nfields:\n  - {id: f_title, type: string}\naccepts_reactions:\n  bench_{s}:make: {creates: [f_title]}\n"
	for name, c := range map[string][2]bool{"declared by both versions": {true, true}, "declared by the new version only": {false, true}} {
		t.Run(name, func(t *testing.T) {
			w := newMigWorld(t)
			w.reg(t, auto)
			w.reg(t, bench(1, c[0], ""))
			b := w.container(t, w.lea, w.id("bench"), nil)
			if err := w.move(t, w.lea, b, "working"); err != nil {
				t.Fatalf("lea starts the bench: %v", err)
			}
			w.reg(t, bench(2, c[1], migAllowed))
			deliverAll(t, b)
			if got := w.dryRun(t, b, w.id("bench"), 2); !slices.Equal(got, []lifecycle.MigrationCheck{lifecycle.ReactionUnfinished}) {
				t.Errorf("the dry run: %v, want reaction_unfinished alone", got)
			}
		})
	}
}

// Events still in the outbox: the relay judges an event by the Item's
// version when it delivers it, so none may be waiting.
func TestMigrate_Quiet_EventsUndelivered(t *testing.T) {
	w := newMigWorld(t)
	// A timer on waiting gives the Type listeners; the request is in
	// review, which has no timer.
	w.reg(t, migHooked(1, false, false, true, ""))
	id := w.hooked(t, 1, "review")
	w.reg(t, migHooked(2, false, false, true, migAllowed))
	if n := count(t, `select count(*) from outbox where item_id = $1 and delivered_at is null`, id); n == 0 {
		t.Fatal("test bug: the request has no undelivered event")
	}
	if got := w.dryRun(t, id, w.id("hk"), 2); !slices.Equal(got, []lifecycle.MigrationCheck{lifecycle.EventsUndelivered}) {
		t.Errorf("the dry run: %v, want events_undelivered alone", got)
	}
	_, err := w.migrate(t, w.adm, id, w.id("hk"), 2)
	assertNotMigratable(t, "the migrate", err, lifecycle.EventsUndelivered)
	deliverAll(t, id)
	if _, err := w.migrate(t, w.adm, id, w.id("hk"), 2); err != nil {
		t.Errorf("the migrate, once the events are delivered: %v", err)
	}
}

// --- the facts the fit check is given come from the Item's own rows ---

func TestMigrate_TheFitCheckIsGivenTheItemsFacts(t *testing.T) {
	w := newMigWorld(t)
	w.reg(t, stRoomSrc)
	w.reg(t, stHallSrc)
	const head = "\nid: card_{s}\nversion: %V\ninitial_state: open\nfields:\n  - {id: f_owner, type: principal}\n" +
		"states:\n  - {id: open, category: todo}\n  - {id: doing, category: active}\n  - {id: done, category: done}\n" +
		"transitions:\n  - {from: open, to: doing}\n  - {from: doing, to: done}\n"
	w.reg(t, strings.Replace(head, "%V", "1", 1)+"links:\n  - {name: blocks, inverse: blocked_by, targets: [card_{s}]}\n")
	hall := w.container(t, w.lea, w.id("hall"), nil)
	mk := func() uuid.UUID {
		res, err := w.create(t, w.eve, w.id("card"), 1, &hall, map[string]any{"f_owner": w.lea.Actor})
		return mustCreate(t, "a card", res, err)
	}
	a, b := mk(), mk() // two cards in the hall with the same owner
	if _, err := w.k.Link(testCtx(t), itemservice.LinkRequest{IdempotencyKey: newKey(), Principal: w.eve, SourceID: a, LinkType: "blocks", TargetID: b}); err != nil {
		t.Fatalf("a blocks b: %v", err)
	}
	if err := w.move(t, w.eve, a, "doing"); err != nil {
		t.Fatalf("a to doing: %v", err)
	}
	// card@2: created only in a room, the owner unique among siblings, no
	// blocks link, and a state between open and doing (no open -> doing).
	v2 := "\nid: card_{s}\nversion: 2\ninitial_state: open\nfields:\n  - {id: f_owner, type: principal, unique_among_siblings: [open, ready, doing]}\n" +
		"states:\n  - {id: open, category: todo}\n  - {id: ready, category: todo}\n  - {id: doing, category: active}\n  - {id: done, category: done}\n" +
		"transitions:\n  - {from: open, to: ready}\n  - {from: ready, to: doing}\n  - {from: doing, to: done}\n" +
		"start:\n  in: [room_{s}]\n" + migAllowed
	w.reg(t, v2)

	res, err := w.k.Migrate(testCtx(t), itemservice.MigrateRequest{Principal: w.adm, ItemID: a, TypeID: w.id("card"), TypeVersion: 2, ValidateOnly: true})
	if err != nil {
		t.Fatalf("the dry run: %v", err)
	}
	want := []lifecycle.MigrationCheck{lifecycle.MigrateNotUnique, lifecycle.LinkChanged, lifecycle.ContainerNotAdmited, lifecycle.PathNotDeclared}
	if got := checks(res.Problems); !slices.Equal(got, want) {
		t.Fatalf("the dry run's problems = %v, want %v", got, want)
	}
	if m := res.Problems[0]; !slices.Equal(m.Fields, []string{"f_owner"}) {
		t.Errorf("not_unique names %v, want [f_owner]", m.Fields)
	}
	if m := res.Problems[1]; m.LinkType != "blocks" {
		t.Errorf("link_changed names %q, want blocks", m.LinkType)
	}
	if m := res.Problems[3]; m.State != "open" || m.ToState != "doing" {
		t.Errorf("path_not_declared names %q -> %q, want open -> doing", m.State, m.ToState)
	}
	// No problem says anything of the other card: not its ID, not its value.
	for _, m := range res.Problems {
		if s, _ := json.Marshal(m); strings.Contains(string(s), b.String()) {
			t.Errorf("a problem names the other Item: %s", s)
		}
	}
}

// --- readers ---

// Readers see the new version at once: its number, and what it lets a
// customer see.
func TestMigrate_ReadersSeeTheNewVersion(t *testing.T) {
	w := newMigWorld(t)
	room := func(version string) string { return strings.Replace(stRoomSrc, "version: 1", "version: "+version, 1) }
	w.reg(t, strings.Replace(room("1"), "own: [ask_{s}]", "own: [pur_{s}]", 1))
	w.reg(t, migPurchaseSrc(1, "")) // customer_fields: [f_note]
	r := w.container(t, w.lea, w.id("room"), nil)
	res, err := w.create(t, w.kim, w.id("pur"), 1, &r, map[string]any{"f_note": "for kim"})
	p := mustCreate(t, "kim's purchase in the room", res, err)
	seen, err := w.k.Get(testCtx(t), w.kim, p)
	if err != nil || seen.Fields["f_note"] != "for kim" {
		t.Fatalf("kim reads her purchase@1: %v, %v; want f_note", seen.Fields, err)
	}
	// purchase@2 shows a customer nothing.
	w.reg(t, strings.Replace(migPurchaseSrc(2, migAllowed), "customer_fields: [f_note]\n", "customer_fields: []\n", 1))
	if _, err := w.migrate(t, w.adm, p, w.id("pur"), 2); err != nil {
		t.Fatalf("the migrate: %v", err)
	}
	seen, err = w.k.Get(testCtx(t), w.kim, p)
	if err != nil {
		t.Fatalf("kim reads her purchase after the migrate: %v", err)
	}
	if seen.TypeVersion != 2 {
		t.Errorf("kim sees version %d, want 2", seen.TypeVersion)
	}
	if _, has := seen.Fields["f_note"]; has {
		t.Errorf("kim still sees f_note (%v): purchase@2 shows a customer no field", seen.Fields)
	}
	staff, err := w.k.Get(testCtx(t), w.lea, p)
	if err != nil || staff.TypeVersion != 2 || staff.Fields["f_note"] != "for kim" {
		t.Errorf("lea reads it: version %d, fields %v, %v; want version 2 with f_note", staff.TypeVersion, staff.Fields, err)
	}
}

// A claimed Item may be migrated by whoever may migrate, and keeps its
// claimant: fay still decides it, gus still does not.
func TestMigrate_AClaimedItemKeepsItsClaimant(t *testing.T) {
	w := newMigWorld(t)
	fay, gus := w.bob, w.carl
	grantRoleIn(t, w.ns, fay, "finance")
	grantRoleIn(t, w.ns, gus, "finance")
	claimed := func(version int, rest string) string {
		src := strings.Replace(migPurchaseSrc(version, rest), "  - {id: review, category: active}", "  - {id: review, category: active, claim: f_claimed_by}", 1)
		src = strings.Replace(src, "{id: f_claimed_by, type: principal}", "{id: f_claimed_by, type: principal, writable_by: ['role:finance']}", 1)
		src = strings.Replace(src, "  - {from: draft, to: review}\n", "  - {from: draft, to: review, allowed: ['role:finance']}\n"+
			"  - {from: review, to: draft, allowed: ['role:finance'], release: true, clears: [f_claimed_by]}\n", 1)
		return src
	}
	w.reg(t, claimed(1, ""))
	res, err := w.create(t, w.eve, w.id("pur"), 1, nil, map[string]any{"f_amount": 6000})
	p := mustCreate(t, "a purchase", res, err)
	if err := w.move(t, fay, p, "review"); err != nil {
		t.Fatalf("fay claims it: %v", err)
	}
	w.reg(t, claimed(2, migAllowed))
	if _, err := w.migrate(t, w.adm, p, w.id("pur"), 2); err != nil {
		t.Fatalf("adm migrates the claimed purchase: %v", err)
	}
	got, err := w.k.Get(testCtx(t), fay, p)
	if err != nil || got.Fields["f_claimed_by"] != fay.Actor {
		t.Errorf("the claim after the migrate: %v, %v; want fay", got.Fields["f_claimed_by"], err)
	}
	if err := w.move(t, gus, p, "cfo_review"); !errors.Is(err, itemservice.ErrForbidden) {
		t.Errorf("gus, who did not claim it, sends it to the CFO: %v, want ErrForbidden", err)
	}
	if err := w.move(t, fay, p, "cfo_review"); err != nil {
		t.Errorf("fay, the claimant, sends it to the CFO: %v", err)
	}
}
