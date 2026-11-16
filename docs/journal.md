# Spike journal

One entry per session, newest at the bottom.

## Later

Ideas noted during the spike, not built yet.

- Rename the repo and Go module to Pli once the name checks pass (domains,
  GitHub organization, trademark). The CLI is already `pli`. (2026-11-15)
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
- A kernel write must name a cause on the same Item. Link reactions will
  write to another Item, so that rule relaxes when they arrive. (Step 6)
- `lifecycle.HasListeners` decides whether an event gets an outbox row. Today it
  only knows timers. Hooks, link reactions and subscriptions must widen it when
  they arrive, or their events are silently never sent. (Step 5)
- "Evolve the schema": old Items should read new optional fields added in a later
  version. Today everything is pinned to the Item's version. (Step 5)
- "Float the policy": move `allowed` out of transitions into a policy section
  keyed by transition name, so it can follow the newest version while the
  lifecycle stays pinned. (Step 5)
- Migrating an Item to a new Type version: a state map (old state → new state),
  a dry run that lists what would move and what has no mapping, a `migrated`
  event recording both versions, and a running workflow that reloads its timer
  table (the entering event stays the same, so a timer keeps its start). (Step 5)
- Rejected gaps are billed: a validator rejection is an action even though it
  never reaches history, so every relay retry of a gap costs money. Add a
  counter metric for gap rejections. (Step 5)
- Unlimited activity retries are billed per attempt: a stuck Item costs about
  1,440 actions a day at the 1-minute backoff cap. Cap the attempts, or alert
  when an Item's activity has retried for more than an hour. (Step 5)
- `VisibleScopes` walks every child scope of the scopes a principal can read,
  and runs on every read and write. With 1,000 narrowed scopes in the namespace,
  even a principal who sees one scope pays for the walk. Fix: check one Item's
  scope ancestors upward instead, or keep a principal → scopes table. (Step 5)
- A post-commit sequencer: one background process that reads events whose
  transaction ID is below the snapshot xmin, orders them, and stamps a gap-free
  log position. Watch and external indexes need "nothing before this point is
  missing", which `commit_seq` cannot give once it comes from a plain sequence.
  (Step A)
- A metric for the age of the xmin watermark: a long transaction holds it back
  for the whole database and every `min_seq` wait times out. (Step A)
- Groups as readers: `scope_access` would hold group IDs and the check becomes
  "the principal or one of their groups", keeping the table O(scopes × reader
  entries) instead of O(scopes × people). Nothing to build yet. (Step A)
- Search cost after Step A: match-first is O(matches). A word in ~10,000
  Items costs ~14 ms, where the old flattened shape took ~1 ms (and ~11 ms for
  a rare word). Options: cap the match step, pick the shape from the estimated
  match count, or move search to its own index (which `min_seq` already
  allows). (Step A)
- After Step A the write ceiling on the 4-vCPU bench box is neither a lock nor
  the WAL flush (`synchronous_commit=off` changed nothing): most likely CPU
  shared by Go, Postgres and the containers. Measure on separate machines
  before tuning anything. (Step A)
- `Project` must redact container and scope IDs once grants exist. Today a
  narrow-only tree means anyone who sees an Item sees its container; the move
  property test checks that `Get` never names a container its reader cannot see.
  (Step A2)
