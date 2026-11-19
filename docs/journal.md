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
  hold? (Step 6) Done in Step 2, part 3 (proof of concept): it waits for the
  hold, behind `GetVersion`; the clock keeps running. Pausing is still open.
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
  real SLA object may declare `pause_while: held`. (Step 7 write-up) Deferral
  done in Step 2, part 3 (proof of concept); `pause_while` is still Later.
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
- `today` in guards is the UTC date; a user's local date can differ (a
  request "starting today" late in the evening in New York). Take the
  namespace's or the requester's time zone. (Step 2, part 1)
- Named guard codes: a guard refusal says only GUARD_FAILED. A transition
  could carry several guards, each with its own code (`end_before_start`,
  `starts_in_the_past`). (Step 2, part 1)
- `except` takes only `creator`. Wider forms (a role, a field) need a rule
  for which of actor and on_behalf_of must match. (Step 2, part 1)
- Anyone who sees a draft vacation request can edit its fields: updates have
  no `allowed`. Field-level or per-state writers would close it. (Step 2,
  part 1)
- A narrowed request its approver cannot see cannot be approved: `field:`
  grants nothing. Either the approver joins the readers when named, or
  narrowing is refused while a principal field names an outsider. (Step 2,
  part 1)
- `f_days` is not checked against the dates (working days, holidays). (Step
  2, part 1)
- After a recall to draft, `f_approver` keeps the last HR answer until the
  next submit corrects it; a denied resubmission leaves it stale in draft.
  Harmless (deciding needs `submitted`), but misleading to read. (Step 2,
  part 1)
