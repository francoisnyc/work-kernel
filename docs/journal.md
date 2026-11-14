# Spike journal

One entry per session, newest at the bottom.

## Later

Ideas noted during the spike, not built yet.

- `idempotency` rows never expire. Add a retention period and a cleanup job.
- The idempotency request hash is built from the Go request struct, so adding a
  field changes the hash of stored keys and an old retry is rejected as a reuse.
  Hash a stable, versioned form instead.
- Erasure deletes `item_event_values` rows but the current values also live in
  `items.fields`. A real erasure must clear both.
- An `Update` that sets a field to its current value still bumps `item_seq` and
  writes an event and an outbox row. Suppress no-op updates: it is one of the
  echo-suppression layers the connector design relies on. (PR #5 review)
- Cache parsed Types in the process, keyed by `(id, version)`; versions are
  immutable, so it is safe. Wait for Step 5's numbers. (PR #5 review)
- When guards need more than `is set` / `==` / `!=`, switch to `cel-go` rather
  than growing the spike's parser. (PR #5 review)
- A Type with no behavior at all (no state needs a workflow) should write no
  outbox rows in the first place, kernel side. Needed for Step 5's burst test;
  until then the relay marks such rows delivered. (Step 3 Phase A)
- When attribution is exposed in the API, only trusted internal principals may
  set `originating_hook`, `causation_id` and `hop`. A public caller may pass
  `correlation_id` (a trace) only, and only as a claim. Today attribution is a
  Go-only request field. (Step 3)
- The ticket's one timer (48h in `in_progress`, back to `open`) is a stand-in.
  The real first-response SLA comes later, with standalone SLA objects. (Step 3)
- The relay delivers one Item at a time. Deliver to several Items in parallel,
  with a bound, keeping each Item's own order. (PR #6 review)
- Every event costs one Temporal call, even when nothing listens, until the
  kernel-side outbox skip above exists (Step 5). (PR #6 review)
- The relay polls every ~200 ms. Use `LISTEN/NOTIFY` to wake it instead of
  waiting for the next poll. (Step 3 Phase A)
- The relay is a single instance. To run several, claim an Item's pending rows
  with `FOR UPDATE SKIP LOCKED`. (Step 3 Phase A)
- Grants on scope nodes (audited) are the admin path, including recovering an
  Item nobody can see. Until then a policy change may not hide an Item from the
  one making it. (Step 4)
- The timer's exemption from visibility trusts the request's `Actor`. When the
  API is exposed, only internal callers may act as `system:*`. (Step 4)
- `VisibleScopes` returns every scope a principal can read, and each narrowed
  Item has its own. Fine for a spike; at scale, check one scope's ancestors or
  join in SQL. (Step 4)
- Search indexes every string value in `fields`, enum values included, with the
  `simple` configuration (no stemming). Choose per-field indexing and a language
  later. Erasure must also cover `items.search`, which follows `items.fields`. (Step 4)
- Narrowing a container rewrites every descendant's `scope_id` in one
  transaction: O(descendants). Proposed fix: give every container its own scope
  node at creation (`narrows = false`, inheriting), so narrowing flips that
  node's flag and readers in place and no descendant row changes.
  `VisibleScopes` then has to treat non-narrowing nodes as inheriting their
  parent. (Step 4)
- `Get` reads the visible scopes and then the Item in two statements. One
  statement would close the tiny window between them. (Step 4)

## 2026-11-12 — Step 1 (finished)

**Built:** repo scaffolding (module, layout, Makefile, docker-compose, goose migration,
proto contract), `docs/research.md`, and `kernel.Create` / `kernel.Get` with idempotency
keys. Nine tests in `internal/kernel` pass, including the "done when" and an
eight-way concurrent same-key test (also clean under `-race`).

**Decisions:** server-generated UUIDv7 IDs; idempotency keys are global; a key reused
with a different request is rejected via `idempotency.request_hash` (added to the schema);
create events record `field_ids`, never values; first event is `item_seq` 1;
`policy.Project` is a pass-through called from `Get`; no `internal/store` yet.

**Surprises:** current libraries need Go 1.26 (the toolchain auto-downloads). Claiming
the key with `insert … on conflict do nothing` as the first statement handles
concurrent duplicates with no extra locking. `go.mod` lists connect-go only because
the generated code in `gen/` (git-ignored) imports it.

**Open questions and risks:**
- `commit_seq` is a `bigserial`: values are assigned at insert, not commit, so a lower
  one can become visible after a higher one. This breaks `min_seq` waits in Step 4.
- Namespace column, links table and archived marker are not in the schema yet.
- The Temporal image is `:latest`, not pinned. Update-With-Start GA is confirmed from
  the SDK source only; the official docs were unreachable.
- No test observes that `Get` goes through `policy.Project` (wait for Step 4).

**Next session:** Step 2, Phase A — Type from YAML, `Update`, `Transition`, the pure
transition function in `internal/lifecycle`.

## 2026-11-13 — Step 2 (finished)

**Built:** migration `00002` with the Step 1 review follow-ups (scoped idempotency keys,
`namespace_id`, attribution columns, `item_event_values`, `commit_counter`, two indexes);
`internal/typedef` (Type from YAML, guard parsing, field checks); `lifecycle.Transition`
(pure); `kernel.RegisterType`, `Update`, `Transition`; the `types/ticket.v1.yaml` fixture.
93 tests and subtests pass under `-race`, including both parts of the "done when".

**Decisions:** Types are stored in the `types` table and read on every write (no cache);
a version is immutable. `ErrInvalid` = malformed request or unknown Type; `ErrRejected` =
the Type refuses it. A rejection rolls back, so it does not consume the key. Lifecycle
validation runs inside the transaction, under the row lock (the design's diagram shows it
before `BEGIN`). `int` fields refuse values beyond ±(2^53−1) instead of rounding. Guards:
`f is set`, `f == lit`, `f != lit`, joined by `&&`. `allowed` is parsed, not enforced.

**Surprises:** one `write` helper now carries the idempotency protocol for all three
operations, so `Link` and `Archive` should be short. `fmt.Errorf` with two `%w` lets one
error match both `ErrRejected` and `*lifecycle.Rejection`. The cloud session needs
`dockerd` started by hand before testcontainers work.

**Open questions and risks:**
- `design.md` still shows validation before `BEGIN` and RPCs returning `WriteResult`
  directly (the proto wraps it in `CreateResponse` etc.). Update the doc?
- Every write reads and re-parses the Type from Postgres. Measure in Step 5.
- The commit counter serializes all writes (by design for the spike).
- `cmd/kernel` is still a placeholder; nothing loads `types/*.yaml` at startup yet.

**Next session:** Step 3, Phase A — outbox relay, lazy workflow with Update-With-Start,
one durable timer. Use the planning phase from `docs/step-prompts.md` (tricky step).
Fold two follow-ups from the PR #5 review into that Phase A:
- An optional `Fields` patch on `TransitionRequest`: apply it, then check the guards,
  under one lock, as one event (values in `item_event_values`, `from`/`to` in the payload).
- A Type without a lifecycle gets one implicit state (rule added by PR #4);
  `typedef` refuses a Type with no states today.

## 2026-11-13 — Step 3 (finished)

**Built:** two PR #5 follow-ups (`Fields` patch on `Transition`; implicit state `default`
in a new category `none`), then Step 3: `expected_item_seq` + `ErrStale`, attribution on
write requests, state timers, `lifecycle.TimerFor` / `NeedsWorkflow`, the Item workflow,
the relay with its Temporal adapter, `cmd/worker`, `cmd/relay`. Both "done when" cases
pass on a real dev server (worker restarted before, and after, the timer was due).

**Decisions:** every new run loads state from Postgres and ignores events at or below the
loaded `item_seq`; the workflow gets a timer table, never the Type; gaps are refused by
the update validator; the relay marks a row only when the update *completed*; the timer
counts from the entering event's `created_at`; timer key = `timer:<item>:<entered>:<expected>`.

**Surprises:** the pinned server answers `NotFound` for an Update to a completed workflow
as well as a missing one, so "nothing listening" is one check. A validator cannot wait, so
before the load it must let updates through and the handler judges them. `reflect.DeepEqual`
on `time.Time` fails for equal instants. Temporal's test environment skips time and made
14 of the workflow tests run in milliseconds.

**Numbers:** the end-to-end tests take ~28 s (real 2 s timers); the rest of the suite ~10 s.

**Open questions and risks:**
- `design.md` still says the relay marks on "accepted", that a new run after a *closed*
  workflow reloads (now every run does), and lists no `expected_item_seq`. Update it?
- Event shapes sent to workflows hold no field values but do not pass through
  `policy.Project`; the rule says "no exceptions". Decide in Step 4.
- `LoadItem` and unexpected kernel errors retry forever, per the retry policy; a deleted
  Item would retry without end. Nothing registers `types/*.yaml` at startup yet.

**After the PR #6 review:** `design.md` now matches what was built and states the policy
boundary (event shapes to our own Temporal are not a principal). A missing Item is final
for both activities: the load completes the workflow, the timer's write is not retried.

**Next session:** Step 4, Phase A — scopes, `VisibleScopes`, full-text search with `min_seq`.

### Go checkpoint (design.md asks for this after Step 3)

- **Language vs design:** roughly 10% language, 90% design. These are estimates from the
  session, not measurements. Nearly all the back-and-forth was about Temporal semantics
  and the write path (stale vs rejected, where gaps are refused, what a run may know).
- **What was hard in Go:** little. `DeepEqual` on times cost one test round; two packages
  both named `workflow` need an import alias; a method cannot be passed to the test
  environment as a mock without `testify`, so activities are registered by name. Errors
  as values fit this design well: `ErrStale` vs `ErrRejected` is one `errors.Is` each.
- **Recommendation:** continue in Go. The Temporal Go SDK covered everything needed
  (Update-With-Start, validators, a time-skipping test environment), and nothing in
  Steps 1–3 was fought for because of the language. Caveat: this judges the code I wrote;
  how readable it is to you, coming from Java, is the half only you can assess.

## 2026-11-14 — Step 4 (finished)

**Built:** the rename to `itemservice` (PR #8). Then Step 4: migration 00003 (root scope,
`scope_id` NOT NULL, `principal_roles`, generated `search` column, indexes);
`policy.VisibleScopes`, `Roles`, pure `Project` and `AuthorizeTransition`; `Create.Narrow`
and the `Update` policy patch; `ErrNotFound` for hidden Items on reads and writes;
`ErrForbidden` for `allowed`; the on-behalf-of intersection; `Search` with `min_seq` and
`ErrNotCaughtUp`; the typedef load checks for `allowed` and timers.

**Decisions:** `ticket.v1` dropped `allowed`, a test Type carries it; lifecycle is asked
before policy; a policy change may not hide the Item from its author; the timer skips
visibility but not `allowed`; narrowing a container re-points the children that inherit.

**Surprises:** `scopes` needed a `namespace_id` to have one root per namespace. Copying the
container's `scope_id` onto a child means narrowing the container later must move the
children; that was not in the plan and has its own test. `jsonb_to_tsvector` made the
search column one line, but it indexes enum values too.

**Numbers:** 43 new tests. `itemservice` tests ~9 s, of which 2 s is the default-wait test.

**Open questions and risks:**
- `TestGet_ReturnsCreatedItem` (Step 1) expected `ScopeID` nil. With approval it now
  asserts the scope is set; the root-scope test checks which one.
- PR #8 carries the rename and Step 4 together. PR #7 (the "Names" section) was still
  open; once it merges, its last sentence about `cmd/kernel` is out of date.

**Next session:** Step 5, Phase A.