- Approval unlocks a whole scope for the rest of the session. Narrower later:
  approve a specific draft write (ties into "every action others see has a
  draft step"). (Step A2)
- Search records every result's narrowing scope, so a broad search makes almost
  every later write need approval. Correct, but agents should use short,
  task-scoped sessions; revisit the UX with real agents. (Step A2)
- The namespace-wide move lock serializes all moves in a namespace. Narrow it
  (for example, a lock per affected tree) if moves get frequent. (Step A2)
- Taint for untrusted fields, approvals bound to a digest of what they cover,
  `ApproveWiden` bound to one held write, and a kill switch per Type version,
  hook or connection: settled direction in design.md ("Settled direction for
  holds, approvals and agents"). Build each when a step needs it. (Design
  review, 2026-11-14)
- A timer write refused as `Invalid` is only logged: it is a kernel bug, and
  the timer is then dropped for that state entry. Add an alert or a counter on
  Invalid timer writes. (Step 6)
- Type migration must re-arm timers for the new version: a running workflow
  carries its timer table in the Snapshot, so after a migration it must reload
  the table (and `TimerDone`) for the Item's new version. (Step 6)
- A timer that comes due while a hold is open is refused as "held" and spent:
  it does not fire again after the hold resolves. Revisit with real SLA
  objects, together with the open question: does the SLA clock pause during a
  hold? (Step 6)
- Once hooks are real, every hook must be declared, including on Types that
  declare none today. For the spike, a Type without a `hooks` section still
  accepts writes from any hook of its own Type (part 1's tests rely on it).
  (Step 6)
- A hook can run again for the same cause: after a reload, an enrich hook
  that wrote nothing is rerun, and a check retried after a timeout calls out
  again. Harmless for read-only actions. Any action with side effects must
  pass an idempotency key to the connector itself; Step 7's invoice test
  proves it. (Step 6)
- The bounded `wait` on `Transition` polls `item_holds` every ~50 ms. Use
  `LISTEN/NOTIFY` to wake waiters instead. (Step 6)
- The workflow has no `GetVersion` branches, so changing its code breaks the
  replay of running histories. Before the first product: replay tests against
  real recorded histories, and versioned changes. (Step 6)
- An action hook reads its inputs when it runs, not as they were at its
  cause: if `f_amount` changes between two runs of the invoice hook, the ERP
  sees the same key with another request and answers `key_conflict`. Fix: an
  action hook reads its inputs as of its cause event (`item_event_values`
  holds them), so retries always send the same request and the invoice bills
  the approved amount. Ties to "an approval covers exact inputs". For the
  spike the visible `key_conflict` is enough. (Step 7)
- An action's write-back can commit while its attempt is reported as timed
  out; if the budget then runs out, the Item gets both `f_invoice_id` and
  `hook_failed` (`budget_exhausted`). Rare and visible. Fix when it matters:
  `HookFailed` for `budget_exhausted` writes nothing when the hook already
  completed for that entry. (Step 7)
- Before giving up on an action, reconcile: look the key up in the connector
  (the ERP knows whether it made the invoice) and write back what it made,
  instead of `hook_failed`. (Step 7)
- A timer that comes due while a hold is open should be deferred until the
  hold resolves, then fire if the Item is still in its state, not be spent
  as it is today. The SLA clock keeps running during a hold by default; a
  real SLA object may declare `pause_while: held`. (Step 7 write-up)
- After a worker dies (SIGKILL), its action attempts are only retried once
  their attempt timeout (StartToClose) runs out: 21 s with a 20 s timeout in
  the bench, while a graceful stop reports the attempts and they retry at
  once. Give long actions a heartbeat timeout (a few seconds) so a dead
  worker is noticed quickly, without shortening the attempt itself. (Step 7)
- `StateEntry` finds pending actions by reading the Item's whole event
  history on every workflow load: O(events). Fine for the spike; keep a
  per-entry "actions done" marker if Items get long histories. (Step 7)
- Types per namespace. Types are global today: every namespace sees the same
  list under its own names. (Step 0, part 1)
- Make the kernel require `Principal.Namespace`. Empty means "default" today,
  for the earlier tests; that is exactly how a forgotten path would leak
  across namespaces. The API's tests fail on any kernel call without one,
  but the kernel itself still accepts it. (Step 0, part 1)
- ~~Confirm the ErrorInfo domain (`pli.dev`, one constant, `api.ErrorDomain`)
  before Part 3 sets the `buf breaking` baseline: clients match on
  (reason, domain), so changing it later breaks them. (Step 0, part 1)~~
  Done in Step 0, part 3: frozen as `pli.francoisnyc.github.io`, held by a test.
- `UpdateItem` and `TransitionItem` read the Item before writing, to convert
  field values against its Type (ints as strings): one extra query per write,
  and for an agent one more recorded read. Move the conversion into the
  kernel's own field check if it shows up in write latency. (Step 0, part 1)
- Filters with several Types or states, and category filters, sort every
  match before the page (`= any(...)` cannot walk the index in order on
  Postgres 17): 26-36 ms at 30,000 matches. One Type and one state use `=`
  and read one page (0.17 ms). Fix when it matters: a UNION ALL per
  (type, state) pair, each walking `items_namespace_type_state`, merged;
  or Postgres 18's skip scan. (Step 0, part 2, EXPLAIN)
- A principal who sees few of many top-level Items pays for the ones they
  cannot see: listing the top level as user-light walks 1,017 rows to
  return 17 (3 ms). The same shape as Step A's search; the UNION ALL split
  of visibility applies here too. (Step 0, part 2, EXPLAIN)
- An agent session's scopes are checked by walking containers up (one
  recursive query per Item read or written) and, for Search and List, by
  expanding the session's containers down. Fine at spike sizes; keep a
  materialized path if sessions get busy. (Step 0, part 2)
- A held transition requested by an agent is resolved without checking the
  session's scopes again; the requester's visibility is. Decide whether a
  move out of the session's scopes should deny it. (Step 0, part 2)
- Agent sessions never expire. Add an expiry (and a way to end one) before
  real agents. (Step 0, part 2)
- The relay and the worker have no health check: they serve no port. A
  worker that starts but cannot poll Temporal is only noticed when holds stay
  open. Give both a small health endpoint (or a heartbeat file) if the stack
  grows a supervisor. (Step 0, part 3)
- `EnsureRoles` only adds: a role taken out of the dev-token file stays in
  the database, with a warning at every start. Real sign-on will bring roles
  from elsewhere; until then removing one is a SQL statement. (Step 0, part 3)
- `cmd/connstub` serves through `httptest.Server` (reused for its Close).
  Fine for a stub; a real connector service would use `http.Server`.
  (Step 0, part 3)
- Postgres in the compose stack keeps its data in an anonymous volume:
  `docker compose down` without `-v` leaves one behind each time. Name the
  volume if the stack starts being kept between sessions. (Step 0, part 3)
- A link event gets an outbox row when the source's Type has listeners, so
  every link costs one Temporal call that the workflow ignores. Skip events
  no listener needs, per op. (Step 1)
- A later Type version that drops a link type or changes its cardinality
  leaves existing links as they were written: `src_single` and `dst_single`
  are snapshots. Decide what a migration does to them. (Step 1)
- Initial links on `CreateItem` (one write, the new Item as source). The
  lock helper already takes a set of targets. (Step 1)
- Enforce the finer agent powers together, with a dedicated `link` power;
  v1 checks read versus any write. (Step 1)
- Guards see incoming links only for link types that target the Type
  itself. An epic's guard cannot read its `stories` (declared by story).
  Let a Type name incoming links it reads, or derive them from the loaded
  set. (Step 1)
- Search indexes principal values like any string (`user:ana` gives the
  words "user" and "ana"). Part of per-field indexing, above. (Step 1)
- Notifications, when they come, must pass through `Project` for each
  recipient, so assigning someone who cannot see an Item sends nothing; the
  taint rule already lists assignee changes as privileged. (Step 1)
- "Linked to" at a hub (2,000 links): once analyzed, Postgres hash-joins
  every Item (27-44 ms). `= any(array(...))` takes 6 ms analyzed but 10 ms
  fresh, and 3 ms instead of 0.15 on small Items; a join is no better. Try
  a LATERAL lookup per link, or pick the shape from the link count.
  (Step 1, EXPLAIN)
- "Assigned to me" walks the Type's Items and filters (9-24 ms at 31,000):
  the same cost as other field filters; fix both together (an expression
  index per principal field, or Postgres 18's skip scan). Fresh, Get at a
  hub still sorts its links (5.8 ms); analyzed it walks the order indexes
  (0.7 ms). `RegisterType` checks link inverses but not targets; only the
  directory load at start does, and only it and the bench register Types.
  (Step 1, EXPLAIN)

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

## 2026-11-14 — Step 5 (finished): the last step of the spike

**Built:** `lifecycle.HasListeners` and the outbox skip; `types/ticket.v2.yaml` (adds
`waiting`, shortens `in_progress` to 24 h) beside v1, plus `channel` and `message`; the
Type cache (`typedef.Cache`), now on by default in the item service and the relay;
`cmd/bench` (`make bench`). New tests: the outbox skip, a 200-message burst in CI, v1 and
v2 side by side end to end. Two approved assertion changes for Types without listeners.

**Decisions:** everything stays pinned to the Item's Type version; migration, "evolve the
schema" and "float the policy" are designs under Later. The cache is on (below).

**Surprises:** v1 and v2 side by side needed no code: the workflow only ever sees a timer
table, never the Type. The first bench run measured the burst after 1,000 narrowed scopes
existed and every write paid for them (see `VisibleScopes` below), so the order was fixed
and the bench rerun. Some read numbers moved a lot between runs on the same code (below).

### Numbers

Machine: Intel Xeon @ 2.10 GHz, 4 logical CPUs, 16 GB RAM, a cloud container. Go 1.26.0,
Postgres 17.12 and the Temporal 1.10.0 dev server in local containers, no tuning. Run of
2026-11-14 (the second run; the first only differs where noted). Relative numbers, not
production ones.

**Write latency, ticket v1, ms** (1,000 calls each after 100 warm-up; every ticket write
also writes an outbox row):

| Operation | Writers | Cache off p50 / p95 | Cache on p50 / p95 | writes/s (off → on) |
| --- | --- | --- | --- | --- |
| Create | 1 | 2.85 / 4.60 | 2.31 / 3.48 | 328 → 413 |
| Update | 1 | 3.21 / 4.92 | 2.25 / 3.13 | 298 → 432 |
| Transition | 1 | 2.47 / 3.92 | 1.97 / 3.42 | 382 → 477 |
| Create | 16 | 20.39 / 24.59 | 24.96 / 33.05 | 770 → 630 |
| Update | 16 | 20.40 / 23.74 | 20.83 / 28.33 | 776 → 746 |
| Transition | 16 | 16.50 / 19.76 | 16.23 / 19.87 | 956 → 966 |

16 writers give only 2–2.5× the throughput of one, at 7–8× the latency: the single
commit-counter row serializes every write. Throughput tops out around 800–950 writes/s here.

**Type cache decision: on.** Reading and parsing a Type costs 0.23 ms p50 (0.37 p95) per
call; cached, effectively 0. With one writer the cache cut p50 by 19–30%, a real share. At
16 writers the counter dominates and the difference is noise (Create went the wrong way in
this run and the right way in the first). Versions never change, so nothing invalidates.

**Burst: 10,000 messages** (`message` and `channel` have no listeners):

| Scenario | Wall | msgs/s | p50 / p95 ms | Rows/msg | Outbox rows | Bytes/msg |
| --- | --- | --- | --- | --- | --- | --- |
| 1 writer, 1 channel | 27.0 s | 370 | 2.57 / 3.99 | 4.00 | 0 | 1,214 |
| 16 writers, 1 channel | 12.4 s | 807 | 19.47 / 24.55 | 4.00 | 0 | 1,317 |
| 16 writers, 16 channels | 11.4 s | 874 | 17.72 / 23.36 | 4.00 | 0 | 1,305 |

Each message is one Item row, one event, one values row and one idempotency row; no outbox
row, no scope, no Temporal call. Bytes include every index (the GIN search index too). One
channel versus sixteen changes little: the global counter, not the channel, is the limit.

**Reads for a principal with 1,000 narrowed scopes, ms:**

| Read | Principal | Visible scopes | p50 / p95 (run 2) | p50 / p95 (run 1) |
| --- | --- | --- | --- | --- |
| Get | light | 1 | 2.32 / 3.34 | 2.06 / 3.17 |
| Get | heavy | 1,001 | 3.75 / 5.66 | 34.54 / 53.53 |
| Search | light | 1 | 16.66 / 26.84 | 5.78 / 9.06 |
| Search | heavy | 1,001 | 38.88 / 57.97 | 39.22 / 60.11 |

A ticket Create by the light principal once 1,000 narrowed scopes exist: 5.05 / 7.16 ms,
against 2.31 / 3.48 before. The visibility check walks every child of the root scope, so
everyone pays for scopes they cannot see. Run 1 measured these reads on a database with no
messages and freshly inserted scopes; run 2 after 30,000 messages. My guess, not verified
with `EXPLAIN`: the heavy Get swings with table statistics (autovacuum had run by run 2),
and the light Search slows because the planner walks `id desc` and filters instead of
using the GIN index. Either way these reads are plan-sensitive and need a look.

**Temporal per ticket** (create → set assignee → `in_progress` → timer fires → `open` →
`in_progress` → `resolved`; 2 s timer; 3 tickets, identical results):

| Workflow runs | History events | History bytes | Actions |
| --- | --- | --- | --- |
| 2 | 42 | 6,194 | 9 |

Actions: 2 starts and 3 updates (two of them Update-With-Start that started a run, billed
as start + update), 1 timer, 3 activities (`LoadItem` per run, one `FireTimer`), no retries.
No update was refused for a gap: the relay logs every failed delivery and logged none.
Counted by Temporal Cloud's rules from docs.temporal.io/cloud/actions, as read on
2026-11-14: start 1; Update-With-Start that starts 2; de-duplicated start or update 0;
update accepted 1, rejected by the validator 1 (never in history); timer started 1;
activity scheduled 1 plus 1 per retry; continue-as-new 1; completion 0; signal-with-start
1; query 1 per query reaching a worker. Events before the first run (create, set assignee)
reach no workflow, cost a Temporal round-trip each, and are not actions.

**Open questions:** `EXPLAIN` the heavy Get and the light Search; `VisibleScopes` scaling
(Later).

## Spike write-up

### What the spike proved

- **The write path holds.** One transaction writes the Item row, the event, its values and
  (when something listens) an outbox row. Idempotency keys, gap-free `item_seq`, if-match
  with `ErrStale`, and `commit_seq` for read-your-writes all work, at 2–3 ms per write with
  one writer on modest hardware.
- **One pure lifecycle function is enough.** `lifecycle.Transition`, `TimerFor`,
  `NeedsWorkflow` and `HasListeners` serve the item service, the relay and the workflow.
  Guards and `allowed` stayed out of the workflow.
- **Durable behavior is cheap and lazy.** Most Items never touch Temporal. A ticket with a
  timer costs 2 short runs, 42 history events, about 6 KB and 9 actions for a full
  lifecycle. A timer survives worker restarts (Step 3).
- **Versions side by side came for free.** The workflow only ever sees a timer table, never
  the Type, so v1 and v2 Items ran on one worker with no version logic in it.
- **"Pay for what you use" is real.** A chat message is 4 rows, about 1.2 KB, no outbox
  row and no Temporal call.
- **Policy at the edge works.** A hidden Item is "not found" on every read and write, a
  narrowed Item disappears on the very next read, and nothing is cached that could leak.
- **Go fits** (the Step 3 checkpoint): the time went into the design, not the language.

### What surprised us

- The global commit counter, a deliberate simplification, is the throughput ceiling:
  16 writers buy 2–2.5× the throughput at 7–8× the latency.
- `VisibleScopes` costs every principal for every narrowed scope in the namespace, on
  writes as well as reads, and the read plans swing between runs.
- Temporal's edges shaped the design more than its features: `NotFound` for a completed
  workflow as well as a missing one, validators that cannot wait, and billing that counts
  rejected updates and every activity retry.
- Narrowing a container rewrites its descendants' scopes (Step 4); the fix is a scope node
  per container.
- Not one Step 1–4 test had to change for Step 5 except the two approved outbox ones.

### Top risks for the real product

1. **Write throughput.** The commit counter serializes all writes (~800–950/s here).
   Replace it with a snapshot-xmin watermark before anything else scales.
2. **Visibility cost.** Check an Item's scope ancestors upward, or keep principal →
   scopes, and add a scope node per container. Today's walk grows with all scopes.
3. **Silent event loss.** `HasListeners` must grow with hooks, link reactions and
   subscriptions; forgetting it drops events without an error.
4. **Temporal bills.** A stuck Item retries forever (~1,440 actions a day), and gap
   rejections are billed. Cap or alert, and count both.
5. **Search on the primary database.** Plans are fragile and every write pays for the GIN
   index. A separate index makes the `min_seq` contract load-bearing.
6. **Type evolution.** Pinning everything was right for a spike; real Types need new
   optional fields, floating policy and a migration with a state map.
7. **Data growth and erasure.** One idempotency row per write forever (a quarter of a
   message's rows); erasure must cover `items.fields`, `item_event_values` and `items.search`.
8. **Trust boundaries.** Attribution and `system:*` principals are trusted from Go callers;
   an exposed API must restrict them.

**Next session:** the follow-up spike (Steps 6–7: holds and connectors), starting with
`HasListeners` and the Temporal cost limits above.

## 2026-11-14 — Step A, hardening (part 1)

**Built:** `commit_seq` from a sequence and `xid8` on every event (migration 00004),
replacing the commit counter; `min_seq` waits until the write's transaction ID is below
the snapshot xmin; a 5 s statement timeout on writes and a 60 s
`idle_in_transaction_session_timeout`. `scopes.item_id` (unique), `scopes.open` and
`scope_access` (migration 00005): a container gets its node with its first child,
narrowing flips a node in place, and `Get`, writes and `Search` share one indexed rule
(`policy.VisibleCondition`). Search matches first, then filters and sorts. `cmd/bench
-explain`, lock-wait sampling and a `synchronous_commit=off` diagnostic in the bench.

**Surprises:**
- Two deadlocks came out of the concurrent policy test, both from Postgres row-lock
  rules rather than from the design. Strengthening a lock the transaction already held
  queues behind a waiter: so lock the subtree before touching the node. A second UPDATE
  of a row the transaction already changed re-checks its foreign keys, taking a share
  lock on the parent scope another change holds: so write each scope row once.
- A child created while another child was making the container's node saw the container
  "disappear": after waiting for a row lock, Postgres re-checks the query against the
  new row version, and a join can miss a row committed after the statement began. Lock
  the row in a statement of its own, then read.
- `synchronous_commit=off` gained nothing, so the new ceiling is not the disk flush.
- Match-first search is not a free win; see the plans below.

**Changed tests (approved):** the two counter tests became watermark tests; the
`lastCommitSeq` helper reads the sequence; the burst test sends one message before
counting (and so counts 201 messages in the channel, a consequence noted in the
implementation commit). Open: `TestMinSeq_WaitsForAWriteStillCommitting` races under
`-race` (its goroutine is not waited for); a fix is proposed, not applied.

### Numbers: Step 5 → Step A

Same bench, same 4-vCPU container type, but this run's CPU is 2.30 GHz against Step 5's
2.10 GHz: compare ratios within a run more than absolute numbers across runs.

| | Step 5 | Step A |
| --- | --- | --- |
| Create p50, 1 writer, cache on | 2.31 ms | 2.03 ms |
| Create, 16 writers (writes/s, ×1 writer) | 630 (1.5×) | 1,300 (2.8×) |
| Update, 16 writers | 746 (1.7×) | 1,328 (2.7×) |
| Transition, 16 writers | 966 (2.0×) | 1,463 (2.6×) |
| Lock waits during 16-writer runs | not measured | 0 samples in every run |
| Burst, 1 writer (msgs/s) | 370 | 418 |
| Burst, 16 writers, 1 channel | 807 | 1,451 (3.5×) |
| Burst, 16 writers, 16 channels | 874 | 1,444 |
| Rows per message, outbox rows, new scopes | 4, 0, 0 | 4, 0, 0 |
| Get, principal reading 1,000 narrowed scopes | 3.75 ms (34.5 in run 1) | 0.17 ms |
| Get, principal reading none | 2.32 ms | 0.16 ms |
| Create by a principal reading none, 1,000 narrowed scopes exist | 5.05 ms | 2.15 ms |
| Search ("rare" word, 1,001 matches), light / heavy | 16.7 / 38.9 ms | 3.8 / 4.6 ms |
| Search, common word (~10,000 matches) | not measured | 14.4 ms (flattened: 0.85 ms) |
| Temporal per ticket: runs, events, bytes, actions | 2, 42, 6,194, 9 | 2, 42, 6,196, 9 |

`synchronous_commit` (ticket Create, cache on): on 562 / 1,478 writes/s (1 / 16 writers),
off 514 / 1,444. No difference: the flush is not the limit.

**Done when:** no write waits on a shared row lock (0 lock-wait samples) ✓; 16 writers
reach 2.5–2.8× one writer (3.5× for messages) ✓; `min_seq` and the relay lose and
reorder nothing (watermark tests, relay tests unchanged) ✓; a principal who reads no
narrowed scope pays nothing for 1,000 of them (Create 2.15 ms against 2.03) ✓. CI green
waits on the test fix above.

### Plan shapes (EXPLAIN ANALYZE, before / after ANALYZE)

- **Get, principal reading 1,000 scopes.** Before Step A: a recursive walk over every
  scope; 3.5 ms with no statistics, 109 ms after ANALYZE (sequential scan, nested loop,
  500,500 rows discarded). After: three index lookups (items pkey, scopes pkey,
  scope_access pkey): 0.14 / 0.05 ms.
- **Search, light principal, 1,001 matches.** Before: walk `items` newest-first and
  discard non-matches (31,016 discarded, 11 ms). After: the match step (bitmap scan,
  then a sequential scan once analyzed: 3% of rows match), then the visibility test per
  match: 8.0 / 4.8 ms.
- **Search, common word, ~10,000 matches.** Match-first: 22 / 14 ms, growing with
  matches. Flattened: 11 ms before ANALYZE, 0.17 ms after (newest-first finds 50 matches
  in 152 rows). The flattened shape is fast when matches are recent and slow when they
  are old; match-first is O(matches) either way. Kept match-first for predictability;
  see Later.
- **OR vs UNION ALL.** The OR plan filters scopes first (17 rows) after ANALYZE and
  costs a scopes-pkey lookup per match before; UNION ALL was slower (28.6 ms against
  22 ms). Kept the OR.

**Next session:** apply the approved race fix (if approved), get CI green, mark #10
ready. Then option B (Steps 6–7).

### Narrowing cost (`go run ./cmd/bench -narrowing`, 2026-11-14)

A container with N descendant scope nodes (N children, each with a child of its own),
narrowed to R readers, then narrowed again to a different R readers:

| N nodes | R readers | scope_access rows written | ms | Reader change: rows | ms |
| --- | --- | --- | --- | --- | --- |
| 100 | 1 | 101 | 10 | 101 | 8 |
| 100 | 50 | 5,050 | 62 | 5,050 | 64 |
| 1,000 | 1 | 1,001 | 48 | 1,001 | 47 |
| 1,000 | 10 | 10,010 | 743 | 10,010 | 801 |
| 1,000 | 50 | 50,050 | 784 | 50,050 | 784 |

Rows written are (N + 1) × R, for the first narrowing and for every reader change: every
inheriting node below holds its own copy of the effective readers. At N = 1,000 and
R = 50 that is 50,050 rows and ~0.8 s, with the whole subtree locked. Too large: a
pointer design is proposed before going further.

## 2026-11-14 — Step A, part 2: pointer, typed principals, property test (finished)

**Built:** migration 00006: `scopes.policy_scope_id` points to the nearest narrowing
scope (null = open), only narrowing scopes keep `scope_access` rows, `scopes.open` goes.
Typed principal IDs from callers (`user:`, `agent:`, `system:`; `group:` reserved): the
actor, `on_behalf_of` (a user), readers and Type `principal:` entries are checked, and
untyped IDs are `ErrInvalid`. A property test: 3 fixed seeds × 30 random steps (creates,
narrowings, reader changes, inherits, 4 concurrent changes) against a naive evaluator.
The race in `TestMinSeq_WaitsForAWriteStillCommitting` is fixed (approved).

**Surprises:** the property test found three concurrency bugs the hand-written tests
missed, all fixed:
- a foreign key on the new pointer took a share lock on an ancestor's row, which a
  concurrent change to that ancestor held; the pointer is derived, so it has no FK;
- the subtree was listed before it was locked, so a scope added in between kept a stale
  pointer; the list is now repeated after locking until it holds still;
- inherit that removes a node updated the Item row twice, and the second update
  re-checked `container_id`, share-locking the container's Item row against a concurrent
  change to the container; the row is now updated once and the node removed afterwards.

All three share a cause: Postgres row-lock rules (FK checks, re-checks on a second
update) take locks bottom-up, against the top-down order the design relies on.

**Numbers.** Narrowing a container (`bench -narrowing`), before → after the pointer:

| N nodes | R | Rows written | ms (narrow) | ms (reader change) |
| --- | --- | --- | --- | --- |
| 100 | 50 | 5,050 → 50 | 62 → 9 | 64 → 8 |
| 1,000 | 10 | 10,010 → 10 | 743 → 41 | 801 → 34 |
| 1,000 | 50 | 50,050 → 50 | 784 → 50 | 784 → 40 |

The remaining ~40 ms is locking the 1,000-scope subtree and moving pointers.

Full bench on the pointer design (same machine type as part 1):

| | Part 1 | Part 2 |
| --- | --- | --- |
| Create / Update / Transition, 16 writers, cache on (writes/s) | 1,300 / 1,328 / 1,463 | 1,464 / 1,283 / 1,490 |
| ×1 writer | 2.8 / 2.7 / 2.6 | 2.6 / 2.2 / 2.5 |
| Lock waits, every 16-writer run | 0 | 0 |
| Burst, 1 writer / 16 writers 1 channel / 16 channels (msgs/s) | 418 / 1,451 / 1,444 | 429 / 1,142 / 1,173 |
| Rows per message, outbox rows | 4, 0 | 4, 0 |
| Get, light / heavy principal | 0.16 / 0.17 ms | 0.20 / 0.25 ms |
| Create by light principal, 1,000 narrowed scopes exist | 2.15 ms | 2.26 ms |
| Search 1,001 matches, light / heavy | 3.8 / 4.6 ms | 4.2 / 4.9 ms |
| Search, common word / flattened | 14.4 / 0.85 ms | 16.1 / 0.97 ms |
| Temporal per ticket | 9 actions, 6,196 bytes | 9 actions, 6,209 bytes |

`synchronous_commit`: on 462 / 1,345 writes/s (1 / 16 writers), off 560 / 1,690. Part 1
showed no difference; this run shows ~25%. Run-to-run noise on this shared 4-vCPU box is
about ±20% (the 16-writer burst read 1,451 then 1,142 on equivalent code), so neither run
settles where the ceiling is; Later already says to measure on separate machines.

Property test: 1.9 s locally under `-race`; the `itemservice` package takes 28 s in CI.

**Done when (Step A, all parts):** no shared lock waits ✓; 16 writers ≥ 2× one ✓ (2.2–2.6×);
`min_seq` and the relay lose and reorder nothing ✓; a principal pays nothing for others'
narrowed scopes ✓; narrowing cost O(R + scopes below), not O(N × R) ✓; stored visibility
equals a naive evaluator under random and concurrent edits ✓; CI green ✓.

**Next session:** Step A2 (one `visible()` check for every output path, moves as policy
changes, agent write rules), then option B (Steps 6–7).

## 2026-11-14 — Step A2: permission paths around the core (finished)

**Built:**
- **One door, enforced.** A source-parsing test fails if an exported read of the item
  service skips `policy.Project`. `StateEntry`, `Type` and `StartAgentSession` are
  documented exemptions.
- **Moves as a container patch on `Update`.** The mover must see the Item and the
  destination; a move into itself or a descendant is refused; a widening move needs
  `ConfirmWiden`. One event names `container_from` and `container_to`. Moves take a
  namespace-wide move lock, then the Item rows in ID order.
- **Server-minted agent sessions** (`StartAgentSession`, migration 00007). Reads record
  narrowing scopes, and the agent write rule returns `*NeedsApprovalError` until the
  session's user calls `ApproveWiden` for that scope.

**Surprises:**
- The move property test found a bug that had been there since Step A part 2. A
  recompute whose subtree root inherits wrote no rows for a narrowing scope below it:
  `writeAccess` looked up the inherited readers only among scopes it had just computed.
  Fixed, with a fixed regression test. Random tests over shapes paid off a second time.
- The node-churn test reproduced the predicted deadlock: a first child that finds the
  container's node gone after waiting upgraded `FOR SHARE` to `FOR UPDATE`. It now
  retries the whole transaction (errRetry, at most 3 times) instead.
- Row locks alone cannot stop moves on disjoint pairs from closing a cycle, hence the
  namespace-wide move lock (Later: narrow it if moves get frequent).
- Caller-chosen sessions would have let an agent escape the rule with a fresh session;
  minting them closed that.
- The container-ID leak cannot happen in a narrow-only tree (seeing an Item implies
  seeing its container). Redaction waits for grants (Later); the move property test
  guards it.

**Changed tests (approved):** agent principals get minted sessions (identifiers only);
`TestTransition_Legal_IsRecorded` expects the agent's session on its event;
`StartAgentSession` joined the `Project` test's exemption list; a bug fixed in one new
agent test.

**Numbers:** no bench rerun; Step A2 adds no work to the hot paths beyond one session
lookup per agent request. The two property tests take about 2 s each locally under
`-race`.

**Done when:** every read path goes through `Project`, enforced by a test ✓; moves are
tested for widening, narrowing, hidden destinations, cycles and crossing moves ✓; the
agent rule refuses a write from a narrow read into a wider scope, and unlocks it only
through the session user's approval ✓; sessions cannot be invented ✓.

**Next session:** option B (Steps 6–7: holds and connectors). Before exposing any API:
the `system:timer` impersonation item under Later.

## 2026-11-14 — Step 6, part 1: protect internal principals (finished)

**Built:** `hook:<type>:<hook-id>` principals (no version; `originating_hook` carries
it). Two doors: the public operations refuse `system:`/`hook:` actors and
`originating_hook`, `causation_id`, `hop`; `Service.Internal()` (Update, Transition)
takes only `system:timer` and hooks, with a cause on the same Item, hop 1..`MaxHop` (8),
origin matching the Item's Type and version, fields and state only, no visibility
check, `allowed` kept. Kernel writes record the human at the root of the cause as
`on_behalf_of`. A source test limits `Internal()` callers to `internal/workflow` and
`internal/hooks`. `FireTimer` writes through `Internal()`; `ErrInvalid` is now a final,
non-retryable `Invalid` in the activity and the workflow.

**Surprises:** the `Project` source test caught `Internal()` (exempted, approved).
`activity.GetLogger` panics outside a real activity, so unit-called activities cannot
log. Hop-0 checks come before the Item lookup, which changed one NotFound test's input.
`dockerd` has to be started by hand again after a worker restart.

**Approved test changes:** four existing tests moved the timer to `Internal()`;
no-outbox checks for the listener-less `intake` Type; `Hop: 1` on one FireTimer input.

**Open:** `admit` reads the cause event on every kernel write (one indexed lookup).

**Next session (part 2, fresh session, new PR):** hooks as activities, holds
(`item_holds`, `transition_requested`/`transition_denied`), bounded `wait`,
on_enter prefetch + CEL guard, wider `HasListeners`. Answers given ahead:
"corrects" = `allowed` with a field patch applied with the move, recorded as
supplied by the hook, never a new target state; `cel-go` is approved.

## 2026-11-14 — Step 6, part 2: hooks, holds, bounded wait, variant D (built)

**Built:** ticket v3 (triage, two enrich hooks, approve checked by `erp_check`, cancel as escape). CEL guards
(`cel.dev/cel-go`, the renamed `cel-go`) replace the spike parser. Hooks are declared in the Type and checked at
load, including each action's arity (`internal/hooks`). `item_holds`; `ResolveHold` re-checks the requester
(access, guards, `allowed`). Frozen reads, escapes, `ErrHeld`. `Transition.Wait` (polls the hold every 50 ms).
`HookInput`. Hook activities run as `workflow.Go` coroutines bounded by a budget. Stub CRM/ERP (`internal/connstub`);
`go run ./cmd/bench -holds`.

**Numbers (4 vCPU, `-holds`):** time to a definitive answer, 60 approvals at 4 concurrent callers, wait 3 s:
p50/p95 111/163 ms at 50 ms stub latency, 568/619 ms at 500 ms; 100% answered within the wait. Write
latency p50/p95 (update; hold request): baseline 5.6/8.7 and 5.8/10.5 ms; stubs down 4.5/6.9 and 5.6/9.0; worker
stopped 4.2/5.5 and 4.4/5.9. Unchanged, as the design wants.

**Surprises:** `cel-go` moved to `cel.dev/cel-go` at v0.32. Nullable typed variables make `f_x != null` and type
errors at load work with no custom code. The new hook workflow tests delivered their trigger a minute after
a start in a quiet state, which Update-With-Start never does, so the run ended at once (fixed, approved; a
test now covers a request reaching a running workflow). Temporal's test environment turns `MaximumAttempts: 0`
into 10; hooks now say "unlimited" as `math.MaxInt32`. A ScheduleToClose budget gives up up to one retry
interval early: Temporal schedules no retry that would start after it.

**Open:** which hooks ran is inferred from `causation_id` = the entering event, so a hook that wrote nothing
reruns on a reload (its key makes the write a no-op, but the connector is called again; see Later). `buf lint`
passes via `go run`.

**Next session:** Step 7 (invoice with an idempotency key, forced timeouts, variants A and B).

## 2026-11-15 — Step 7: invoice action, forced timeouts, variants A and B (finished)

**Built:** a third hook kind, `action` (ticket v4's `create_invoice` on entering approved): one key per
entry, `hook:<item>:<entered seq>:<hook>`, passed to the ERP as `Idempotency-Key`; never dropped when
the state is left; `LoadItem` reports actions not done for any entry (from `originating_hook` +
`causation_id`). `Internal().HookFailed` writes `hook_failed` (`budget_exhausted`, `key_conflict`,
`action_failed`) under its own key. A write-back whose key comes back with a different result is a
`key_conflict`, never "already written". Stub ERP invoices (keys, 409 conflicts, late or dropped
first answers). `WorkerOptions.HookAttemptTimeout`. Bench modes `-invoices` and `-variants`;
`testdata/ticket_a.v1.yaml` (variant A). Variant B lives on `spike/variant-b` (pushed, never merged).

**Surprises:** Go's `net/http` resends a POST carrying `Idempotency-Key` by itself when a reused
connection drops before the answer (`Request.isReplayable`), inside the same activity attempt: with
dropped answers Temporal saw no failure at all. The key made that safe too. A graceful
`worker.Stop()` reports its cut-off attempts, which retry at once (1.3 s); a SIGKILL is only noticed
when the attempt times out (21 s with a 20 s StartToClose). The first `-variants` run timed approvals
behind the relay's backlog of the preparation's own events (464 ms instead of 164 ms at 50 ms);
the bench now waits for the outbox to drain. Docker Hub rate-limited anonymous pulls (429); the
images came from `mirror.gcr.io` and were tagged locally.

**Open:** see the write-up below; Later has the action-input, heartbeat and reconcile items.

**Next session:** the design.md proposals were approved on 2026-11-15 (the outcome is recorded
under Steps 6–7). Decide whether the spike's follow-up ends here.

### Numbers: invoices under forced failures (`go run ./cmd/bench -invoices`)

Runs 2 and 3 (run 1 had no "no client resend" row and no retries column; its rows matched).
Duplicates are counted on the ERP side. "Temporal retries": tickets whose invoice action succeeded
on a second or later Temporal attempt, from each workflow's history.

| Run | Tickets | Keys | Forced failures | Temporal retries | Duplicates | hook_failed | All invoiced after |
| --- | --- | --- | --- | --- | --- | --- | --- |
| late answers (1 s; attempt timeout 200 ms) | 1,000 | yes | 1,000 late | 1,000 / 1,000 | 0 / 0 | 0 | 159.0 / 158.9 s |
| dropped answers, client resend on | 1,000 | yes | 1,000 dropped | 0 / 0 | 0 / 0 | 0 | 154.9 / 155.9 s |
| dropped answers, no client resend | 1,000 | yes | 1,000 dropped | 1,000 / 1,000 | 0 / 0 | 0 | 158.5 / 156.8 s |
| control: late answers, no keys | 100 | no | 100 late | 100 / 100 | **100 / 100** | 0 | 15.8 / 15.8 s |
| `worker.Stop()` mid-activity (answers after 15 s) | 50 | yes | 50 late | 50 / 50 | 0 / 0 | 0 | 8.7 / 9.0 s |
| `cmd/worker` SIGKILL mid-activity | 50 | yes | 50 late | 50 / 50 | 0 / 0 | 0 | 28.0 / 28.1 s |

Which retry layer ran: the late-answer, no-resend, control and both stop runs were retried by
Temporal (a new attempt, same key); dropped answers with the client's resend on were retried by
`net/http` inside one attempt (same key, same header). Both layers send the same key, which is
the point: 3,100 forced failures per run (1,000 late, 2,000 dropped, 100 stopped or killed
mid-activity) gave no duplicate in either run, while the same run without keys gave one per ticket. After the stops:
all invoiced 1.3 s after `worker.Stop()`, 21.0 / 20.9 s after the SIGKILL.

### Numbers: A, B and D (`go run ./cmd/bench -variants`, from `spike/variant-b`)

4 vCPU; relay polling 50 ms; 40 approvals per case, 8 concurrent callers; p50 / p95 ms of time to
a definitive answer, run 1 / run 2. "Within" = definitive on the first call (B: within its 2 s ERP
timeout). The 20% stale mix applies to D only (A and B have no prefetch); its numbers match D at 20%
on hold, with every stale customer caught by the hold (8 / 8 `denied_check`).

| Latency | Wait | Mix | A p50 | A p95 | B p50 | B p95 | D p50 | D p95 | Within (A / B / D) |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 50 ms | 3 s | 0% on hold | 207 / 161 | 212 / 214 | 62 / 62 | 66 / 64 | 160 / 166 | 208 / 259 | 100 / 100 / 100% |
| 50 ms | 3 s | 20% on hold | 160 / 159 | 212 / 212 | 57 / 59 | 62 / 64 | 158 / 161 | 211 / 216 | 100 / 100 / 100% |
| 50 ms | 5 s | 0% on hold | 165 / 160 | 215 / 212 | 60 / 59 | 68 / 63 | 167 / 165 | 260 / 261 | 100 / 100 / 100% |
| 50 ms | 5 s | 20% on hold | 163 / 164 | 261 / 215 | 58 / 56 | 65 / 62 | 158 / 163 | 217 / 259 | 100 / 100 / 100% |
| 500 ms | 3 s | 0% on hold | 617 / 619 | 667 / 625 | 509 / 509 | 514 / 515 | 616 / 571 | 673 / 675 | 100 / 100 / 100% |
| 500 ms | 3 s | 20% on hold | 576 / 579 | 620 / 671 | 507 / 506 | 511 / 510 | 618 / 621 | 625 / 684 | 100 / 100 / 100% |
| 500 ms | 5 s | 0% on hold | 618 / 618 | 682 / 670 | 510 / 509 | 515 / 514 | 616 / 615 | 624 / 674 | 100 / 100 / 100% |
| 500 ms | 5 s | 20% on hold | 622 / 621 | 675 / 670 | 506 / 506 | 513 / 510 | 621 / 619 | 675 / 672 | 100 / 100 / 100% |
| 5 s | 3 s | 0% on hold | 5,111 / 5,109 | 5,165 / 5,162 | 2,001 † | 2,002 † | 5,109 / 5,111 | 5,159 / 5,163 | 0 / 0 / 0% |
| 5 s | 3 s | 20% on hold | 5,103 / 5,107 | 5,154 / 5,162 | 2,001 † | 2,002 † | 5,102 / 5,103 | 5,149 / 5,155 | 0 / 0 / **20%** |
| 5 s | 5 s | 0% on hold | 5,113 / 5,110 | 5,165 / 5,161 | 2,002 † | 2,002 † | 5,109 / 5,110 | 5,163 / 5,159 | 0 / 0 / 0% |
| 5 s | 5 s | 20% on hold | 5,114 / 5,111 | 5,164 / 5,163 | 2,002 † | 2,002 † | 5,110 / 5,066 | 5,212 / 5,160 | 0 / 0 / **20%** |

† B at 5 s answers nothing: every approval is refused as unavailable after its 2 s timeout, good
customers included. A and D get every answer right, after the ERP's 5 s plus ~110 ms.
D's guard refuses the 20% on hold in the request's own transaction; that is its 20% within the wait
at 5 s (at 50 and 500 ms everyone is within the wait anyway, so the p50 does not move).

Where the held path's time goes at 50 ms (0% on hold, wait 3 s, run 1; p50 / p95 ms):

| Variant | Request | To the ERP | ERP + write | Back to the caller | Total |
| --- | --- | --- | --- | --- | --- |
| A | 0.2 / 7.2 | 115.7 / 146.7 | 50.7 / 51.4 | 30.2 / 51.2 | 207.5 / 211.7 |
| D | 0.4 / 4.2 | 79.9 / 121.2 | 50.5 / 52.2 | 25.3 / 50.4 | 160.0 / 207.7 |

The held path costs ~100–110 ms over B at the median: ~80 ms to reach the ERP (relay poll, 25 ms on
average at 50 ms; update-with-start, workflow task, activity dispatch, hook input read) and ~25–30 ms
back (the wait polls every 50 ms). A's row is the bench's first case and carries its warm-up (run 2
of the same case: 161 ms); D's row is the representative one. `LISTEN/NOTIFY` for the relay and the
wait (both in Later) would remove most of the two polls, ~50 ms.

Kernel write latency, stubs at 50 ms, p50 / p95 ms (100 writes, 4 writers):

| Variant | Condition | Update | Approval request (wait 0) |
| --- | --- | --- | --- |
| A | baseline / ERP down / ERP hanging | 5.1 / 7.3 · 4.9 / 7.7 · 5.0 / 7.8 | 4.8 / 8.2 · 5.6 / 7.6 · 5.6 / 7.8 |
| D | baseline / ERP down / ERP hanging | 5.0 / 7.7 · 5.1 / 10.8 · 6.4 / 9.9 | 5.5 / 7.3 · 6.3 / 9.3 · 5.3 / 7.1 |
| B | baseline / ERP down / ERP hanging | 5.7 / 8.3 · 5.2 / 7.7 · 4.8 / 8.6 | **55.6 / 58.6 · 51.7 / 52.3 · 2,001.5 / 2,002.0** |

"ERP down" answers 503 after the stub's 50 ms latency; "hanging" answers after 30 s. B's write path
holds the ERP's latency, and its timeout when the ERP hangs; A and D do not notice the ERP at all.

## Holds and connectors: write-up (Steps 6–7)

**The question.** The leaning decision of 2026-11-12: connectors run outside the write transaction,
with holds and a bounded wait. Steps 6–7 built it (D, and A for comparison) and the alternative (B: a
synchronous call before the transaction, 2 s timeout) and measured both.

**What the numbers say.**
- *Correctness under failure.* With the key passed to the connector, 3,100 forced failures per run
  (late, dropped, a stopped or killed worker) made no duplicate invoice, in two runs; without keys, one per
  ticket. Both retry layers (Temporal's and `net/http`'s own) used the same key. Idempotency at the
  connector is what makes "outside the transaction" safe; the kernel's own key cannot, because the
  side effect is not in its transaction.
- *Latency when the ERP is healthy.* B is faster: ~60 ms vs ~160 ms at 50 ms, ~510 vs ~620 ms at
  500 ms. The held path's extra ~100 ms is two polls and the workflow hop, not the design; half of
  it goes with `LISTEN/NOTIFY`. Every variant answered 100% within a 3 s wait up to 500 ms.
- *When the ERP is slow or down.* B's write path inherits the ERP: every approval takes the ERP's
  latency inside the write, 2 s when it hangs, and at 5 s latency B answers nothing (100% refused,
  good customers included). A and D keep kernel writes at ~5 ms whatever the ERP does, and get every
  answer right, late; the caller gets "pending" and the Item shows the hold.
- *D vs A.* Identical when nobody is on hold (D's prefetch only helps refusals). D refuses customers
  known to be on hold in the request's own transaction, so at 5 s latency D answers the 20% on hold
  within the wait where A answers none. The hold caught every stale prefetch (8 / 8 per run).

**Recommendation: make the leaning firm.** Connectors run outside the write transaction, always:
checks as holds with a bounded wait, enrich hooks as prefetches, side effects as action hooks whose
idempotency key reaches the connector. Use the D pattern (prefetch + guard, hold as backstop) for
checks whose deciding fact can be prefetched; plain holds (A) otherwise. Do not offer B, not even as
an option for "fast" connectors: its only gain is ~100 ms at the median, which `LISTEN/NOTIFY`
mostly recovers, and it couples every write's latency and availability to the slowest connector.
Before the product: `LISTEN/NOTIFY` for the relay and the wait, heartbeats on long actions, action
inputs read as of the cause, and reconciliation before an action gives up (all in Later).

**Re-entry semantics.** Each entry into approved is a business event, billed once: re-entering it
bills again (a new key). Right for the spike; for real billing, open: should a second approval of
the same amount be refused, or reconciled against the ERP by the ticket's identity?

**The three open questions: recommendations, not decisions.**
1. *Does the SLA clock pause during a hold?* No by default: the customer is waiting whatever the
   system is doing, and a hold lasts ~0.2–0.6 s at p50 with a healthy connector. But a timer must
   not be spent by a hold, as it is today: it should fire when the hold resolves (deferred, not
   lost). Let a real SLA object declare `pause_while: held` for Types where a long external check
   is the customer's own doing.
2. *Is "requested, not yet applied" a substate or a separate field?* A separate field, as built
   (`item_holds` and `Item.Hold`). States are business lifecycle, never plumbing; a substate would
   double the states, and every guard, timer and view would have to know about it.
3. *How does the UI show a hold?* Mostly it does not need to: up to 500 ms of connector latency,
   100% of approvals answer within a 3 s wait, so the user sees the result. When the wait runs out,
   show the Item in its state with a pending badge from `Item.Hold` ("approval pending: ERP credit
   check, since 10:42"), keep the escape (cancel) available, and turn the badge into the outcome with
   its reason code (`customer_on_hold`, `budget_exhausted`) when it resolves. `hook_failed` shows the
   same way on the Item, with a retry action once reconciliation exists.

## 2026-11-15 — Step 0 (proof of concept), part 1: API v1 server and pli (finished)

**Built:** the v1 proto (replacing the first cut, a **breaking change**: every message renamed,
`Principal` gone from requests, `Link` and `Archive` removed until built; nobody depended on it) and
`gen/`, now committed and checked by CI with `buf lint`. `internal/api` serves the seven part 1
operations over Connect, gRPC and gRPC-Web: dev-token auth (`internal/devtoken`), names with the
namespace, plain-JSON values (ints as strings), the error table with ErrorInfo (`pli.dev`).
`cmd/itemservice` serves it (refuses to start without `--dev-tokens`); `cmd/pli` drives it. Kernel:
real namespaces (`Principal.Namespace`, `EnsureNamespace`), typed refusals (`RejectedError` codes,
`InvalidError`, `HeldError`), field errors now `ErrInvalid`, an unconfirmed widening move now
`ErrRejected` (`widen_not_confirmed`), `ListTypes`, `typedef.ParseDir`.

**Surprises:** the namespace tests caught a real leak path: a hold's requester was re-checked in
"default" (roles read from the wrong namespace), so a hook's allow was denied outside it. One old test
assumed a single root scope in the database. Go 1.24's `http.Protocols` serves h2c (gRPC without TLS)
with no extra library. buf v1.75 needs a newer Go patch than `go.mod`; the go command fetches it.
A mutation check (drop the namespace on `Update` only) is caught by the API's namespace guard.

**Open:** Later has six new items (PR #18 review added fixed error messages, with values in
metadata). design.md's Moves section now matches the widening-move decision.

**Next session:** Step 0, part 2: ListItems, ListItemEvents, filters and paging, created_at and
created_by columns, category, agent sessions with scopes and powers, and their pli commands.

## 2026-11-15 — Step 0 (proof of concept), part 2: new reads, capability mode, fixed messages (finished)

**Built:** `ListItems` (a container's children, or the top level), `ListItemEvents` (history, projected:
values from item_event_values, cleared as null, erased named, reader lists shown, unseen containers
restricted, no session ID), filters on Search and List (Types, states, categories as (type, version,
state), field equality and int ranges; a filter alone is a search), keyset paging on (created_at, id)
with tokens bound to the read, caller and namespace, and Items with create time, creator, the user the
creator acted for, and category. Agent sessions in capability mode: scopes (containers or the whole
namespace) and powers (`transition:<state>`), checked live; outside the scopes NOT_FOUND, read-only
sessions NOT_ALLOWED; grants in the dev-token file, resolved by the API and fitted by the kernel; the
`Pli-Agent-Session` header, SESSION_INVALID; sessions in one namespace. ApproveWiden over the API with
a required request_id and validate_only; NEEDS_APPROVAL names the Item the write lands at and its
audience (`namespace` or `restricted`). Fixed error messages per reason, details in metadata, an
uncoded rejection INTERNAL. pli: `item list`, `item history`, filters and paging on `item search`,
`agent start` (session saved per --as name), `agent approve` (approver = token; says the audience,
needs --yes in scripts).

**Kernel changes, counted:** (1) migration 00009: items.created_at/created_by/created_on_behalf_of,
backfilled from event 1; three paging indexes; agent_sessions.namespace_id/scopes/powers.
(2) `List`, `ListEvents`, `Filter`, paging tokens. (3) `StartAgentSession` takes scopes, powers and a
grant; `AgentSession`; `ErrSessionInvalid`; scope and power checks in Get, Search, List, ListEvents,
Create, Update, Transition and moves. (4) `ApproveWiden` takes an idempotency key, approves by Item,
stays in the session's namespace; `ApprovalAudience`. (5) `NeedsApprovalError` names the Item and
whether the scope is open. (6) `RejectedError.Metadata` (from_state, to_state, fields), from new
`lifecycle.Rejection` fields. (7) `item.KnownPower` and the power vocabulary. (8) List and ListEvents
refuse an empty namespace.

**EXPLAIN** (`go run ./cmd/bench -explain`: 31,000 Items, 30,000 messages in 16 channels, 1,001
narrowed tickets; Xeon 2.1 GHz, 4 CPUs; Postgres 17.12). Fresh / analyzed:

| Read | Plan | ms fresh | ms analyzed |
| --- | --- | --- | --- |
| ListItems, a channel (~1,900 children), page 1 | `items_container_created`, stops at 51 | 0.23 | 0.12 |
| ListItems, same, page 2 (keyset) | same index, row comparison in the index condition | 0.08 | 0.05 |
| ListItems, top level, user-light (17 of 1,017 visible) | `items_top_level`, filters 1,000 hidden | 2.7 | 3.2 |
| Filter: one Type, one state, ~30,000 matches | `items_namespace_type_state`, stops at 51 | 0.22 | 0.17 |
| Same as `= any(...)` (before the fix) | seq scan + top-N sort | 26 | 30 |
| Filter: two Types, two states | seq scan + top-N sort | 36 | 26 |
| Filter: category todo (1,001 hidden tickets) | seq scan fresh; bitmap on the index analyzed | 20 | 0.7 |
| Filter: Type ticket, state open (1,001 hidden) | `items_namespace_type_state`, filters hidden | 3.9 | 2.6 |
| ListItemEvents, one Item | `item_events_pkey` + `item_event_values_pkey` | 0.06 | 0.06 |

**Surprises:** `= any('{x}')` costs a sort that `= x` does not, so single values are now `=`. A
container with no children has no scope node: content created in it lands in the scope it inherits,
so NEEDS_APPROVAL now names the Item written at, not the scope's owner, and approving an Item approves
its landing scope (its audience, which the review then asked to show). /dev/null is a character
device, so "is stdin a terminal" needs more than the mode bit (now golang.org/x/term). Eight of the new tests were wrong
(no namespace on the new reads, a stale search word, seed arguments, a whole-struct compare); fixed
with approval. No data race under -race.

**Open:** Later has six new items, among them the multi-value filter sort and sessions not
expiring. After review: design.md now records this part's decisions (capability mode, approvals and
their audience, fixed messages); pli asks golang.org/x/term whether stdin is a terminal (the first
direct dependency added in this part, approved); CI's gofmt check skips the generated gen/.

**Next session:** Step 0, part 3: `docker compose up` for the whole stack, `buf breaking`, the README
and the scenario script. The script's agent session must start on one container inside a
namespace-wide grant, so it exercises capability mode, and must pass `--yes` to `agent approve`.

## 2026-11-16 — Step 0 (proof of concept), part 3: one-command stack, scenario, README (finished)

**Built:** `docker compose up -d --build --wait` runs the whole stack: Postgres, the Temporal dev
server, a one-shot `migrate` (every service waits for it to succeed), the item service with the
example dev tokens mounted read-only, the relay, the worker and `connstub` (stub CRM and ERP, the ERP
at 1 s). Health checks and `depends_on` conditions, no sleep loops; only 8080 (API) and 8233 (Temporal
UI) are published, and `compose.dev.yaml` (opt in with `-f`) adds 5432 and 7233. One Dockerfile: a
builder on go.mod's Go (CI checks they agree), static binaries, one distroless nonroot stage per
service. New commands: `cmd/migrate` (the embedded migrations, no default `DATABASE_URL`) and
`cmd/connstub` (customers from `deploy/customers.json`, unknown keys refused). The item service gained
`GET /healthz` (200 or 503, no body, outside the contract) and `--probe`, for images with no shell.
`scripts/scenario.sh` (`make scenario`, and a CI job that always runs `down -v`) drives only pli: two
users, every refusal a client can cause (15 reasons, each code and reason checked, metadata where it
carries any), an agent session on one room inside a namespace-wide grant (NOT_FOUND outside,
NEEDS_APPROVAL, AUDIENCE_CHANGED, approve, retry), every operation across namespaces, and the PENDING
approve's asynchronous outcome; it prints the five reasons it cannot cause. README, with a walkthrough
run command by command against the stack. `buf breaking` with WIRE_JSON on pull requests. Review
fixes: 6a `errNotBuilt` removed; 6b one-transaction approvals with `expected_audience`; 6c history
names without recorded reads; 6d pli's SESSION_INVALID hint. pli: `XDG_CONFIG_HOME` on every OS,
`--expect-audience`. The ErrorInfo domain is frozen as `pli.francoisnyc.github.io`.

**Kernel changes, counted:** (1) `ApproveWiden` requires `ExpectedAudience` and reads the scope's
audience in its own transaction; a different one is a `RejectedError` (`audience_changed`, both
audiences in metadata), nothing recorded; `AudienceName`. (2) `Get` split: `visibleItem` (namespace,
readers, session scopes; no read recorded), used by history's name resolution. (3) `EnsureRoles`
(adds only; reports roles a list no longer names; validates all before writing). Proto: one additive
field, `ApproveWidenRequest.expected_audience`.

**Numbers** (this container: Xeon 2.1 GHz, 4 CPUs; images already built; the ERP stub answers after
1 s, so about 1 s of every approve is the stub):

| Measure | Value |
| --- | --- |
| Images (distroless static) | connstub 15.8 MB, migrate 20.2 MB, item service 32.2 MB, relay 47.0 MB, worker 49.4 MB |
| Build, all five images (modules cached) | 34 s |
| `docker compose up -d --wait`, fresh volumes, until every service is healthy | 6.4-6.6 s |
| Held approve, `--wait 5s` → APPLIED, first on a cold stack | 1136 ms, 1189 ms (two cold stacks) |
| Held approve, warm | 1038-1238 ms (5 runs) |
| Denied approve, `--wait 5s` → DENIED | 1081-1137 ms |
| PENDING approve → approved, seen by `item get` | 1205-1282 ms after the PENDING answer |
| Scenario, whole run (pli build cached; includes the ~2 s NOT_CAUGHT_UP wait) | 5.5-6.0 s |

Seven passing runs: five in a row on one stack, then two on a second fresh one. Reruns never collide
(each run tags its Items with its own word). No difference between a cold and a warm first approve.

**Surprises:** the script's first version expected only t1 approved at search time, but t2's PENDING
approve is allowed in the background about 1.2 s later, before the search; the fix checks t1 in and t3
out, and a new step waits for t2's outcome on purpose. Two of my Phase B test helpers hung for the
whole 10-minute test timeout when the server stopped at start (the cleanup waited on a channel the
loop had already drained). In this container, builds in Docker cannot use the session's TLS proxy:
the Dockerfile has an optional `ARG GOPROXY`, and I built through a mirror served from the local
module cache (compose's bake also refused host networking). The local `main` here was stale (PR #9),
so `buf breaking` against it listed a dozen false breaks; `make breaking` now compares against
`origin/main`, and CI fetches the base with an explicit refspec, since a shallow single-branch clone's
`git fetch origin main` updates only `FETCH_HEAD`. On macOS `os.UserConfigDir` ignores
`XDG_CONFIG_HOME`, and the go command keeps its own settings in the config directory, so the script
builds pli before it sets one. `docker compose up --wait` treats the one-shot migrate's exit 0 as done.

**Open:** Later has four new items (no health check on relay and worker, roles never removed,
connstub on `httptest.Server`, Postgres's anonymous volume).

**Next session:** Step 0 is done when the PR's CI is green (the scenario job and `buf breaking`
included): `main` then becomes the `buf breaking` baseline. Then proof-of-concept Step 1: link types and
principal fields.

## 2026-11-16 — Step 1 (proof of concept): link types and principal fields (finished)

**Built:** typed links and principal fields, through the kernel, the API, pli and the scenario.
A Type declares `links` (name, inverse, targets, `max`/`inverse_max`); `Link` and `Unlink`
(`LinkItems`, `UnlinkItems`) write one event on the source only, with its `item_seq` and etag;
the database enforces cardinality (snapshot flags under partial unique indexes); both ends must be
visible, and for an agent in its session, with the write rule on both ends. Get shows links both
ways in a fixed order (200 per direction, hidden targets restricted, hidden sources omitted); history
shows link events; neither records a read. Guards read `links.<name>` (`{state, category}`) on all
three doors, locking the linked Items FOR SHARE in UUID order with a re-read and retry. Search
filters on links (by the name the results see) and on principal fields with `me`. Ticket v5
(blocks/blocked_by, duplicate_of, `f_assignee: principal`, no close while blocked); the scenario
runs on it. Review: unknown link names in filters are refused; a link name keeps its inverse across
versions; ordering indexes for Get.

**Kernel changes, counted:** (1) migration 00010: `links`, PK, two partial unique indexes,
`links_target`, `links_source_order`, `links_target_order`. (2) typedef: link types, the
`principal` field type, `links.<name>` in guards and each transition's `GuardLinks` (from the CEL
AST), `CheckLinkTargets` and `CheckLinkInverses` in `ParseDir`. (3) `lifecycle.TransitionLinked`;
`Transition` is it with no links, which fails safe. (4) `Link`, `Unlink`, `lockLinkEnds`. (5)
`lockForTransition` on the public and internal transitions and in `ResolveHold`'s re-check. (6)
`write()` retries 40P01 and 40001 (`retryable`); an unexported test hook (set only from
`export_test.go`). (7) Links in `Get`, link events in history. (8) Link conditions, `me`, and
string/principal fields across versions in filters; `resolveFilter` takes the principal. (9)
`RegisterType` checks inverses against the registered versions. Proto, additive only (buf
breaking green): two operations, `Link`, `LinkDirection`, `LinkFilter`, `LinkType`, two event
kinds, `FIELD_TYPE_PRINCIPAL`, four reasons.

**EXPLAIN** (`go run ./cmd/bench -explain-links`: 31,000 v5 tickets, 1,000 hidden from the
reader; a hub with 2,000 links each way, 10% hidden; 16,000 links; Xeon 2.1 GHz, 4 CPUs,
Postgres 17.12). ms fresh / analyzed. Before: without the ordering indexes. After: with them.

| Read | Before | After | Plan after (analyzed) |
| --- | --- | --- | --- |
| Get, outgoing links, hub (2,200) | 5.7 / 12.2 | 5.8 / 0.71 | `links_source_order`, stops at 201 |
| Get, incoming links, hub (2,000, 200 hidden) | 3.7 / 13.9 | 3.7 / 0.63 | `links_target_order`, filters hidden, stops |
| Get, either direction, small (2) | 0.11–0.12 / 0.09–0.10 | 0.13–0.15 / 0.10 | the order indexes |
| Guard link set (blocked_by), hub | 0.53 / 0.88 | 0.54 / 0.90 | `links_target` |
| Assigned to me (300 visible of 31,000) | 23.5 / 9.0 | 15.5 / 9.7 | seq scan of items, top-N |
| Linked to, outgoing, hub (`IN`) | 5.6 / 27.5 | 5.7 / 43.7 | hash join on a seq scan of items |
| Linked to, incoming, hub (`IN`) | 8.3 / 11.1 | 8.3 / 19.3 | hash join on a seq scan of items |
| Linked to, either direction, small (`IN`) | 0.13–0.23 / 0.11–0.15 | 0.15–0.18 / 0.12–0.20 | indexes |

"Linked to" rewritten, hub out / in / small out / small in, fresh then analyzed: as a join
5.5 / 7.4 / 0.27 / 0.16, then 35.6 / 41.4 / 0.12 / 0.21; as `= any(array(...))` 10.4 / 11.1 /
3.0 / 2.6, then 6.4 / 5.7 / 0.12 / 0.12. Neither is faster both fresh and analyzed at the hub
and no slower on small Items, so `IN` stays (Later). Without `links_target`, the guard's link set
is 0.86 / 1.02 and "linked to" outgoing at the hub 9.6 fresh: it stays.

**Surprises:** cel-go takes `links.blocked_by` as a declared variable name and resolves the
qualified identifier to it, so a typo in a link name fails at load; the checked AST's reference
map gives the names a guard reads. Phase A's premise was wrong that KEY SHARE would not block a
state change (every write takes FOR UPDATE); FOR SHARE was kept as the robust choice. Once
analyzed, Postgres serves "linked to" at the hub by hash-joining all 31,000 Items, and its time
varies between runs (27.5 and 43.7 ms, same plan). Bash's `set -e` does not reach a failing
command inside `$(...)` used as an argument: the scenario's jq helper turned a jq error into "no
results", which two of my empty-result checks then passed; `fail` now leaves a marker the parent
checks. Three Phase B tests were wrong (an agent ID not among the readers, a fourth pinned Type
list, the jq helper); fixed with approval. Get returned an empty, non-nil link slice, which broke
two equality tests: no links is nil. The Docker daemon in this container stopped twice between
commands; images were built through a module mirror served from the local cache, as in Step 0.

**Numbers:** scenario, two runs in a row on a fresh stack with the final images: 6.4 s and 6.3 s;
the held approve 1,204 and 1,198 ms, the denied one 1,149 and 1,195 ms (the ERP stub answers in
1 s), as in Step 0. `go test -race ./...` green, no data race.

**Open:** Later has nine new items. **Next session:** proof-of-concept Step 2 (the vacation
request: human approvals, acting timers, link reactions, a stub email connector), once this PR
is merged.