- A stuck undo is visible (the workflow's `undos` query) but alerts no one.
  An undo or a `reaction_failed` must reach a person. (Step 2, part 2)
- Nothing keeps a person to one open leave balance: two open balances make
  every submit `ambiguous_balance`. A unique rule (per Type, field and state)
  would prevent it; a concurrent create is a phantom the resolve's locks
  cannot see. (Step 2, part 2)
- `after` reactions with a patch of their own (only `undoes` is built), and
  `set` besides `add`. (Step 2, part 2)
- A held reaction that fails for good resolves its hold as error with the
  reason `reaction_failed`, the name of the `REACTION_FAILED` event (a
  refused undo). Rename it (e.g. `reaction_gave_up`); a workflow test pins
  it today. Refusal codes no longer reuse event names. (Step 2, part 2)
- Undo finds its reservation by an idempotency-key prefix (`starts_with`
  over one actor's keys). Fine for the proof of concept; at scale, a
  reservations table keyed by (source, hold) or a text_pattern_ops index.
  (Step 2, part 2)
- A manager revoking an approved request (approved -> withdrawn by
  `field:f_approver`) is a separate business act, not built. (Step 2, part 2)
- Recall during the approval hold is `HELD` (only withdraw is an escape):
  with the worker down, ana waits up to the hook budget to recall.
  (Step 2, part 2)
- An acting timer's last run time is not carried across continue-as-new (or
  a crash): the next run then counts from the schedule, so an overdue one
  fires at once. Still never more than one at once. (Step 2, part 3)
- A run of an acting timer that gave up (`hook_failed`) is not tried again;
  only the next run comes. A missed reminder may matter for a real SLA.
  (Step 2, part 3)
- The dev clock is per worker. Workers on one task queue with different
  scales would give one Item mixed durations, and a running workflow keeps
  the durations it loaded until its next run. Record the scale per Type in
  the stack, or refuse a mismatch. (Step 2, part 3)
- Email is a stub: the template is the hook's ID and the recipient a
  principal. A real connector needs addresses from a directory, rendering
  outside the kernel (no free text in events), bounces, and a rate limit.
  (Step 2, part 3)
- `StartsWorkflow` starts a workflow for any `reaction_applied` of a Type
  with reaction hooks, not only for the reaction that has hooks. Harmless
  (the run finds nothing to do), but it could filter by reaction. (Step 2,
  part 3)
- Refused undos are retried by every fresh run: `pendingUndos` ignores
  `reaction_failed`, so an undo the target refused for good comes back
  after each crash or continue-as-new, and is refused (and recorded) again.
  Noise, not harm. (Consolidation after Step 2)
- Acting runs are counted as completed, not started, after a crash: a run
  that gave up (`hook_failed`) counts, so a given-up run 1 is never
  retried. (Consolidation after Step 2)
- Test harness: a delivered event reaches the same run even when the real
  system would start a new one (the run had finished). Two of this step's
  tests assumed a run stays alive in a state with nothing to do. A harness
  that refuses updates after completion would catch it. (Step 2, part 3)
- String order is byte order (`COLLATE "C"`) in every sort and comparison:
  rank keys need it. Locale-aware sorting of human text (a title column
  sorted for a French reader) would be a per-field or per-request collation.
  (Jira part 1)
- `order_by` on an enum sorts by option name, so the PLAT priorities are
  named to sort (P1..P4). Sort by declared option order instead; the options
  of different Types must then agree on it. (Jira part 1)
- `order_by` takes one declared field. Create and update times as sort keys,
  and a second key, are not there. (Jira part 1)
- One field ID should mean one thing across Types: `f_priority` is an int
  on `ticket` and an enum on the PLAT Types, so a search by it with no Type
  named is refused. A registry of shared field definitions would catch this
  at load. (Jira part 1)
- An online rebalance. `pli rank rebalance` is a quiet-time batch: two
  drags in three made while it runs land out of place (bench, Jira part 1),
  because a drag sits between keys the batch then moves. The fix is a
  generation prefix on the keys: the batch migrates cards one by one into
  the next generation, from one end, always taking the card that is at
  that end now, so a card dragged meanwhile is migrated in its turn and the
  order holds at every step (Jira's LexoRank buckets). (Jira part 1)
- An expression index per ranked field, if containers of 30,000 cards
  become a target. Sorting by a field sorts every match: at 30,000 issues
  in one space a page of the backlog by rank is 29 to 32 ms and each of a
  move's three reads 13 to 14 ms, against 3 ms for a 1,000-card board. A
  generic form: a field attribute (`indexed: true`) that makes the kernel
  keep `(container_id, value collate "C", id)` for that field. (Jira part 1)
- A transition held by a check or a held reaction may not clear fields
  (refused at load). A held move is written in two steps, the request with
  its patch and the move when the hold resolves: decide which one clears,
  and what a denied hold leaves behind. (Jira part 1)
- Write answers compute rollups and drop them: the API builds every write's
  answer with `Get`, which counts the linked Items, and only `GetItem`
  shows the counts. One count per epic write for nothing; give the kernel a
  read without rollups. Also: after ANALYZE the count of a 1,000-issue epic
  switches to a sequential scan of `items` (10.9 ms against 3.6 ms fresh).
  (Jira part 1)
- "No rule reads this": a field attribute that would skip the outbox row
  for writes that only touch such fields (a rank). Not needed yet: the PLAT
  Types have no listeners, so a drag writes no outbox row. On a Type with
  listeners a drag costs one outbox row, and six workflow history events
  when the card's workflow is running. Revisit if a ranked Type gets hooks
  or timers. (Jira part 1)
- Issue keys (PLAT-12): the kernel has no counter per container. An app
  can keep one on the space with an etag loop, at the price of serializing
  creates in a project. (Jira part 1)
- Archive (or delete) is still not built: unwatching unlinks and leaves the
  subscription behind. (Jira part 1)
- A timer at a date rather than after a duration: a sprint closing on its
  end date. Closed by hand in Jira part 1.
- Rollups count; they do not sum. "Points done of points planned" needs a
  sum of an int field, the first rollup to read fields: it must go through
  `Project`'s field rules when those exist. (Jira part 1)
- Symmetric link types (`relates` has an inverse, `related_by`, because a
  link name may not be its own inverse), and Type templates: story, bug and
  task are three files with one lifecycle. (Jira part 1)
- `RegisterType` does not check `incoming` against the registered Types;
  only `ParseDir` does, at load. (Jira part 1)
- `pli rank rebalance --in` takes an Item: the top level cannot be
  rebalanced. And a move after the last card of a run of equal keys longer
  than the cap is refused though one write would do (the run's last card
  cannot be read cheaply: ties sort by ID ascending in both directions).
  (Jira part 1)
- Web UI: a "clear" action for an optional bool. A checkbox sends true or
  false; it cannot put a bool back to unset. (Web UI)
- Web UI, not built: a readers change or a move after creation, agent
  sessions and `ApproveWiden`, board columns by state, a rebalance, live
  updates (there is no Watch), ESLint. (Web UI)
- What a generic UI misses in the API (none needed a change): a field has
  no label and no `required` of its own (forms show field IDs and mark
  nothing required); there is no "who am I" and no list of principals (the
  UI reads both from the dev-token file); a link carries only the other
  Item's name, so the Item page reads each linked Item for its title (one
  GetItem each, the first 40): a batch get, or a title on a link. (Web UI)
- ~~The stack still publishes the Temporal UI (8233) and the email stub's
  read port (8085) on every interface; only the API and the web UI moved to
  127.0.0.1. (Web UI)~~ Done in the same pull request: every published
  port, compose.dev.yaml's included, is on 127.0.0.1.
- A production build of the web UI has no sign-on at all: real sign-on
  (OIDC) replaces the dev picker as it replaces the interceptor. (Web UI)
- A state's timer is cancelled and restarted on every event of its Item,
  even one that does not touch what the timer hangs on (the `at` field, or
  nothing at all for a duration timer): the wait is "the timer, or any
  event". Keeping the timer when its deadline is unchanged would save two
  history events (TimerCanceled, TimerStarted) per write. It changes the
  commands of running workflows, so it needs `GetVersion`. (Jira 2.1)
- Computed effects, not built: `sets` on Create ("started at"); on a
  transition a check or a held reaction holds (which of the two writes
  computes, at which event's time); `before.f_x` (a field before the
  patch); links in scope; `cause.at` in guards and invariants; a marker in
  the event saying which fields were computed rather than patched.
  "Resolved by" (`cause.actor`) waits for a declared keyword resolved by
  `policy`, never a CEL variable. (Jira 2.1)
- Guards still read `today` from the item service's Go clock, effects from
  the write's stamp: around midnight UTC a guard and an effect of one write
  can see two days. Give guards the stamp's date too. (Jira 2.1)
- An `at` timer with an offset (`at: {field, to, before: 1h}`: a warning
  before a deadline), and a timer on a `date` field once a Type or a
  calendar says which zone ends the day. (Jira 2.1)
- The web UI's datetime input edits to the second and sends milliseconds;
  the search form takes an instant as typed text, with its offset. (Jira
  2.1)

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

## 2026-11-16 — Step 2 (proof of concept), part 1: approval primitives and the HR check (finished)

**Built:** the vacation request (`vacation_request@1`) goes draft -> submitted -> approved or
rejected through the API, pli and the scenario. Approval is a business state decided by an ordinary
transition; the kernel gained only generic primitives. Submitting is held by the HR check
(`hr.manager_of`, against a stub HR directory), which reads who the request is for and writes
`f_approver` on the way into submitted, or denies `unknown_employee` / `no_eligible_approver`.
The terms and the approver are frozen in submitted, approved and rejected. Submit, recall and
withdraw are `allowed: [creator]`; approve and reject `allowed: [field:f_approver]`,
`except: [creator]`. No identity guard is left; guards keep the dates and the days.

**Kernel changes, counted:** (1) the `date` field type (a CEL timestamp in guards). (2)
`writable_by: kernel`, refused on the public door, some hook must write it. (3) `frozen` per
state, `lifecycle.CheckFrozen`, checked against the state a write starts in. (4) `field:<id>` in
`allowed`, and no transition's patch (nor its check's correction, at load) may write a field its
`allowed` names. (5) `creator` in `allowed` (`policy.IsRequester`) and `except: [creator]`
(`policy.TookPartInCreation`), through `policy.MayTransition`. (6) `today` as a guard variable,
from `Options.Now`; `lifecycle.Input` / `TransitionWith`; reserved names. (7) Hooks may read
`created_by` and `created_on_behalf_of` (`HookInput`). (8) The item service checks a patch
(not writable, frozen, then held) on create, update, transition and `ResolveHold`'s correction,
and judges `allowed`/`except` with the stored fields and the creator, for the original requester
on the re-check. (9) API: `FIELD_NOT_WRITABLE`, `FIELD_FROZEN`. Proto, additive (buf breaking
green): `FIELD_TYPE_DATE`, `Field.writable_by` (`FieldWriter`), `State.frozen`,
`Transition.except`. No migration. App side: the HR action and stub, `--hr-listen`/`--hr-directory`,
`HR_URL`, compose, the Type, two dev users (dana, erin) and bob's grant to triage-bot.

**Review rework (tests changed or removed, with approval):** identity moved out of CEL. The
lifecycle tables `TestApproveGuard_NoSelfApproval` and `TestRequesterGuard_RecallAndWithdraw`
became policy tests (every row kept); `TestApproveGuard_WithoutWhoFailsSafe` went (no identity
guard is left); `TestGuardVariables_Compile` became `_OnlyToday`; the five `TestGuardCaller_*`
became `TestCreator_*`; the requester's self-approval and other people's recall or withdraw
became `NOT_ALLOWED` instead of `GUARD_FAILED`. Then `except` widened: `TestIsCreator` became
`TestIsRequester`, `TestTookPartInCreation` was added, and the agent-alone row (named approver)
flipped to refused, also in the item service. Three tests pinning the Types in `types/` gained
`vacation_request@1`. One scenario step of mine expected `NOT_ALLOWED` before a failing guard.

**Surprises:** my CEL requester guard started with `caller == created_by`, so the agent that
created ana's request passed it while acting for bob; the review moved identity to declared
rules, computed in one place. `creator` then needed two meanings: the requester in `allowed`,
anyone who took part in creating the Item in `except` (separation of duties is conservative).
Guards run before `allowed`, so a refusal test must hold the data valid to reach `allowed`.
cel-go takes a Go `time.Time` as a timestamp, and a null `today` makes a guard fail to evaluate,
which is the fail-safe wanted for callers that pass no date. Docker Hub answered 429 again
(images from `mirror.gcr.io`), the Docker daemon died and left a stale pid file, and image
builds used the module cache served over HTTP as `GOPROXY`.

**Numbers:** scenario, two runs in a row on a fresh stack: 7.7 s and 7.8 s; the held submit
(`--wait 5s`, HR answers at once) 127 and 231 ms; the ticket's held approve 1,155 and 1,147 ms
(the ERP stub takes 1 s). `go test -race ./...` green, no data race.

**Open:** Later has seven new items (UTC today, named guard codes, wider `except`, editing
others' drafts, approvers outside the readers, days against dates, a stale approver in draft).
**Next session:** proof-of-concept Step 2, part 2 (link reactions, the leave balance, the held
deduction and its compensation), once this PR is merged.

## 2026-11-17 — Step 2 (proof of concept), part 2: link reactions, the leave balance, the saga (finished)

**Built:** approving a `vacation_request@2` holds while the reaction `deduct` adds its `f_days`
to the requester's `leave_balance@1` (`f_used`); the approval applies if that write commits, or is
DENIED `insufficient_balance` (the balance's invariant). `refund` undoes it, by the recorded
delta, once per deduction: when the hold closes any other way than allowed, and when an approved
request is withdrawn. The request's `balance` link is set by the kernel on entering submitted
(the unique open balance owned by the requester), never by the requester. HR (hana, `hr_admin`)
owns and allots balances. Through the API, pli and the scenario. design.md has the section.

**Kernel changes, counted:** (1) `reaction:` principals, a kernel actor, relaxed only where a
reaction acts. (2) Reactions in typedef: held and after (undo), consent (`accepts_reactions`),
load rules within and across Types. (3) Invariants (CEL over fields, last on every door).
(4) `writable_by: [role:...]` (policy.MayWriteRoleField). (5) Kernel-set links: `writable_by:
kernel` and `resolve` on a link type, LINK_NOT_WRITABLE, the resolution on entering a state with
its one-pass lock order, LINK_UNRESOLVED, one per state. (6) Holds decided by a held reaction.
(7) `Internal().React`, `Undo`, `ReactionFailed`, `DenyReason`; `add` with `field_deltas`
(migration 00011). (8) `StateEntry.EnteredHold` and `PendingUndos`. (9) Workflow: RunReaction /
ResolveReaction, undos never dropped (1 s to 1 min backoff, no limit), the `undos` query,
continue-as-new after 100 undo attempts; the relay passes the state left. (10) API and proto,
additive (buf breaking green): Type invariants, reactions, accepts_reactions, link writable_by
and resolve, Field.writable_by_roles; ItemEvent added, link_replaced, reaction,
REACTION_FAILED; INVARIANT_FAILED, LINK_NOT_WRITABLE, LINK_UNRESOLVED. App side: the two Types,
hana in the dev tokens, the scenario on v2.

**Test fixes after Phase C (approved):** (1) principals passed to `ListEvents` carry the
namespace `default` (reads need one); (2) the agent rows of
`TestLeaveBalance_OnlyHRWritesOwnerAndAllowance` run on a balance the agent can see, with one row
kept: an agent that cannot see the narrowed balance gets NOT_FOUND before the role check;
(3) `StuckUndo_BacksOff…` fails for 80 min in one run, under the 100-attempt cap (hours cross
runs in `…_ContinuesAsNew…`); (4) history reasons are upper case (`REACTION_NOT_ACCEPTED`);
(5) history ints are strings (`"5"`). Earlier, three tests pinning `types/` gained the two new
Types. A refusal for an amount that cannot be evaluated is `reaction_amount_invalid`, so no
refusal code reuses an event kind's name.

**Surprises:** the undo cannot recompute its amount: `withdrawn` freezes nothing, so a
requester could edit `f_days` before the refund ran; the delta must be stored. Writable-by-kernel
on `f_owner` would have locked HR out too, hence role writers. A stuck undo is only visible if
the workflow retries itself (Temporal's own retries happen out of its sight), which costs
history, hence the continue-as-new cap. Reruns of the scenario would make every balance lookup
ambiguous without a lifecycle on balances. An existing test that walks the source caught a refusal without a named code. In the
scenario, the withdrawal during the approval hold always won (the deduction ran 0 times): the
reverse order is covered by the Go race tests. The Docker daemon died twice; images were built by
hand with the local module cache as `GOPROXY` and tagged for compose.

**Numbers:** scenario, two runs in a row on a fresh stack: 10.5 s and 10.2 s; the held approval
with the deduction (`--wait 5s`) 175 and 226 ms; the HR check 178 and 123 ms; the ticket's ERP
check (1 s stub) 1,149 and 1,092 ms. The race of two 15-day approvals on 20 days left: one
APPLIED, one DENIED `INSUFFICIENT_BALANCE`, `f_used` 20 of 25, both runs. Go: 5 rounds of 20
concurrent deductions on 25 days gave exactly 5 each; 20 escape-versus-deduction races left as
many refunds as deductions. `go test -race ./...` green, no data race; the three histories
recorded from main replay.

**Flaky test on main.** Before any change, the baseline `go test ./...` (9cccd7f) failed once in
`internal/hooks`:

```
--- FAIL: TestDroppedAnswer_DefaultConnectors_NetHTTPResends (0.00s)
    resend_test.go:36: result [INV-1], ERP invoices [INV-1], dropped 0; want one invoice, returned, after one dropped answer
FAIL
FAIL	github.com/francoisnyc/work-kernel/internal/hooks	0.124s
```

The invoice was made once and returned, but the stub reported no dropped answer; it passed 7
reruns alone and every later full run. The stub (`connstub`) closes the connection and only then
counts the drop, so the resend can finish, and the test read the counter, first. Not fixed here
(Later). If it fails in CI on this PR, stop and report; no reruns until green.

**Open:** Later has eight new items (the flaky test, alerting on a stuck undo, one open balance
per person, after reactions with patches, the `reaction_failed` hold reason, the reservation
lookup at scale, manager revocation, recall during the approval hold).
**Next session:** proof-of-concept Step 2, part 3 (acting timers, timers deferred during holds,
the stub email connector, the full scenario), once this PR is merged.

## 2026-11-17 — Step 2 (proof of concept), part 3: acting timers, deferred timers and the email stub (finished)

**Built:** `vacation_request@3` reminds its approver (48 h, then 24 h later, two runs), expires
after 120 h when nobody decides, and emails the requester when approved, rejected, expired or
refunded, through a stub email connector. A state timer due during a hold now waits for it. The
compose stack runs `vacation_request` on a dev clock (a day lasts 2 s), so the scenario watches
reminders and the expiry in about 10 s. design.md has the section ("Time and mail").

**Kernel changes, counted:** (1) typedef: acting timers on states (`{id, after, every, max,
hook}`, load rules), hooks `on: timer` and `on: reaction`, an action hook may write nothing when
its action writes nothing, the `creator` attribute. (2) lifecycle: `ActingTimers`,
`ReactionHooks`; an acting timer is behavior (`NeedsWorkflow`); `reaction_applied` starts the
workflow for a Type with reaction hooks. (3) hooks: `email.notify` (`Call`, `RunCall`).
(4) itemservice: `Internal().HookRan` (`hook_ran`), `Internal().ReactionApplied`
(`reaction_applied`, `not_applied`), `creator` in hook inputs, `hook_failed` naming the timer
and run, `StateEntry.ActingRuns`, reaction hooks in `PendingActions`, a pending undo done only
once both refund and `reaction_applied` exist (Types with reaction hooks), timer and run in
history. (5) workflow: acting timer coroutines, reaction hooks run on `reaction_applied`, state
timers deferred during a hold and `Held` not final behind `GetVersion("timer-waits-for-hold")`,
LoadItem scaling only the listed Types' declared durations and recording `DevDay`, Undo writing
`reaction_applied`. (6) relay: the reaction of `reaction_applied`. (7) API and proto, additive
(buf breaking green): `State.acting`, `ActingTimer`, `ItemEvent.timer` and `run`, `HOOK_RAN`,
`REACTION_APPLIED`. App side: the v3 Type, the email stub and its read-only port, a per-customer
ERP latency, the worker's dev clock flags, compose, README (curl), the scenario.

**Test changes (approved):** after Phase B, four fixes of my own new tests (a helper renamed
`vr3Cause`, `mailWorld.get` added, the `expired` event delivered after FireTimer, an index
guard). For the dev-clock decision (option 2): the worker flag tests rewritten for
`--dev-time-scale-types` (missing, empty, an empty name, without a scale, unknown at start),
`DevTypes` added to the two mail e2e worker configs, a new e2e test of the deferral on the real
stack (`deferral_e2e_test.go`), the scenario's ticket deferral step removed. After Phase C:
(1) Step 6's `TestHooks_TimerRefusedAsHeld_IsSpent` asserted the old rule; renamed
`…_OldVersionIsSpent` and pinned to `DefaultVersion` with `OnGetVersion`, its assertion kept;
(2) `TestActing_ANewEntryCountsAgain` keeps its run alive in draft (a long timer) and carries
`corr-1` on both events; (3) `TestReactionApplied_RunsTheRefundEmail` delivers the event with
the start. `cust-slow` left `deploy/customers.json`.

**Surprises:** two of my tests assumed a run stays alive in a state with nothing to do (draft,
withdrawn); the real system starts a new run for the next event, but the test harness happily
delivers to the finished one (Later). A Step 6 test encoded the old held-timer rule; versioning
the change let it stay, as the test of the old branch. Acting timers had to be coroutines, not
part of the main loop's timer: an `AwaitWithTimeout` woken early cancels its timer, a command,
so any change to the main loop's waits would have broken the old histories' replay. Scaling a
duration by `d * day / 24h` overflows int64 in nanoseconds (120 h × 2 s); it is computed in
float. The `reaction_applied` payload's `target` (`<item>:<seq>`) collided with the link
events' `target` (a UUID) in the history decoder, which then failed to read the whole page; it
now reads `target` as a UUID only when it is one. The worker's dev clock checks names against
the loaded Types, which the item service loads, so in compose the worker now waits for the item
service. The Docker daemon died twice again.

**Numbers:** scenario, two runs in a row on a fresh stack: 28.4 s and 27.1 s (stack up 7.8 s
and 7.4 s); part 2 was 10.5 s, and the rest is the dev clock. Vacation steps, run 1 / run 2:
reminders twice then the expiry 10.45 / 10.44 s (five dev days are 10 s); approve and its email
0.78 / 0.58 s; withdraw, refund and its email 0.46 / 0.44 s; the denied approval 0.49 / 0.49 s;
the final check of every email 4.07 / 4.07 s (two more dev days, waited on purpose). The worker
logged its dev clock warning on start. Go: 57 new test functions; the deferral e2e tests 8.3 s
(allowed: the timer does nothing) and 7.4 s (denied: the timer moves the ticket after the
denial). `go test -race ./...` green in 154 s, no data race; the three histories recorded from
main replay. The flaky `TestDroppedAnswer_DefaultConnectors_NetHTTPResends` did not show.

**Open:** Later has six new items (acting runs across continue-as-new, a given-up reminder not
retried, a per-worker dev clock, a real email connector, `StartsWorkflow` per reaction, the
harness delivering to a finished run); two older ones (the held timer spent) are marked done.
**Next session:** proof-of-concept Step 3 (the Jira scenarios), once this PR is merged; plan
it per step as the build order says.

## 2026-11-18 — Consolidation after Step 2: seven bugs where Step 2's primitives meet (finished)

**Built:** fixes for seven bugs an adversarial review of Step 2 found by reading the code, each
traced, each first reproduced by a failing test (Phase B), then fixed in its own commit with its
tests named (Phase C). Plus the flaky stub test, fixed, and two noise items, Later only.

**Kernel changes, counted** (seven, one per bug; no proto change):
(1) workflow activity `RunReaction`: a target refused as held (`ErrHeld`, checked before
`ErrStale`) is a retryable error, no longer nil. (2) workflow activity `RunCheck`: a correction
`ResolveHold` refuses (Invalid, Rejected, Forbidden) closes the hold as error,
`correction_refused`, under its own key `hold:<id>:refused`; typedef refuses a check writing a
field its from-state freezes. (3) itemservice `React`: under the target lock, the resolve link's
lookup runs again (`resolveCandidates`, now shared with resolution); none, several or another
Item deny with `link_target_changed`. (4) itemservice `ListEvents`: a `causation_id` naming
another Item is shown only to a reader who can see it, else omitted (as is one that does not
parse). (5) typedef refuses a check or held reaction on an escape; the relay marks an escape's
event `Canceled`; the workflow never reads a canceled hold as allowed, behind
`GetVersion("canceled-hold-not-allowed")`, asked only of a `Canceled` event. (6) typedef refuses
`resolve.on_enter` on the initial state, and a held reaction leaving a state other than its
resolve link's `on_enter`. (7) lifecycle and relay: an event closing a hold any other way than
allowed, on a Type with a held reaction leaving that state, starts the workflow. Not the kernel:
(8) the connstub drop counter.

**Each bug, before and after:**
1. *A target's open hold left the source's hold open forever.* Before: `RunReaction` returned nil
   while pot's hold was open; end to end, src was still held 15 s after pot's hold was allowed.
   After: a retryable error; src allowed once pot's hold closed (1.2 s). The workflow test (budget
   runs out: error, `budget_exhausted`) passed before too: the workflow half was right.
2. *A refused correction left the hold open forever.* HR answering `bob`, not `user:bob`. Before:
   the hold still open past the 2 s budget plus 3 s; the activity returned the Invalid refusal.
   After: the hold closes as error, `correction_refused`, at once (0.2 s), one event, the request
   in draft; a retried activity ends with nil or Stale, never a key reuse. The load rule was
   missing; the guard (a non-correction refusal is not giving up) passed before and after.
3. *A held deduction went to a balance resolution would no longer pick.* Before: closing ana's
   balance, giving it to erin, or opening a second one, then approving: allowed, `f_used` 5 in all
   three. After: DENIED `link_target_changed`, no balance touched.
4. *Another Item's ID leaked through history.* Before: a reader who sees the balance but not the
   (narrowed) request found the request's ID in the balance's history, in `causation_id` only,
   through `ListEvents` and the API. After: omitted for that reader, kept for ana, who sees both.
5. *Holds on escapes; a canceled hold read as allowed.* Before: such Types loaded; the relay sent
   `Canceled: false`; a canceled event entering the requested state queued no undo. After: refused
   at load; `Canceled: true`; the undo runs, caused by the cancel, and leaving that state later
   runs no second one. A fourth recorded history (main at b0f92a4: v3's approval hold canceled
   by the withdrawal, `canceled_hold_id` folded into `HoldID`) replays, with the other three.
6. *Two resolve-link load rules.* Before: both Types loaded. After: refused; a link people set
   stays exempt.
7. *A lost undo when the run was dead.* Before: the escape started no workflow (relay test:
   `Start` false); end to end (a real run took the request, then was terminated before deciding;
   the deduction committed by hand; the withdrawal): after 15 s `f_used` was still 5, no refund,
   the escape delivered and the workflow terminated. After: the escape starts a run, whose
   LoadItem finds the undo: refunded once (3.5 s).
8. *The flaky `TestDroppedAnswer_DefaultConnectors_NetHTTPResends`* is fixed: connstub counts the
   dropped answer before closing the connection. The review saw 3 failures in 900 runs before, 0
   in 1,800 after. Here, before: 0 of 900 idle, 75 of 2,700 with `-race` and 4 CPU burners (all
   `dropped 0`). After: 0 of 1,800 idle, 0 of 2,700 with `-race` and the same 4 burners. Its Later
   entry is removed.

**Test changes:** none to existing tests. 19 new test functions, and the fourth recorded history
(its own commit, recorded before any workflow change). `Event.Canceled` was a contract stub in
Phase B.

**Surprises:** the item 1 and item 7 reproductions needed a real run caught mid-decision: a row
lock on the balance keeps the run's `React` waiting while the request is delivered, then the run
is terminated. `activity.GetLogger` panics outside an activity (a test calling the activity
directly), so `RunCheck` does not log the refusal; the `transition_failed` event records it.
`GetVersion` works in an update handler, and asked only of a `Canceled` event it leaves every
recorded history untouched. Item 2's error resolution needed its own key: the check's key
`hold:<id>` is the correction's, and a retried activity sends the correction again. The flaky
test only shows under load: 0 of 900 on an idle machine. The Docker daemon died twice.

**Numbers:** `go test -race ./...` green in 161 s, no data race; the four recorded histories
replay. Scenario, two runs on a fresh stack: 27.9 s and 26.9 s (stack up 8.3 s and 7.4 s). Steps,
run 1 / run 2: vacation submit with the HR check 0.28 / 0.23 s; bob approves with the deduction
0.16 / 0.16 s; leave: an approved request deducts 0.39 / 0.40 s, withdrawing refunds 0.18 / 0.29
s, the race of two approvals 0.58 / 0.57 s; mail: reminders then expiry 10.43 / 10.39 s, approve
and email 0.62 / 0.62 s, withdraw, refund and email 0.44 / 0.44 s, the final email check 4.07 /
4.06 s.

**Open:** Later has two new items (refused undos retried by every fresh run; acting runs counted
as completed, so a given-up run is never retried) and loses one (the flaky test).
**Next session:** proof-of-concept Step 3 (the Jira scenarios), once this PR is merged.

## 2026-11-18 — Jira proof of concept, part 1: the PLAT software project (finished)

**Built:** scenarios A and D of design.md run through pli, on eight new Types (`space`, `story`,
`bug`, `task`, `epic`, `sprint`, `comment`, `subscription`). The board is a search for the Items
linked to a sprint, ordered by rank; "my open work" is a search across three Types ordered by
priority; an epic shows its progress as each reader sees it and does not close while an issue is
open; done requires a resolution and reopening clears it; watching is a Subscription and a link.
Rank is an app field: `internal/rank` (base-62 fractional keys, byte order) and `pli rank`.

**Kernel changes, counted** (eight; proto v1 only grew, `buf breaking` green):
(1) `order_by` on List and Search: one declared field (string, int, date, enum), asc or desc,
nulls last both ways, ties by ID ascending; keyset tokens carry the sort value and are bound to
the order. (2) Field filters and `order_by` across Types: the selected Types, or every registered
Type that declares the field; refused when nobody declares it or the declarations disagree.
(3) Date filters (equality, ranges). (4) String range filters; every text comparison and sort is
`COLLATE "C"`. (5) `incoming`: a Type names links other Types declare towards it; guards read
them (`typedef.LinkSource`, `CheckIncoming` in `ParseDir`). (6) Rollups: counts over a readable
link name by category or state, on `GetItem`, counting what the reader sees and recording an
agent's narrowed reads. (7) `clears` on transitions, with its order of checks and load rules.
(8) `targets: ['*']`. Proto: `order_by` (two requests), `Item.rollups`, `Type.incoming`,
`Type.rollups`, `Transition.clears`. No migration; no workflow change (the four recorded
histories replay; no `GetVersion`). Not the kernel: `internal/rank`, pli's `rank` commands and
`--order-by`, the Types, `cmd/bench --rank` and `--explain-order`.

**EXPLAIN** (`go run ./cmd/bench --explain-order`; 30,000 issues in one space, 2% hidden, 1,000
linked to one sprint and 1,000 to one epic, 5,000 tickets beside them; the item service's own SQL,
captured with a pgx tracer; fresh = autovacuum off, no statistics):

| Read | Fresh (ms) | Analyzed (ms) | Analyzed plan |
| --- | --- | --- | --- |
| Board: 1,000 linked to the sprint, by rank, page 1 | 5.6 | 3.4 | links -> items nested loop, top-N heapsort of 850 |
| Board, page 2 (keyset on the rank) | 4.0 | 3.0 | same |
| Backlog: the space's 30,000 children, three Types, by rank, page 1 | 58.1 | 32.3 | parallel seq scan, sort of every row |
| Backlog, page 2 | 82.0 | 28.8 | same |
| A move's neighbour read: highest rank below a key, 2 rows | 24.7 | 14.3 | parallel seq scan, sort of every match |
| A move's tied-run read: rank = a key | 8.7 | 13.4 | scan, no sort |
| My open work: assignee = me (1%), todo and active, by priority | 16.7 | 10.6 | top-N heapsort of 150 |
| Open P1 (7,500), by points descending, 20 per page, page 1 | 40.0 | 24.2 | sort of every match |
| Open P1, page 3 | 72.1 | 24.7 | same |
| No Type named: everything in todo, by rank | 47.8 | 22.6 | sort of every match |
| Words "alpha" (35,000 matches) among three Types, by rank | 63.7 | 47.6 | top-N heapsort over the matches |
| Sprints ending in a date range, latest first | 0.1 | 0.3 | 27 rows |
| The epic's rollup: 1,000 issues counted by state | 3.6 | 10.9 | seq scan on items after ANALYZE (nested loop fresh) |
| The epic's guard: the link set of its 1,000 issues | 0.3 | 0.4 | index scan on links_target |

Sorting by a field sorts every match, as expected. No plan demands an index at PLAT's size (a
board is 3 ms). At 30,000 cards in one container a move is three reads of 13 to 14 ms: an
expression index per ranked field is in Later, not built.

**Rank bench** (`go run ./cmd/bench --rank`; 1,000 stories in one space, in process, 4 CPUs).

Key length, in bytes (threshold 32):

| Run | Longest | Mean |
| --- | --- | --- |
| Even keys (after a rebalance) | 3 | 3.0 |
| 4,000 random moves by 8 users | 7 | 4.2 |
| Always at the top, midpoint keys (first version): 100 / 200 / 500 / 1,000 inserts | 22 / 40 / 92 / 184 | 3.9 / 6.4 / 22.8 / 71.2 |
| Always at the top, end-aware keys: 100 / 200 / 500 / 1,000 / 2,000 / 3,000 | 6 / 8 / 14 / 24 / 44 / 64 | 3.2 / 3.6 / 5.6 / 11.2 / 26.7 / 45.3 |
| Always right below the same card (midpoint): 100 / 200 / 500 / 1,000 | 21 / 38 / 92 / 180 | 3.9 / 6.3 / 22.3 / 69.5 |

Threshold crossings: none in 4,000 random moves. Always at the top: first at insert 160 with
midpoint keys (841 of 1,000 past it; one rebalance every 152 to 168 inserts), at insert 1,415
with end-aware keys (no rebalance in 1,000 inserts): 8.8 times rarer. Always into the same gap:
first at insert 167, one rebalance every 161 to 169 inserts.

| Moves | p50 (ms) | p95 | p99 | Reads | Writes |
| --- | --- | --- | --- | --- | --- |
| 8 users, random before/after (287 moves/s) | 27.3 | 34.2 | 37.4 | 3.00 | 1.000 |
| 1 user, before a card | 12.9 | 16.0 | | 3.00 | 1 |
| 1 user, right below the same card | 12.1 | 16.0 | 18.0 | 3 | 1 |
| 1 user, always at the top | 5.2 | 7.3 | 8.4 | 1 | 1 |

Re-keys: 0 in 4,000 concurrent moves, largest tied run 0, no two cards ever shared a key, none
refused at the cap (8); 6 moves were planned again because a card moved between two reads.

Rebalance of 1,000 cards, alone: 995 rewritten in 2.53 s (2.5 ms per card), 2 reads, 995 events,
995 value rows, 0 outbox rows; longest key 180 -> 3.

The write path per drag (200 drags):

| Type | Events | Value rows | Outbox rows | Update p50 (ms) | p95 |
| --- | --- | --- | --- | --- | --- |
| story (no listeners) | 1 | 1 | 0 | 2.4 | 3.4 |
| story with a hook and a timer | 1 | 1 | 1 | 2.3 | 3.3 |

A card of the second Type dragged 20 times: no workflow history when none is running (the relay
finds nothing listening); 6.0 history events per drag when its workflow is running on a timer.

**The rebalance finding.** 400 drags by 4 users next to cards that are never dragged, while a
rebalance of the same 1,000 cards runs:

| Run | Out of place | Anchors out of order | Skipped by the rebalance | Re-keys |
| --- | --- | --- | --- | --- |
| Rebalance, order kept at every write (pli's) | 261 | 0 | 332 | 0 |
| Rebalance written top to bottom | 325 | 24 | 243 | 28 |
| No rebalance | 0 | 0 | | 0 |

Written top to bottom, a rebalance mixes old and new keys: the board is out of order while it
runs and cards meet on one key. `rank.RewriteOrder` (keys that move down first, from the top;
then keys that move up, from the bottom) keeps the container in order after every write, a
property test holds it, and the anchors stay in order. It does not save the drags: a drag is
placed between its neighbours' keys of that moment, the batch then moves those neighbours, and
two drags in three end up elsewhere. So the rebalance is a quiet-time batch (pli says so each
time it runs, and the README), end-aware keys make it rare, and the online version is in Later.

**Test changes to existing tests** (each agreed in review): five refusal cases replaced by the
nearest refusal that still holds (a field no registered Type declares; two Types declaring a field
with different types; a range on a principal, or on a bool in the API); the three pinned lists of
Types gain the eight PLAT Types. Fixed in tests written in Phase B, which had failed at setup and
never run: a link filter named by its inverse, a key expected on a card not ranked yet, summaries
that parsed as numbers, and a flake in the scenario (the number of cards a rebalance rewrites).
78 new test functions; `TestBetween_AlwaysAtTheTop` became `TestBetween_AlwaysAtAnEnd` and
`TestBetween_AlwaysIntoTheSameGap` with the end-aware keys.

**Surprises:** the bench found two things no test had: `rank.Plan` called a card that was moved
between two of its reads a tied run (now `ErrChanged`, and the move is planned again), and the
rebalance above. Postgres compares text by the database's collation, and nothing in the repo
pinned one: without `COLLATE "C"` rank order would depend on the image. Ties sort by ID ascending
in both directions, so the last card of a run of equal keys cannot be read with one descending
query. After ANALYZE the rollup's count switches to a sequential scan of `items` (10.9 ms against
3.6 ms). Phase B tests that fail at setup are not tested tests: four of them had bugs, and the
pinned lists of Types belong in Phase B next time. Docker builds
still need the module cache served as `GOPROXY`, and the daemon does not survive between turns.

**Numbers:** `go test -race -count=1 ./...` green in 142 s, no data race. Scenario, two
runs in a row on a fresh stack: 29.3 s and 28.9 s (stack up 7 s); the board 16 / 18 ms, a rank
move 29 / 34 ms, 47 open P1 Items in 3 pages 82 / 82 ms.

**Open:** a move's cost is its three reads (12 ms of a 13 ms move at 1,000 cards), not its write;
`Threshold` 32 and `MaxGrowth` 4 held up (random drags reach 7 bytes); the Later list has this
step's sixteen entries, the online rebalance first.
**Next session:** the SLA design pass (paper only), then Jira part 2 (the IT service desk).

## 2026-11-18 — A generic web UI for the proof of concept (finished)

**Built:** `web/`, one generic UI (TypeScript, Vite, React, connect-es) that is a client of the
public API only, like pli. No screen names a Type: forms come from `GetType`'s fields, buttons from
its transitions, the inbox from every transition whose `allowed` names `field:X`, the board from a
link and a rank field. Act as any dev user; browse the top level and search (words, Types, states,
categories, field conditions, `order_by`, paging); an Item's page (fields, transitions, links both
ways, rollups, contained Items, history); create in a container, with readers; link and unlink from
either end; held moves with `wait` (APPLIED, DENIED, PENDING, ERROR); refusals shown with code,
reason and metadata; a sprint board ranked by dragging. A `web` container in compose serves the
site and proxies `/api`; `docker compose up` gives http://localhost:8090.

**No kernel change, no proto change, no new call.** `buf breaking` has nothing to compare.

**The one non-test Go change:** `internal/rank/rank.go` now draws its jitter through an unexported
variable, `var int64N = rand.Int64N`, where it called `rand.Int64N` directly (two call sites in
`random`). Nothing but the in-package test `vectors_test.go` assigns it. It exists so the golden
vectors can fix the draw and hold the TypeScript port to exact keys, not to a window.

**Rank in two languages.** `web/src/rank` ports `rank.go` and `move.go`'s `Plan` (BigInt for
`big.Int`; `RewriteOrder` is not ported, the UI does no rebalance).
`internal/rank/testdata/vectors.json` (67 KB) is written and verified by `TestVectors`: Valid,
Compare, NeedsRebalance, Evenly (22 cases), Between (30 gaps at five fixed draws) and Plan (22
cases at three draws: one write, re-keyed tied runs, the cap, no rank, changed). The port passed
all 264 vector tests on its first run. A drop on the board is `Plan` over `ListItems` on the moved
card's container, then one `UpdateItem`: the smoke test checks the container's order, with a story
that is not on the board between two that are.

**Codegen and CI.** `make gen-web` (`buf.gen.web.yaml`, protoc-gen-es from `web/node_modules`)
writes `web/src/gen`, committed; `make gen` and the `test` job stay Node-free. A vendored
`google/rpc/error_details.proto` gives the client `ErrorInfo` and `BadRequest`. New `web` job:
client current, `tsc`, vitest, `vite build`. The `scenario` job runs the Playwright smoke test
after `scenario.sh`, on the same stack, and uploads traces on failure.

**Dev-only, and the ports.** The picker reads the real `dev-tokens.example.yaml`, mounted
read-only into the web container and served at `/dev-tokens.yaml`: every dev token is readable on
that port. So compose now publishes 8090 **and the item service's 8080** on 127.0.0.1 only (they
were, and Compose's default is, every interface). The picker, the parser and the YAML library are
compiled in only with `VITE_DEV_USERS=1` (checked: a build without it has no `devusers` chunk and
no mention of the file).

**Tests:** 388 vitest tests in 13 files (written failing in Phase B, 386 of them; none changed in
Phase C), 2 new Go tests, 3 Playwright tests. Not changed: any existing test.

**Numbers:** `go test -race -count=1 ./...` green. Scenario, two runs in a row on a fresh stack:
41.5 s (it builds pli first) and 29.6 s. Smoke test: 3 tests in 3.5 s, twice. The site: 397 kB of
JavaScript (120 kB gzipped), plus 97 kB for the dev-user chunk.

**Surprises:** the Type says less than a form wants (no labels, no `required` on a field), and the
UI is fine without: IDs as labels, and the server's refusal instead of a red asterisk. A generic
Item page offers every link an Item could receive, so a story shows nine "add" rows and six
boards: correct, and noisy. The inbox row leaves the list when its Item moves on, so the answer
has to live on the page, not in the row. The ticket's enrich hooks write events in their own time:
a test that counts history rows is a flake. TypeScript 7 (the native compiler) worked with Vite 8
and vitest 5 with no change. Unranked cards are shown in the board's list with a "not ranked"
badge, not in a group of their own as Phase A said: one list keeps a drop's arithmetic the same.

**Not verified here:** the web image's build stage (`npm ci` and `vite build` inside Docker).
This sandbox cannot reach npm from inside a build, so the stack was run with the site built on the
host and copied into the same nginx stage; CI's `scenario` job is the first real run of
`web/Dockerfile`.

**Open:** 8233 and 8085 were still published on every interface when this entry was written; a
later commit of the same pull request bound them, and compose.dev.yaml's two ports, to 127.0.0.1.
The six "Board:" links
per story suggest a Type could say which links make a board; that is a Type attribute, not built.
**Next session:** unchanged: the SLA design pass (paper only), then Jira part 2 (the IT service
desk).
