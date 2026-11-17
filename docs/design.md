# Work kernel — design summary for the spike

This is the settled design, condensed from the design docs ("Work kernel: …" in
my Claude artifacts). Treat it as the spec. Raise questions; don't silently
deviate.

## The idea

Every unit of work is an **Item** of a **Type**. Trackers (Jira-like), chat
(Slack-like) and a helpdesk will be apps built on the kernel. The kernel stays
small; everything product-specific is a Type or an app.

## Names: kernel and item service

- **Kernel**: the whole core layer, everything that is not an app. Today that
  is the item service, `internal/lifecycle`, the relay (`cmd/relay`) and the
  workflow worker (`cmd/worker`); later the fan-out. Several programs, one layer.
- **Item service**: the one program that serves reads and writes of Items
  (`ItemService` in the proto). It is the only door to Item state.
- Say "the item service" for the program and "the kernel" for the layer.
  In code: `cmd/itemservice` and `internal/itemservice` (type `Service`).

## Item

Seven parts:

| Part | Meaning |
| --- | --- |
| Identity | id, namespace, Type id and version (pinned at creation) |
| Fields | values validated against the Type's schema, keyed by stable field IDs |
| State | current lifecycle state; every state has a category |
| Links | typed edges to other Items (blocks, relates…) |
| Container | at most one parent Item (e.g. a Space contains tickets; a ticket contains comments) |
| Policy | read/write rules via scopes and grants (below) |
| History | append-only events: every accepted operation |

Five write operations: **create, update, transition, link, archive**. Each is
validated (schema, lifecycle, policy), then either accepted and recorded, or
rejected with a reason.

## Kernel rules for size

- **Pay for what you use.** An Item that keeps every default (no lifecycle,
  no links, inherited policy) costs one row and one event, and gets no outbox
  row when nothing listens. A Type without a lifecycle has one implicit state.
- **Membership test.** Something joins the kernel only if all three reference
  apps need it and it must behave the same way everywhere (searched,
  permissioned or audited uniformly). Assignee, SLA, approvals and comments
  stay in Types or user space.

## Type

A Type is versioned YAML data that **names code but never contains it**.

- Fields: stable ID, type, options, defaults, invariants as expressions.
  Types: `string`, `int`, `bool`, `enum`, `principal`, `date` (an ISO 8601
  calendar date, `2026-12-01`). A field may be `writable_by: kernel`: only the
  internal door (a hook, or another Type's reaction it accepts) writes it
  (see "Approval as a business state"), or `writable_by: [role:<name>, ...]`:
  on the public door only holders of a role write it (see "Link reactions,
  the leave balance and the saga"). A Type may declare `invariants`, rules
  over its fields that every write keeps, and `reactions`, writes on the
  Items it links to.
- States: each has a category — `triage, backlog, todo, active, waiting, done, canceled, none`.
  `none` means "not work": work views leave it out. A Type that declares no
  states gets one implicit state, `default`, with category `none`. A state may
  declare `frozen: [fields]`: no write changes them while the Item is in it.
- Transitions: from/to, guard expression, required fields, who may act, action references.
  - Who may act is the transition's `allowed` list. An entry is `role:<name>`,
    `principal:<id>`, `field:<id>` (whoever a principal field holds when the
    move runs) or `creator` (the requester: see below). Absent or empty means
    anyone who can see the Item.
  - Who may not act is its `except` list. It takes only `creator` for now,
    and there it means anyone who took part in creating the Item. A match is
    `ErrForbidden`, asked with `allowed`, after the guards.
  - **Identity rules are declared, never written in CEL**, so the
    effective-user logic lives in one place (package `policy`); guards keep
    data rules (dates, days).
  - A timer acts as `system:timer` and gets no silent exemption: if the
    transition it uses has an `allowed` list, the list must include
    `principal:system:timer`, or the Type is refused at load.
- Timers/SLAs: separate objects with start/pause/resume/stop conditions (later).
  Spike stand-in: a state may carry one timer,
  `{id: in_progress, category: active, timer: {after: 48h, to: open}}`. `to` must
  be a transition the Type declares from that state, so a timer can never make a
  move a person couldn't. The timer counts from the event that entered the state.
- Approvals: a business state, decided by an ordinary transition; the kernel
  has no approval concept (see "Approval as a business state").
- Items stay on the Type version they were created with. Versioning rule:
  **pin the lifecycle, evolve the schema, float the policy.**

Guards are CEL expressions (`cel-go`), compiled when a Type loads. Every field
is a variable named by its ID, typed by the schema and nullable: unset is
`null`, so "is set" reads `f_x != null`. A guard that cannot be evaluated does
not hold. A guard reads links as `links.<name>` (see "Links, containment and
people"). A date field is a CEL timestamp at 00:00 UTC, and `today` is the
date the write happens on, **in UTC** (the item service's clock; a user's
local date can differ: Later). Who acts is not a guard variable: `caller`,
`caller_on_behalf_of`, `created_by`, `created_on_behalf_of`, `today` and
`links` are reserved names no field may take.

Hooks (Step 6) are declared in the Type version, pinned with the lifecycle:
`{id, on: enter, state}` for an **enrich** hook (reads some fields, calls out,
writes others, never holds the Item, optional `fallback`), or `{id, on:
transition, from, to}` for a **check**. Each names an action (code in
`internal/hooks`, never in the Type) with `reads` and `writes`, matched to the
action by position; the action's arity is checked at load. Besides fields,
`reads` may name the kernel-set attributes `created_by` and
`created_on_behalf_of`. A transition may be an `escape` (cancel): it may
leave a held state.

An **action** hook (Step 7) is `{id, on: enter, state, kind: action}`: a side
effect outside the kernel (an invoice in the ERP). It writes at least one
field (what it made) and has no fallback. Its idempotency key is one per
entry into the state, `hook:<item>:<entered seq>:<hook>`, and the action
passes it to the connector itself, so a retry gets what the first attempt
made. Each entry is its own business event: re-entering the state runs it
again under a new key. Once its state is entered it runs to completion, even
if the Item has moved on (enrich hooks of a state already left may be
dropped; actions never are). If it cannot complete, it writes `hook_failed`
(below).

## Write path

```
client → item service (Go, stateless)
           check the request is well formed
           BEGIN
             claim idempotency key (namespace, actor, key)
               already used → return the stored result, write nothing
             lock Item row (SELECT … FOR UPDATE)
             check the principal can see the Item (else: not found)
             validate schema + lifecycle + policy against the locked row
             write Item row (item_seq + 1)
             take commit_seq from a sequence (no lock)
             insert item_events (item_id, item_seq, commit_seq, xid, attribution…, op, payload)
               xid = pg_current_xact_id(), the writing transaction's ID
             insert item_event_values (field values for this event)
             insert outbox (item_id, item_seq)
             store the result under the idempotency key
           COMMIT
         → returns { item_id, commit_seq, item_seq }
```

- Same idempotency key again → return the stored result, write nothing. Keys
  are scoped by `(namespace_id, actor, key)`, so two clients never collide.
  A key reused with a different request → `ErrIdempotencyKeyReuse`.
- Validation runs inside the transaction because it depends on the Item's
  current state and fields, which are only reliable under the row lock.
- A rejected write rolls back, so it leaves no event and does not use up its
  key: the client can fix the Item and retry with the same key.
- `Update` and `Transition` take an optional `expected_item_seq` (an if-match,
  checked under the row lock; zero means no check). The refusals are distinct:
  - `ErrNotFound` — no such Item, or the principal may not see it (HTTP 404).
    The two cases look the same on purpose, on reads and on writes, so an
    Item's existence never leaks. It is checked before everything else.
  - `ErrRejected` — the rules say no; retrying is pointless (HTTP 422).
  - `ErrForbidden` — the move is legal and the principal can see the Item, but
    the transition's `allowed` list does not include them (HTTP 403). The
    lifecycle is asked first, so an illegal move is always `ErrRejected`.
  - `ErrStale` — someone changed the Item first; re-read and retry (HTTP 409).
- `commit_seq` names a write so that a later read can wait for it (`min_seq`).
  It comes from a plain sequence, so writes no longer queue on one row. Sequence
  numbers are taken in one order and committed in another, and a rolled-back
  write leaves a gap, so `commit_seq` is **not** a cursor: "everything up to S"
  cannot be read off it.
- **What `min_seq` guarantees.** Every event stores the ID of the transaction
  that wrote it. A transaction ID below `pg_snapshot_xmin(pg_current_snapshot())`
  belongs to a transaction that has finished; no later commit can carry it. So a
  read with `min_seq = S` looks up the event S's transaction ID and waits until
  the reader's watermark has passed it. An S whose event cannot be found yet
  (still committing, or rolled back) is never treated as reached: the read keeps
  waiting up to its bound, then returns `ErrNotCaughtUp`. Then the write S is visible, together with
  everything written by older transactions. It does **not** promise every event
  with a lower `commit_seq`: a younger transaction may have taken a lower number
  and still be running. That is enough for read-your-writes, which is what
  `min_seq` is for.
- **What that rules out.** Watch and any external index need "nothing before
  this point is missing", and `commit_seq` cannot give it. They will read from a
  **post-commit sequencer**: one background process that reads events whose
  transaction ID is below xmin, orders them, and stamps a gap-free log position.
  Watch and indexers use that position as their cursor. Not built yet.
- The relay does not need the watermark. It delivers per Item in `item_seq`
  order, and the Item's row lock means write n+1 cannot start before write n has
  committed, so it never sees a gap within an Item.
- **A long transaction holds the watermark back for the whole database**, so
  every `min_seq` wait would time out. The write path runs with a statement
  timeout, and the database with `idle_in_transaction_session_timeout`.

### Events and personal data

- `item_events.payload` holds the operation's shape (type, state, field IDs,
  links), never field values.
- Field values for each event go in `item_event_values`, keyed by
  `(item_id, item_seq)`. History, audit and replay read both tables.
- To erase personal data, delete or redact rows in `item_event_values`; the event
  log itself stays append-only and intact. Per-subject encryption
  (crypto-shredding) can replace this later without changing the event shape.

## Durable behavior (Temporal)

- Most Items never get a workflow. A workflow starts **lazily** when an Item
  enters a state that needs timers or waits.
- **Relay** (`cmd/relay`, single instance): reads undelivered outbox rows and
  delivers each Item's events **in item_seq order**, one at a time, with update
  ID = `item:seq` (a redelivery reuses it). It sends the event's shape only,
  never field values.
  - The Type decides how: one function, `lifecycle.NeedsWorkflow`, answers "does
    this event enter a state with behavior?" (a timer today). Yes →
    **Update-With-Start** (workflow ID = item ID, conflict policy USE_EXISTING).
    No → a plain Update, which reaches a workflow only if one already runs.
  - It marks the row delivered once the update has **completed**, not when it is
    accepted: an accepted update can still fail.
  - **Nothing listening**: a plain Update answered `NotFound`. The server gives
    that answer both for a workflow that never existed and for one that has
    completed. Either way the row is marked delivered.
  - Each delivery is bounded at 10 s. A failing Item backs off; the others carry on.
- **Workflow** (`internal/workflow`, run by `cmd/worker`): keeps
  `lastAppliedSeq`. Every new run first loads the Item's state, `item_seq` and
  a small timer table from Postgres, sets `lastAppliedSeq = item_seq`, and never
  sees the Type itself. Duplicate (at or below `lastAppliedSeq`) → accept and
  ignore. Gap → refused by the update's validator, so the relay retries from the
  lowest undelivered seq and the refusal is not written to history. Continue-as-new
  carries the loaded state. A run completes when the Item's state has no behavior.
- The workflow is a thin scheduler of timers and waits. It uses pure functions
  from `internal/lifecycle` (`TimerFor`); guards stay in the item service. Timer-driven
  transitions go back through the item service's write path (as an activity) with
  `expected_item_seq`, so history stays in one place and a stale timer cannot
  make a wrong move. Stale, rejected, "Item not found" and invalid (a malformed
  kernel write, which retrying cannot fix) are final; any other
  failure retries with backoff, without limit.

## Holds (Step 6)

- Asking for a transition that has a check does not move the Item. Under the
  row lock the requester is checked (visibility, guards, required fields,
  `allowed`); then one transaction writes a `transition_requested` event
  (payload `from`, `requested_to`, `hold_id`; no `to`) and an `item_holds` row.
- A field patch sent with a held transition is written at once, with the
  request, and stays if the move is denied or fails. This differs from an
  unchecked transition, where the patch and the move are one write and stand
  or fall together. The guards are checked against the patched fields at the
  request and again at resolution.
- While the hold is open, the fields the check reads are frozen, and only a
  declared escape leaves the state (it cancels the hold). Anything else is
  `ErrHeld`, an `ErrStale`, naming the hold. A timer refused as held is spent.
- The check runs as an activity and calls `ResolveHold` under the key
  `hold:<id>`: **allow** and **correct** (allow with the hook's patch) apply the
  requester's transition after checking the requester again (still sees the
  Item, still in `allowed`, guards hold on the current fields), else
  `transition_denied` with reason `requester_lost_access`, `forbidden` or
  `guard_failed`; **deny** writes `transition_denied`; **error** writes
  `transition_failed`. Reasons are codes, never free text. The resolving event
  records the hook as actor and the root human as `on_behalf_of`.
- Each hook has a budget (ScheduleToClose, default 15 min). When it runs out,
  an enrich hook writes its fallback (if any) and a check resolves as error.
- `Transition` takes a bounded `wait` (0 to 5 s, not part of the idempotency
  hash) and answers `applied`, `denied`, `pending` or `error` with the hold ID.
  A retry with the same key reports the hold's current outcome. `Get` shows
  the Item's latest hold.
- Variant D: an enrich hook prefetches a fact (`on_hold`) into a field and a
  CEL guard refuses at once on it; the check is the backstop for a stale or
  missing prefetch. Enrich hooks of a state already left are skipped.
- **Action hooks** (Step 7). An action that gives up writes `hook_failed`
  through the internal door (payload: the hook and a reason code,
  `budget_exhausted`, `key_conflict` or `action_failed`; no field or state
  change), under its own key `hook_failed:<item>:<entered seq>:<hook>`. Only
  an action hook may write it. Its write-back's key coming back with a
  different request means the connector made a second, different thing for
  one key: a duplicate, recorded as `key_conflict`, never treated as already
  written (an enrich hook's fallback shares its key, so there it is). An
  action is complete for an entry once an event names the hook as
  `originating_hook` and the entry as `causation_id` (its write-back or
  `hook_failed`); `LoadItem` reports every action not complete, for any
  entry, not just the current one.

## Policy

- **Principals are typed IDs**, given by the caller: `user:<id>`, `agent:<id>`,
  `group:<id>`, and for the kernel's own actors `system:<id>` (`system:timer`)
  and `hook:<type>:<hook-id>` (a Type's hook), and since Step 2, part 2
  `reaction:<type>:<id>` (a Type's link reaction).
  The actor, `on_behalf_of`, every reader and every role holder is one. An
  untyped ID is `ErrInvalid`. Only users and agents are readers today; `group:`
  is reserved and refused until groups exist, so groups need no migration later.
  In a Type's `allowed` list a principal entry reads `principal:user:<id>`. It
  may name a hook of the same Type, `principal:hook:<type>:<hook-id>`; another
  Type's hook is refused at load, since a hook acts only on its own Type.
- **Two doors.** The item service's public operations (`Create`, `Update`,
  `Transition`, `Get`, `Search`, `StartAgentSession`) serve people and agents.
  They refuse a `system:` or `hook:` actor, and any `originating_hook`,
  `causation_id` or non-zero `hop`, with `ErrInvalid`; `correlation_id` stays
  open to callers, as a claim. The kernel's own actors write through
  `Service.Internal()` (`Update` and `Transition` only):
  - It serves `Update`, `Transition`, `ResolveHold` (a check settles its hold),
    `HookFailed` (an action hook gives up) and `HookInput` (a hook reads its
    declared `reads`, nothing else).
  - It accepts `system:timer` and `hook:<type>:<hook-id>` only; any other
    `system:` actor is `ErrInvalid` until it is defined. It refuses people,
    agents, an `on_behalf_of` set by the caller and any agent session.
  - On a Type that declares hooks, a hook must be declared and writes only
    its `writes`. No kernel actor makes a checked transition itself
    (`ErrForbidden`): only `ResolveHold` applies it, for a requester.
  - A write names its cause: `originating_hook` (for the timer
    `timer:<type>@v<version>:<state>`, for a hook
    `hook:<type>@v<version>:<hook-id>` naming that hook; either way on the
    Item's own Type and version) and `causation_id` = an existing event of **the same Item**,
    with `hop` ≥ 1. (Link reactions will write to another Item; this rule
    relaxes then.)
  - `hop` above `MaxHop` (8) is `ErrRejected` ("hop limit") and writes nothing.
  - It changes fields and state only, never policy or container.
  - It skips visibility (see Boundary) and still applies `allowed`.
  - Only the worker and the hook runner may call `Internal()`. Go cannot limit
    an exported method to some packages of a module, so a test reads the
    module's source and fails on any other caller.
- **Scopes**: every container has a scope node, created with the container. It
  inherits (`narrows = false`) until the container is narrowed, which then flips
  the node's flag and readers in place. An Item's read access comes from its
  nearest ancestor scope that narrows it. Each Item stores one `scope_id`, never
  null: its own node if it is a container or narrowed, else its container's node.
- **Root scope**: each namespace has one root scope (no parent, `narrows = false`)
  that every principal in the namespace reads. An Item that is not narrowed, in
  no container, is in it. There is no "null means everyone".
- A child can **narrow** its container's access but never widen it by inheritance.
- **Narrowing is a change to the Item's policy part**, made through the existing
  operations. `Create` takes an optional `Narrow{readers}`, which gives the Item
  a scope node under its container's scope. `Update` takes an optional policy
  patch: new `readers`, or `inherit` to drop the Item's own node. Both write an
  event and bump `item_seq`; the payload holds reader IDs only.
  - A change that would hide the Item from the principal making it (or from
    `on_behalf_of`) is `ErrInvalid`. Recovery by an admin comes later, with grants.
  - Narrowing a container changes only its own node; no descendant row changes.
- **Only narrowing creates a closed scope.** Sharing a single Item with someone
  outside its readers will be an additive grant row, never a new scope.
- **A narrowed child is invisible to its parent's readers** unless they are its
  readers too. There is no "locked" placeholder in the parent; a discoverable
  flag may come later.
- **Roles** live in `principal_roles(namespace_id, principal, role)`: the
  server's own record, read on every write that needs it, never claimed by a caller.
- **On behalf of**: when an agent acts for a user, both must see the Item and
  both must pass `allowed`: every `role:`, `principal:` and `field:` entry is
  judged for the agent and for its user. **Except `creator`**, which is judged
  once for the principal as a whole (an agent acting for the requester is the
  requester). A `field:` entry holding `user:bob` never matches `agent:x`, so
  an agent acting for an approver cannot approve.
- **Grants** widen explicitly (e.g. share one ticket with a customer). A grant
  attaches to a scope node. Effective readers = (parent readers narrowed by this
  node) + grants.
- **When a scope node appears.** An Item gets its own node when it is narrowed,
  or when it gets its first child (in that child's `Create`). A message that never
  has children costs no node.
- **Who can see what is kept, not computed per request.** Every scope has a
  pointer, `policy_scope_id`: the nearest narrowing scope at or above it, or null
  when nothing above it narrows (everyone in the namespace reads it). Only
  narrowing scopes have rows in `scope_access(scope_id, principal)`: their
  effective readers, the readers of every narrowing scope on the way up,
  intersected. An inheriting scope holds no rows of its own; it points at the
  scope whose rows apply. Pointers and rows are updated in the same transaction
  as the reader or policy change that affects them, so a narrowed Item or a
  removed reader is hidden on the very next read. A principal pays only for the
  scopes they are in; other people's narrowed scopes cost nothing.
- **Why a pointer** (Step A measurement): with a copy of the readers on every
  inheriting scope, narrowing a container with 1,000 descendant scopes to 50
  readers wrote 50,050 rows (0.8 s), and so did every reader change after it.
  With the pointer, narrowing writes R rows plus one pointer per inheriting
  scope down to the next narrowing scope; a reader change writes R rows plus a
  recompute of each nested narrowing scope below; un-narrowing moves the
  pointers back.
- **Get and writes** check the Item's scope: `policy_scope_id` is null, or
  `scope_access` has a row for (`policy_scope_id`, principal), and for
  `on_behalf_of` when set. One indexed lookup, with no query over other scopes.
- **Search** filters with the same test, joined in SQL.
- Walking down from a scope is what a policy change does to move pointers and
  recompute nested narrowing scopes: O(scopes below the change), touching one
  column per inheriting scope and R rows per narrowing one.
- **Concurrent policy changes.** A recompute first locks the affected scope rows
  top-down, ancestor before descendant, and only then rewrites `scope_access`.
  Two changes in the same subtree therefore run one after the other, in the same
  lock order, and cannot leave the table wrong or deadlock.
- **Groups (later).** Readers may become groups. `scope_access` then holds group
  IDs (already a valid typed principal), and the check becomes "the principal, or
  one of the principal's groups", so the table stays O(narrowing scopes × reader
  entries), not O(scopes × people).
- API in `internal/policy`:
  - `Project(principal, visible, item)` — pure. All-or-nothing per Item for the
    spike; redacting single fields comes later.
  - `AuthorizeTransition(principal, roles, transition)` — pure. "Is the move
    legal" stays in `lifecycle.Transition`; "may this principal make it" is
    asked here, afterwards.
  - A general `Authorize(principal, action, item)` comes later.
- Policy data lives in Postgres; evaluation is in Go. No external authz service.
- **Boundary.** `Project` applies to every path that reaches a principal: people,
  agents, connectors, webhooks, search results. Internal infrastructure that
  only receives event shapes without field values (relay → our Temporal) is not
  a principal, so those payloads do not pass through `Project`. For the same
  reason a timer's write is not subject to visibility; it is subject to `allowed`.
- **One door, enforced.** Every exported read of the item service calls
  `policy.Project`; a test parses the package and fails if a new read does not.
  `StateEntry` and `Type` are the only exemptions: they feed the workflow, not a
  principal. Any count, facet or total is computed after `Project`, never before.
- **History on the way out.** Event payloads keep opaque container IDs
  (`container_id`, `container_from`, `container_to`) for audit and replay.
  Anything that later exposes history or Watch to a principal passes each such
  ID through `Project` and redacts what that reader cannot see.
- **Moves are a change to the Item's container part,** made through `Update` (a
  `Move` patch: a destination, or none for the top level). One `Update`, one
  event, whose payload names `container_from` and `container_to`.
  - The mover must see the Item and the destination (and pass any `allowed`
    rule); a destination they cannot see is `ErrNotFound`.
  - A move into the Item itself or anything below it is `ErrInvalid`.
  - A move that widens who can see any scope in the moved subtree (effective
    readers before vs after) needs `ConfirmWiden`; otherwise `ErrRejected`
    (`widen_not_confirmed`; over the API `FAILED_PRECONDITION` / `WIDEN_NOT_CONFIRMED`):
    the request is valid, and the client asks the user and resends it confirmed.
  - Locks: the Item rows involved in ID order, then scopes ancestor-first.
    Pointers below the moved Item are recomputed; no descendant Item row changes.
  - Moves also take one namespace-wide move lock (a transaction advisory lock).
    Row locks alone cannot stop a cycle made by moves on disjoint pairs (A into
    B and C into D, then B into C and D into A, each pair checked before the
    others commit). Moves are rare, so serializing them costs little.
- **The agent write rule.** Every read by an agent records, per agent session,
  each narrowing scope it read (`agent_session_reads`); open scopes restrict
  nothing. A write by the agent must land at or below every recorded scope.
  Otherwise it is `ErrNeedsApproval`, naming the target scope. Writes are field
  updates, transitions with fields, creates (target: the container's scope, or
  the root), moves (target: the destination's scope), policy changes that
  widen readers, `Inherit` included, and links added or removed (targets:
  both ends' scopes, the source first). A plain transition puts no content anywhere.
  - Approval is a separate act by the human, not a flag the agent could set:
    `ApproveWiden(user, session, scope)` records an audited row in
    `agent_session_approvals` for that one scope. It counts only when the
    approver is the `on_behalf_of` user of the agent's request, and the approver
    must see the scope (else `ErrNotFound`). The agent's retry then succeeds.
  - `ErrNeedsApproval` names the **Item the write lands at**: the Item
    written, the container created in, or a move's destination (none: the
    top level). Approving that Item approves the scope its content lands in:
    its own node if it has one, else the scope it inherits. A container with
    no children yet has no node, so its content lands in its container's
    scope; naming the scope's owner would name the wrong thing.
  - **Approving an Item approves its audience**: everyone who reads that
    scope, which for an open scope is the whole namespace. So the refusal
    also says whether the scope is open (`audience`: `namespace`) or
    narrowed (`restricted`), and so does `ApprovalAudience`, which checks an
    approval without recording it. A client shows the audience before the
    user approves.
  - `ApproveWiden` is a write like any other: it takes a required
    idempotency key; the same key and request again records nothing more;
    the key with another request is `ErrIdempotencyKeyReuse`. A session that
    does not exist, or is in another namespace than the approver's, is
    `ErrNotFound`.
  - An `agent:` actor without a session is `ErrInvalid`, on reads and writes.
  - **Sessions are minted by the server**, never chosen by the caller:
    `StartAgentSession(agent, on_behalf_of, scopes, powers, grant)` returns
    a session ID and stores `agent_sessions(session, agent, on_behalf_of,
    namespace_id, scopes, powers, created_at)`. A session belongs to one
    namespace (see "Who is calling" for scopes, powers and grants). Every agent read
    and write must use a session that exists and belongs to that agent and that
    `on_behalf_of` user; otherwise `ErrInvalid`, and nothing is recorded or
    written. Without this, an agent could escape the rule with a fresh session.
  - A policy change that widens readers needs approval whenever the session has
    recorded any narrowing read: the content it exposes may be what was read.
  - An approval by anyone other than the session's user is recorded but never
    counts.
  - `agent_session_reads.scope_id` has no foreign key: a recorded scope may be
    removed later (`Inherit` on an Item without children). The record stays, no
    target is below it any more, and every write in that session then needs
    approval. Conservative, never silently looser.

## Search

- `Search` matches the string values in an Item's fields, using Postgres
  full-text search on a generated `tsvector` column. The column is part of the
  row, so it cannot fall behind a write.
- The visibility filter (see Policy) is applied in SQL before the limit, never
  after fetching.
- The visibility test is "the scope's `policy_scope_id` is null, or the
  principal is in `scope_access` for it". If the `OR` spoils the plan, it becomes two branches joined
  with `UNION ALL` (open scopes; the principal's scopes), checked with `EXPLAIN`
  on fresh and analyzed databases.
- The text match runs first and the sort and limit after it, in a subquery the
  planner cannot flatten. Otherwise Postgres walks Items newest-first and
  discards non-matches, which grows with the size of the scope (Step A, `EXPLAIN`).
- `min_seq`: Search waits until the watermark has passed the transaction that
  wrote `min_seq` (see Write path), up to a bound (default 2 s), then returns the
  retryable `ErrNotCaughtUp`. On one Postgres the wait ends at once; the contract
  is there for when search has its own index.

## Agents (later in the spike)

- An agent is its own principal with a human sponsor. Its principal carries the
  agent session (`Principal.AgentSession`), on reads and writes alike.
- Acting for a user: rights = user's rights ∩ agent's grant.
- Every event records attribution:
  - `actor`: whose permissions were checked (user, agent, `system:timer`, or
    `hook:<type>:<hook-id>`, with no version, so `allowed` matches across versions)
  - `on_behalf_of`: the human at the root, if different. On a kernel write
    (timer, hook) the kernel fills it from the cause: the cause's
    `on_behalf_of` if set, else the cause's actor if it is a `user:`, else
    empty. A timer or hook triggered by an agent working for Ana records Ana.
    It is attribution only, never checked for visibility or `allowed`: a hook
    must not fail because that human later lost access.
  - `agent_session`: set when an agent acts
  - `originating_hook`: set when a hook or connector caused the write
  - `causation_id`: the event that caused this write
  - `correlation_id`: groups a whole chain
  - `hop`: depth in the chain

  Build the columns in Step 2; callers fill them as they appear. Since Step 3,
  write requests carry them (`agent_session`, `originating_hook`, `causation_id`,
  `correlation_id`, `hop`) as a Go-only field, not yet in the API. Since Step 6,
  only the internal door may set `originating_hook`, `causation_id` and `hop`
  (see Policy, "Two doors").

  A timer's write is recorded as: `actor` = `system:timer`, `originating_hook` =
  `timer:<type>@v<version>:<state>`, `causation_id` = the `item:seq` of the event
  that entered the state, `correlation_id` inherited from that event, `hop` = that
  event's hop + 1. Its idempotency key is `timer:<item>:<entered seq>:<expected seq>`.
  It writes through `Internal()`.

  A hook's write is recorded the same way: `actor` = `hook:<type>:<hook-id>`,
  `originating_hook` = `hook:<type>@v<version>:<hook-id>`, `causation_id` = the
  event that triggered it, `hop` = that event's hop + 1, and `on_behalf_of` as
  above.

## Schema (first cut)

```sql
create table types        (id text, version int, definition jsonb, primary key (id, version));
create table scopes       (id uuid primary key, namespace_id text not null default 'default',
                           parent_id uuid references scopes, narrows boolean not null,
                           item_id uuid unique references items,      -- the Item this node belongs to
                           policy_scope_id uuid references scopes);   -- nearest narrowing scope; null = open
create table scope_access (scope_id uuid references scopes, principal text,  -- typed: user:…, agent:…
                           primary key (scope_id, principal));  -- effective readers of narrowing scopes only
create unique index scopes_root on scopes (namespace_id) where parent_id is null;
create index scopes_parent on scopes (parent_id);
create table scope_readers(scope_id uuid references scopes, principal text, primary key (scope_id, principal));
create table grants       (scope_id uuid references scopes, principal text, granted_by text,
                           created_at timestamptz not null default now(), primary key (scope_id, principal));
create table agent_sessions (session text primary key, agent text not null, on_behalf_of text,
                           created_at timestamptz not null default now());
create table agent_session_reads (session text references agent_sessions, scope_id uuid,  -- no FK: see Policy
                           primary key (session, scope_id));   -- narrowing scopes an agent session read
create table agent_session_approvals (session text references agent_sessions, scope_id uuid, approved_by text,
                           created_at timestamptz not null default now(),
                           primary key (session, scope_id, approved_by));
create table principal_roles (namespace_id text not null default 'default', principal text not null,
                           role text not null, primary key (namespace_id, principal, role));
create table items        (id uuid primary key, namespace_id text not null default 'default',
                           type_id text not null, type_version int not null,
                           container_id uuid references items, scope_id uuid not null references scopes,
                           state text not null, fields jsonb not null default '{}',
                           item_seq bigint not null default 0, updated_at timestamptz not null default now(),
                           search tsvector generated always as
                             (jsonb_to_tsvector('simple', fields, '["string"]')) stored);
create index items_search on items using gin (search);
create index items_namespace_scope on items (namespace_id, scope_id);
create table item_events  (item_id uuid references items, item_seq bigint,
                           commit_seq bigint not null unique default nextval('commit_seq'),
                           xid xid8 not null default pg_current_xact_id(),
                           actor text not null, on_behalf_of text, agent_session text,
                           originating_hook text, causation_id text, correlation_id text,
                           hop int not null default 0, op text not null, payload jsonb,
                           created_at timestamptz not null default now(), primary key (item_id, item_seq));
create table item_event_values (item_id uuid, item_seq bigint, field_values jsonb not null,
                           primary key (item_id, item_seq),
                           foreign key (item_id, item_seq) references item_events);
create table outbox       (item_id uuid, item_seq bigint, delivered_at timestamptz,
                           primary key (item_id, item_seq));
create index outbox_pending on outbox (item_id, item_seq) where delivered_at is null;
create index items_container on items (container_id);
create table idempotency  (namespace_id text not null default 'default', actor text not null, key text not null,
                           request_hash text not null, result jsonb not null,
                           created_at timestamptz not null default now(),
                           primary key (namespace_id, actor, key));
create table item_holds   (id uuid primary key, item_id uuid not null references items, hook text not null,
                           from_state text not null, to_state text not null, requested_seq bigint not null,
                           requester text not null, requester_on_behalf_of text, requester_agent_session text,
                           status text not null default 'open',  -- open, allowed, denied, error, canceled
                           reason text, resolved_seq bigint, created_at timestamptz not null default now(),
                           resolved_at timestamptz, foreign key (item_id, requested_seq) references item_events);
create unique index item_holds_one_open on item_holds (item_id) where status = 'open';
create table links        (namespace_id text not null, source_id uuid not null references items,
                           source_type_id text not null, link_type text not null,  -- the declared name
                           target_id uuid not null references items,
                           src_single boolean not null, dst_single boolean not null,  -- snapshots of the source Type
                           created_at timestamptz not null default now(),
                           primary key (source_id, link_type, target_id));
create unique index links_src_single on links (source_id, link_type) where src_single;
create unique index links_dst_single on links (target_id, source_type_id, link_type) where dst_single;
create index links_target on links (target_id, link_type, source_id);
create index links_source_order on links (source_id, created_at, link_type, target_id);
create index links_target_order on links (target_id, created_at, link_type, source_id);
```

## Public API (v1)

Designed and reviewed 2026-11-15 ("Work kernel: Public API" in the design
docs). This section is the spec for step 0 of the proof of concept. The proto
in `proto/kernel/v1` is the first cut from before the spike and is replaced to
match.

**Principles.** One contract for every client (the `pli` CLI first, then UIs,
bots, agents through an MCP server, other people's apps); nothing reaches Items
any other way, and the internal door never goes over the network. Served with
connect-go, so every operation answers gRPC, gRPC-Web and JSON over HTTP.
The item service also answers `GET /healthz` (200 while its database
answers, else 503, with no body) for container health checks; it is
outside the API contract: not in the proto, not versioned.
Follow the published conventions: Google's API design guides (AIPs) for
resources, paging, errors; Stripe's model for idempotency keys.

**No generic action call.** Every operation that isn't "change fields" or
"move state" gets its own named, typed call (`ApproveWiden`, `RankItem`), never
a catch-all `Action(item, name, json)` (Unix's `ioctl` is the warning). Each
new call passes the kernel membership test.

### Who is calling

- A bearer token on every call; an interceptor turns it into the principal and
  puts it in the request context. **No request message carries a principal.**
  `on_behalf_of` is never a request field either.
- Proof of concept: a config file maps dev tokens to principals
  (`ana -> user:ana`); `pli --as ana` picks a token. The server refuses
  dev-token mode unless started with an explicit flag, and warns on every
  start. Real sign-on (OIDC) later replaces only the interceptor.
  **Roles from the dev-token file** (Step 0, part 3): an entry may name the
  roles its principal holds in its namespace (`roles: [approver]`, bare
  names). In dev-token mode only, the server adds them at start
  (`EnsureRoles`, one transaction), logging each one it adds; it never
  removes a role, and warns about roles of the file's namespaces that the
  file no longer names. Real sign-on brings roles from elsewhere.
- Agents (in practice an LLM with tools) start sessions in **capability mode**:
  `StartAgentSession` takes the agent's token, the user it acts for
  (`acting_for`: who must have granted the agent; empty for the agent
  itself), and **required** `scopes` (what it may touch) and `powers`. A
  session can only narrow these; sub-agents inherit them; reading untrusted
  text will drop outbound powers (with taint, later).
  - **Scopes** are container names, each with everything below it, or
    `namespaces/{ns}`, the whole namespace. Membership is checked at each
    request, walking containers as they are now, so a move changes what the
    session sees at once. Outside its scopes an Item does not exist for the
    session: `NOT_FOUND` on reads and writes alike, before any other check.
    The top level (creating there, moving there, listing it) needs the whole
    namespace.
  - **Powers**: `read` (always implied), `create`, `edit_fields`,
    `transition:<state>` (may move Items into that state; transitions have no
    IDs, so the power names the target; its format is checked, the state is
    not, since a session spans Types), `transition:*` (any), `move`,
    `comment` (reserved: stored only, will mean "create comment Items") and
    `outbound`. An unknown power is `INVALID_ARGUMENT`. Every power is
    stored; v1 enforces read versus any write power: without one, every
    write is `NOT_ALLOWED`.
  - **Grants**: what a user let an agent do, scopes and powers, per
    namespace; a config entry in the dev-token file for now. The API
    resolves the grant for (agent, user, namespace); the kernel checks the
    session fits inside it: every scope at or below a granted one, every
    power covered (`transition:*` covers every `transition:<state>`). No
    grant, or more than granted, is `NOT_ALLOWED`. A scope the agent or its
    user cannot see is `NOT_FOUND`.
  - **The session header.** Every later call carries the agent's token and
    the session ID in the `Pli-Agent-Session` header; the user the agent
    acts for comes from the session, never the request. The header with a
    user's token, or an agent's token without it (except to start a
    session), is `INVALID_ARGUMENT` naming the header in metadata. An unknown
    session, another agent's, or another namespace's is `UNAUTHENTICATED` /
    `SESSION_INVALID`: a bad credential.
- `ApproveWiden` and future approvals take the approver from the token. It
  takes a required `request_id`, like every write, and `validate_only`
  (AIP-163): check the approval and answer its `audience`, recording
  nothing. A real approval also takes **`expected_audience`**, required in
  the API and in the kernel (Step 0, part 3): the audience the approver was
  shown (`namespace` or `restricted`). The kernel reads the scope's audience
  in the approval's own transaction and commits only for that audience;
  otherwise `FAILED_PRECONDITION` / `AUDIENCE_CHANGED`, nothing recorded,
  the `request_id` unused. A replay answers from the stored result, with
  the audience its request committed for. `pli agent approve`, run as the
  user, says who will see the agent's content, sends that audience, and
  approves only after a "y" at a terminal or `--yes`;
  `--expect-audience` names the audience without asking the server first.
- Timers and hooks never use the network API.

### Resources and operations

Names carry the namespace from day one: `namespaces/{ns}/items/{item}`,
`namespaces/{ns}/types/{type}/versions/{n}`. Names stay flat: an Item's
container is a field, not part of its name, so a move never renames it.

| Operation | What it does |
| --- | --- |
| `CreateItem` | Create an Item of a Type, optionally in a container and narrowed |
| `GetItem` | One Item as the caller sees it; hidden and missing are the same answer |
| `UpdateItem` | A field patch, a readers change, or a move (one per call, as today) |
| `TransitionItem` | Move state, optionally with a patch and a `wait`; returns the outcome |
| `SearchItems` | Words and/or filters over what the caller can see; paged |
| `ListItems` | A container's direct children, same filters; paged |
| `ListItemEvents` | An Item's history, projected for the caller; paged |
| `LinkItems`, `UnlinkItems` | Add or remove a link of a type the source's Type declares |
| `GetType`, `ListTypes` | Read Type definitions (read-only; Types load from files at start) |
| `StartAgentSession`, `ApproveWiden` | The agent rules |

`Link` and `Archive` as they stand in the first-cut proto are removed until
built. Every write returns the Item after the write plus `item_seq` and
`commit_seq`. An Item carries its name, Type and version, container, state and
category, fields, `etag`, links (both directions), the open hold if any, create
and update times, and creator.

### Field values

Plain JSON values (`google.protobuf.Value`), converted and checked against the
field's type in the Type on the server: int64 and decimals as strings, dates
and timestamps as ISO 8601 strings, principals as `user:ana`, enums by name. A
mismatch is `INVALID_ARGUMENT` naming the field. A patch changes only the
fields it names; JSON `null` clears one. No field masks.

### Writes

- `request_id` (the idempotency key) is **required** on every write; same key
  and same request returns the stored result, same key and a different request
  is `ALREADY_EXISTS` / `IDEMPOTENCY_KEY_REUSED`. A request refused before any
  work uses no key.
- `etag` on any write: refused as `ABORTED` / `STALE` if the Item moved on.
  Today it is `item_seq` as a string; clients treat it as opaque.
- `TransitionItem` takes `wait` (default 0, at most 5 s) and answers `APPLIED`,
  `DENIED` (with a reason code), `ERROR` or `PENDING` with the hold's name. A
  denied held transition is a successful call, not an error.

### Reads

- Paging everywhere: `page_size` (default 50, max 500), `page_token`,
  `next_page_token`. Opaque tokens, no offsets, no exact totals.
- Paging is keyset on `(created_at desc, id desc)` (history: `item_seq`),
  never an offset. A page token is bound to its request (filters, words,
  container), its caller and its namespace: used with anything else it is
  `INVALID_ARGUMENT` (decided in Step 0, part 2).
- A filter with one value (one Type, one state) is `=` in SQL, which walks
  the index in order and reads one page. Several values are `= any(...)`,
  which on Postgres 17 sorts every match before the page (26-36 ms at
  30,000 matches): a known cost, in the journal's Later with its fixes.
- Filters on `SearchItems` and `ListItems`: Type, state, category, field
  equality and ranges, principal fields ("assigned to me"), links ("linked to
  X as `sprint`"). A filter alone is a valid search. Link conditions and "me"
  are described in "Links, containment and people" (Step 1).
- `min_seq` on search for read-your-writes; `UNAVAILABLE` / `NOT_CAUGHT_UP`
  after 2 s. `GetItem` always reads the latest state.
- Every read goes through `Project`. `Watch` comes later, after the sequencer.

### Errors

Every error has a standard code and an `ErrorInfo` with a stable reason
(AIP-193); metadata never names anything the caller can't see. The
ErrorInfo domain is `pli.francoisnyc.github.io`, **frozen** from Step 0,
part 3 (clients match on reason and domain; a test holds it). It is a
label, never resolved.

**Messages are fixed.** Each reason has one message, the same every time;
dynamic values go in ErrorInfo metadata, never in a message: `hold` (HELD),
`scope` and `audience` (NEEDS_APPROVAL), `audience` and `expected_audience`
(AUDIENCE_CHANGED), `from_state` and `to_state` (a transition rule's
refusal), `fields` (MISSING_FIELDS, comma-separated IDs), `header` (a
misused header). A field violation's description may name the
field, never a value or an ID. A test fails if a message contains an ID.
A rule's refusal answers with its code; every kernel rejection carries one
(a test walks the source), and one without a code is a bug: `INTERNAL`.

| Kernel refusal | Code | Reason |
| --- | --- | --- |
| Bad or missing token | `UNAUTHENTICATED` | `UNAUTHENTICATED` |
| Bad agent session (`ErrSessionInvalid`) | `UNAUTHENTICATED` | `SESSION_INVALID` |
| `ErrInvalid` | `INVALID_ARGUMENT` | `INVALID_REQUEST` (+ field violations) |
| `ErrNotFound` (hidden or missing) | `NOT_FOUND` | `NOT_FOUND` |
| `ErrForbidden` | `PERMISSION_DENIED` | `NOT_ALLOWED` |
| `ErrNeedsApproval` | `PERMISSION_DENIED` | `NEEDS_APPROVAL` (+ scope, audience) |
| `ErrRejected` | `FAILED_PRECONDITION` | the rule's code, e.g. `GUARD_FAILED` |
| `ErrRejected`, an approval's audience changed since shown | `FAILED_PRECONDITION` | `AUDIENCE_CHANGED` (+ audience, expected_audience) |
| `ErrRejected`, a max-one side of a link type already has its link | `FAILED_PRECONDITION` | `LINK_CARDINALITY` (+ link_type, side) |
| `ErrRejected`, the link exists / does not exist / its target's Type is not allowed | `FAILED_PRECONDITION` | `ALREADY_LINKED` / `NOT_LINKED` / `LINK_TARGET_TYPE` |
| `ErrForbidden`, a write names a field it may not write (kernel-written, or named by its transition's `allowed`) | `PERMISSION_DENIED` | `FIELD_NOT_WRITABLE` (+ fields) |
| `ErrRejected`, a write names a field the Item's state freezes | `FAILED_PRECONDITION` | `FIELD_FROZEN` (+ fields, state) |
| `ErrRejected`, a write would break one of the Type's invariants | `FAILED_PRECONDITION` | `INVARIANT_FAILED` (+ invariant) |
| `ErrForbidden`, LinkItems / UnlinkItems on a link only the kernel sets | `PERMISSION_DENIED` | `LINK_NOT_WRITABLE` (+ link_type) |
| `ErrRejected`, a direct transition into a state that sets a link found no single target | `FAILED_PRECONDITION` | `LINK_UNRESOLVED` (+ link_type, reason) |
| `ErrStale` | `ABORTED` | `STALE` |
| `ErrHeld` | `ABORTED` | `HELD` (+ hold name) |
| `ErrIdempotencyKeyReuse` | `ALREADY_EXISTS` | `IDEMPOTENCY_KEY_REUSED` |
| `ErrNotCaughtUp` | `UNAVAILABLE` | `NOT_CAUGHT_UP` |
| The caller canceled the call | `CANCELED` | `CANCELED` |
| The call ran out of time | `DEADLINE_EXCEEDED` | `DEADLINE_EXCEEDED` |
| anything else | `INTERNAL` | `INTERNAL` |

### Versioning

`kernel.v1` only grows: new operations, optional fields, enum values, reasons
and metadata keys. Anything breaking is `v2`, served side by side. `buf lint`
and `buf breaking` (against `main`) run in CI. Clients ignore unknown enum
values and fields; every enum keeps `UNSPECIFIED = 0`. Until the proof of
concept is done and someone else depends on v1, breaking changes are allowed
but listed in the journal.

`buf breaking` uses **WIRE_JSON** (`buf.yaml`): beyond wire numbers and
types, it holds JSON field names and enum value names, since clients send
and match them (pli speaks JSON). CI runs it on pull requests only, against
the base branch; from Step 0, part 3 on, `main` is the baseline.

## Links, containment and people (decided 2026-11-15)

- **A Type declares its link types** like its fields: name, inverse name,
  allowed target Types, at most one or many. A link change is an event on the
  source Item (with `item_seq`, so history and etags see it). Links are
  visible to CEL guards and usable as search filters. A link to something the
  caller can't see shows as "a restricted item". **Links never change who can
  see anything.** A `max 1` link (a story's `epic`, its `sprint`) can be shown
  like a field but stays a link.
- **Containment is the one privileged link:** at most one, no cycles, the only
  link that carries permissions. It keeps its own storage and enforcement
  (scope pointer, move lock, `ConfirmWiden`).
- **People are principals, referenced by a principal field type**
  (`assignee`, `reporter`, `approvers`): the server checks the value is a
  real principal; search can filter by it. A person's profile is a `person`
  Item tied to its principal, with `manager` and team links, written only by
  HR or a sync; identity, roles and groups never live in Items.

### Links and principal fields as built (proof of concept, Step 1)

**Declaring.** `links: [{name: blocks, inverse: blocked_by, targets: [ticket],
max: many, inverse_max: many}]`. `max` is per source, `inverse_max` per
target; unwritten is `many`. Names and inverses are lower-case identifiers,
unique among the Type's names and inverses. Targets are Type IDs, any version;
they must exist in the set loaded at start (`ParseDir`). **A link name keeps
its inverse across a Type's versions**: a version that gives a declared name
another inverse is refused at load and by `RegisterType`, naming the Type, the
link and both inverses, so (source Type, name) always names one inverse.

**Writing.** `Link` and `Unlink` (`LinkItems`, `UnlinkItems`) take the
declared name, never the inverse, a `request_id` and an optional etag, both
the source's. One event on the **source only** (`link_added` /
`link_removed`, payload `{link_type, target}`, opaque IDs), with `item_seq`;
the target gets no event and no new `item_seq`. So the target's etag does
not change when it gains or loses an incoming link: a client holding it may
meet a guard that differs from what it saw. One Item per write; no initial
links on create.
- The caller must see both ends (else `NOT_FOUND`); for an agent both are in
  its session's scopes, it holds a write power, and the write rule runs
  against **both** ends' scopes, the source first (a link changes the
  target's behavior through guards).
- Locks: the source FOR UPDATE and the targets FOR KEY SHARE, all in UUID
  order (one helper, `lockLinkEnds`).
- Cardinality is the database's: each row carries `src_single` and
  `dst_single`, snapshots of the source Type at write time, under partial
  unique indexes. A violation is `LINK_CARDINALITY` with `link_type` and
  `side`; it never names the Item holding the slot. **A target-side refusal
  still tells the caller that some source, possibly one they cannot see,
  holds the target's single slot.** Accepted, for the same reason as guards
  (below): the rule must hold for everyone.
- Links may change while a hold is open: checks read fields, not links.

**Reading.** `GetItem` and every write's answer show links both ways, as
`{link_type, direction, item}`: the declared name outgoing, the inverse
incoming. Search and List results carry none. The order is fixed: outgoing
before incoming; within a direction by the link's `created_at`, then the
**declared** name (`link_type` as stored, also for incoming links, which are
shown by their inverse), then the other end's ID (the target outgoing, the
source incoming). At most 200 per direction, restricted ones included, then
`links_truncated`; the full list pages through a link filter.
- An outgoing link to an Item the caller cannot see (or outside an agent's
  session) is **restricted** (no ID); an incoming link from one is
  **omitted**, since the target has no event that could show it.
- **Someone who sees a restricted outgoing link cannot remove it** (they
  cannot see its target), and **the hidden target may keep the source's own
  guard failing** for them. Accepted: links never widen visibility.
- Listing links (Get, history) records no agent read: a link carries only an
  ID. If a link listing ever shows titles or fields, it must record the read.

**Guards.** `links.<name>` is a list of `{state, category}`, never fields.
The names a Type's guards see are its declared names (outgoing) and the
inverses of its link types that target the Type itself (incoming); incoming
links declared by other Types are not visible yet. Each transition records
the names its guard reads (from the CEL AST); only those transitions read and
lock linked Items. Guards evaluate linked Items whatever the caller can see:
**so a refusal caused by a hidden blocker tells the caller that something
they cannot see blocks the move**, though the refusal names nothing.
Accepted: a rule must hold for everyone.
- Locking, on all three doors (public transition, timers, a hold's re-check):
  read the Item and its link set unlocked; lock the Item FOR UPDATE and the
  linked rows FOR SHARE together in UUID order; read the link set again and
  start the transaction again (`errRetry`) if it changed. FOR SHARE blocks
  every state change of a linked Item until the commit. `write()` also
  retries Postgres deadlocks (40P01) and serialization failures (40001).
- Links that were not read fail safe: a guard that reads links does not hold.

**Search.** A link condition `{link_type, direction, item}` names the link as
the **result** Items see it: outgoing takes a declared name (Items that block
X), incoming an inverse name (Items blocked by X). It matches every Type that
declares the name in that direction; a name no Type declares in that
direction is `INVALID_ARGUMENT` (Types only grow, so a refused name can only
start working later). The condition's Item must be visible (else
`NOT_FOUND`); results are only Items the caller sees.

**Principal fields.** `type: principal` holds `user:<id>` or `agent:<id>`
(`group:` is reserved; kernel actors are refused); no existence check until
person Items. A principal field grants nothing. In an equality filter, `me`
is the caller: the user an agent acts for, else the agent; `me` is accepted
only on principal fields (a stored value always has a kind prefix, so it
cannot collide). **A field that is a string in one version and a principal in
another counts as one stored type for equality filters** (the value must
then be a principal or `me`). Guards read it as a string. An agent may
assign anyone; notifications, when they come, must pass through `Project`
for their recipient.

### Approval as a business state (proof of concept, Step 2, part 1)

Decided 2026-11-16. A manager's decision is an **ordinary transition**
(`submitted -> approved`, `submitted -> rejected`) restricted by `allowed`:
no approval hold kind, no `ApproveHold`. The kernel gains only generic
primitives; nothing in it is named "approval" (the membership test: approvals
stay in Types).

**Kernel-written fields.** `writable_by: kernel` on a field: only the
internal door writes it (a hook declaring it in its `writes`). The public
door refuses it on create, update and a transition's patch, held requests
included: `PERMISSION_DENIED` / `FIELD_NOT_WRITABLE` (+ `fields`). Some hook
must write it, or the Type is refused at load (in part 2, link reactions
will count as writers; the attribute is an enum so link types can take it).

**Frozen fields per state.** `frozen: [fields]` on a state: no write may name
them while the Item is in that state, from any door (a hook included):
`FAILED_PRECONDITION` / `FIELD_FROZEN` (+ `fields`, `state`). Naming a field
is enough, even with its current value. A write is checked against the state
it starts in, so the transition **entering** the state may set the fields (a
check's correction on the way into `submitted`), and the one **leaving** it
may not (recall first, then edit). Freezing the decided states too keeps what
was approved what stands.

**Order of a patch's refusals:** malformed (`INVALID_REQUEST`), not
writable, frozen, then held (an open check's frozen reads).

**`field:<id>` in `allowed`.** Matches the principal a principal field holds
when the transition runs (the stored value); an empty field matches no one.
**A transition's patch may not write a field its own `allowed` names**
(`FIELD_NOT_WRITABLE`, before `allowed`), on every door; a check's
correction is that transition's patch, so a check may not write such a
field (refused at load). On an editable field, `field:` means "whoever the
field names when the transition runs": anyone who may edit the field in an
earlier write can change that.

**`creator`: two meanings, side by side** (package `policy`, one function each):

| Where | Function | Matches | Why |
| --- | --- | --- | --- |
| `allowed: [creator]` | `IsRequester` | the principal's effective user (its `on_behalf_of`, else its actor) equals the Item's effective creator (`created_on_behalf_of`, else `created_by`) | "This is the requester": only the person the Item is for, or an agent acting for them, acts as them |
| `except: [creator]` | `TookPartInCreation` | the principal's actor **or** its effective user equals `created_by` **or** `created_on_behalf_of` | "Who must not complete it": separation of duties is conservative, so an agent that created a request for ana may not decide it, acting alone or for anyone |

The kernel's own actors (`system:timer`, hooks) are neither. In `allowed`,
`creator` is judged once for the principal; every other entry keeps the
both-must-pass rule. `except` is asked with `allowed`, after the guards: a
match is `NOT_ALLOWED`.

**Who `allowed` and `except` judge, per door:** the token's principal (and
its session's user) on the public door; `system:timer` or the hook on the
internal door (never the creator); on `ResolveHold`'s re-check, the
**original requester**, actor and `on_behalf_of`.

**Agents cannot approve** (Step 2): a `field:` entry holding `user:<manager>`
never matches `agent:<x>`, and the agent's user passing is not enough. An
agent with a namespace-wide session and every power, acting for the
approver, is `NOT_ALLOWED`.

**Hooks read the creator.** A hook's `reads` may name `created_by` and
`created_on_behalf_of` (set at create, never editable). The vacation
request's HR check reads them, asks the HR directory for the requester's
manager, and answers `correct` with `f_approver` (the Item enters
`submitted` with it, in the same write) or `deny` (`unknown_employee`, or
`no_eligible_approver` when someone manages themselves).

**Built in part 2** (see "Link reactions, the leave balance and the saga"):
`writable_by` on link types, reactions and their consent, `add`, the
refund. **Still open for part 3:** a field-less reminder event needs an
explicit `ErrHeld` exemption, and timers refused as held will be deferred,
not spent.

**Constraints that were kept open for parts 2 and 3:** `writable_by` extends to link
types; reactions will declare which fields they write on a target Type; an
`add` op on ints and a compensating refund arrive in part 2; a field-less
reminder event will need an explicit `ErrHeld` exemption, and timers refused
as held will be deferred, not spent, in part 3.

### Link reactions, the leave balance and the saga (proof of concept, Step 2, part 2)

Decided 2026-11-16, built 2026-11-17. The vacation request
(`vacation_request@2`) deducts its days from a leave balance
(`leave_balance@1`) when approved, and gives them back when the approval
does not stand. Nothing in the kernel is named balance or vacation: the
kernel gained reactions, invariants, role writers and kernel-set links.

**Reactions.** A Type declares writes on the Items its links point to:
`{id, kind: held, on: enter, state, from: [...], link, add: {field: cel}}`,
and its undo `{id, kind: after, on: enter, state, from, undoes: <held>}`.
The principal is `reaction:<type>:<id>`, `originating_hook`
`reaction:<type>@v<n>:<id>`; reaction and hook IDs share one space
(`item_holds.hook` names either).
- A held reaction holds each `from -> state` transition: asking for it
  opens a hold the reaction decides. It writes on its link's one target;
  the move applies if that write commits, and is denied with the target's
  refusal (an invariant's ID) if not. One hold per transition: a check
  hook and a held reaction on one transition are refused at load. `add`
  expressions are CEL over the source's fields only (no `today`, no links),
  of type int; the fields they read are frozen while its hold is open, like
  a check's reads.
- Every held reaction has exactly one undo (refused at load otherwise). The
  undo reverses the write's **recorded delta**, never recomputes it: a
  requester who edits `f_days` after withdrawing cannot get more back than
  was taken. An `after` reaction with a patch of its own is Later.
- **Consent.** The target declares `accepts_reactions: {<type>:<id>:
  [fields]}` (int fields, unversioned keys), checked at load across Types
  (`ParseDir`) and at write time against the target Item's pinned version
  (`reaction_not_accepted`). A consent counts as the writer of a
  `writable_by: kernel` field.
- **`add`** is for reactions only: an unset field is 0 + n. History shows
  both the result (`item_event_values.field_values`, as for every event)
  and the amount (`field_deltas`, migration 00011); the payload lists
  `field_ids` and `added_field_ids`; the API shows `fields` and `added`.
- **Relaxed only for reactions:** a reaction's cause is an event of the
  source while it writes the target (`React`, `Undo`), or resolves its own
  hold on the source (allow, deny or error, never correct). `hook:` and
  `system:` keep the same-Item, same-Type rule; a reaction is refused on
  `Internal().Update` and `Transition`. A reaction is never the requester
  nor took part in creating.

**The saga.** `React` writes one Item, the target, under the key
`<held>:<source>:<requested seq>:<target>`. It locks the source FOR SHARE
and the target FOR UPDATE in UUID order and commits only while **that hold
is open**: an escape (the source FOR UPDATE) waits for it or makes it
refuse (`ErrStale`), so a deduction never outlives its hold. It checks the
link at write time (`not_linked`). Deduct and resolve are two transactions
of one Item each, so the hold can close another way in between:
- **Undo** runs when the hold closes any other way than allowed (denied, an
  error, the budget, the approver's re-check failing, canceled by an
  escape), and when the state its allowed resolution entered is left by the
  undo's trigger (`approved -> withdrawn`), for the hold that **entered**
  it (`StateEntry.EnteredHold`), never an earlier one. Its key is
  `<reservation key>:<undo>`, one per deduction whichever path asks. It
  finds the reservation through the idempotency record committed with the
  deduction, so without a committed deduction it writes nothing, and it
  never runs twice. It does not check the link: it gives back what was
  taken, where it was taken. An open hold, or an allowed one whose state
  was not left, is `not_undoable`.
- **In the workflow** an undo is never dropped: one activity try per
  attempt, a pause doubling from 1 s to one minute, without limit. A
  refusal ends it, recorded once on the source (`REACTION_FAILED`).
  Attempts and the last error stay on the pending undo and show in the
  workflow query `undos` (LoadItem reads Postgres, which never sees
  retries); after 100 undo attempts in one run the run continues as new,
  carrying them. LoadItem reports the undos not yet done, so a run after a
  crash resumes them. Alerting on a stuck undo is Later.
- **Why not one transaction** (deduct and resolve together): no
  compensation would be needed, but it writes two Items per transaction;
  one Item per write keeps Items independent, and is the shape a target in
  another service will need.

**Invariants.** `invariants: [{id, rule}]`, CEL over the fields only; the
ID is the refusal code (`INVARIANT_FAILED`, metadata `invariant`) and a
held reaction's deny reason. A rule that cannot be evaluated (a null) does
not hold, so rules test for null themselves; there are no field defaults.
Checked **last** on every write that changes fields, on every door: create,
update, a transition's patch, a held request's patch, a hook's write, a
reaction, an undo; a check's correction that breaks one denies the hold,
naming it. **Why not a self-transition with a guard:** updates have no
guards, so lowering `f_allowance` below `f_used` would pass; a
self-transition is a state entry (timers restart, enter hooks rerun,
plumbing in the lifecycle); and a Type without states would need a fake
one. An approver may learn `insufficient_balance` about a balance they
cannot see: accepted, for the same reason as a hidden blocker (a rule must
hold for everyone).

**Role writers.** `writable_by: [role:<name>, ...]`: on the public door the
actor, and the user an agent acts for, must both hold one of the roles
(`FIELD_NOT_WRITABLE`, after visibility); the kernel's own writers are not
affected. On `leave_balance`, `f_owner` and `f_allowance` are `hr_admin`'s
and `f_used` the kernel's; the invariant `balance_incomplete` refuses a
balance without an owner and an allowance, so a non-admin cannot create one.

**Kernel-set links.** A link type `writable_by: kernel` with `resolve:
{on_enter, field, equals: creator, state}`: LinkItems and UnlinkItems
refuse it to everyone (`LINK_NOT_WRITABLE`, after NOT_FOUND), and the write
that enters `on_enter`, on any door, sets it to the unique Item of its one
target Type in `state` whose principal `field` holds the Item's creator
(`created_on_behalf_of`, else `created_by`), whoever can see it. None or
several: `no_<link>` / `ambiguous_<link>`, a denied hold inside
`ResolveHold`, `LINK_UNRESOLVED` on a direct transition. The same target
again records nothing; another replaces it in the same write
(`link_target`, `link_replaced` on the transition event). One per state.
Locks: the candidates and the current target are read **before** any lock;
then the Item (FOR UPDATE), the guard's linked Items (FOR SHARE), the
candidates (FOR SHARE) and the current target (FOR KEY SHARE) are locked in
one UUID-sorted pass; then everything is read again, and a change starts
the transaction again. A balance created meanwhile is not locked (a
phantom): two open balances for one person are refused as ambiguous, never
prevented (uniqueness is Later). Balances stay open until HR closes them,
so a new balance needs no delete.

**Workflow changes without `GetVersion`** are proven by replay:
`internal/workflow/testdata/histories/` holds histories recorded from main
before the change (a ticket with a hold and a timer, a vacation request
with the HR check, a timer that fires), and `TestReplay_HistoriesFromMain`
replays them on today's code.

## Spike build order

A step is a unit of work, not a calendar day: it is finished when its "done when"
passes, however long that takes.

| Step | Build | Done when |
| --- | --- | --- |
| 1 | Schema, `Create` and `Get`, idempotency keys | Same idempotency key twice → same item_id and commit_seq; exactly one event and one outbox row |
| 2 | First, the schema follow-ups from the Step 1 review (scoped idempotency keys, `namespace_id`, attribution columns, `item_event_values`, commit counter, indexes). Then load a Type from YAML; `Update` and `Transition` via the pure transition function; guards | Illegal transitions rejected with a reason; legal ones recorded; events carry attribution columns and values live in `item_event_values` |
| 3 | Outbox relay with per-Item ordering; interpreter workflow started lazily with Update-With-Start; one durable timer | Killing the worker mid-timer still fires it; duplicates and gaps handled |
| 4 | Scopes and `VisibleScopes`; Postgres full-text search with `min_seq`; one narrowed Item | A second user never sees the narrowed Item, even right after a change |
| 5 | Type v2 while v1 Items run; measure actions, history bytes, write latency; burst test: 10,000 messages into one channel (a Type with no lifecycle); write up | Both versions complete; burst numbers show rows and latency per message, and no outbox rows when nothing listens; all numbers recorded in `docs/journal.md` |

**Hardening, Step A (after Step 5):** `EXPLAIN` the unstable reads; replace
the commit counter with the sequence and watermark above; keep visibility in
`policy_scope_id` and `scope_access`, with a scope node per container; typed
principal IDs; property tests against a naive evaluator. Done when 16
writers wait on no shared row lock and reach at least 2× the 1-writer
throughput on the same machine, `min_seq` and the relay lose and reorder
nothing, a principal who sees one scope pays nothing for others' narrowed
scopes, and the same bench is rerun and compared in `docs/journal.md`.

**Follow-up spike, Steps 6–7: holds and connectors** (after Step A2). Their job
is to test the leaning decision of 2026-11-12, that connectors run outside the
write transaction, with holds and a bounded wait, before it becomes firm.
Background: `docs/research/extension-points-in-work-platforms.md` and
`docs/research/connectors-for-the-item-kernel.md`. Setup: stub HTTP servers
stand in for a CRM and an ERP, with configurable latency, error rate and outage.
The ticket Type gets three hooks: `on_enter(triage)` fetches the customer's plan
from the CRM (falling back to `unknown`); a blocking ERP check on `approve`
answers allow or deny (customer on hold); `on_complete(approval)` creates an
invoice in the ERP.

| Step | Build | Done when |
| --- | --- | --- |
| 6 | First, protect internal principals: only server-side code may act as `system:*` or `hook:*`, and only it may set `originating_hook`, `causation_id` and `hop`. Then hooks as activities that write back through the item service; a **hold** (the Item stays in its state until the check allows, denies or corrects it); a bounded `wait` on `Transition`; the variant D guard: an `on_enter` prefetch of `customer.on_hold` plus a CEL guard (`cel-go`, replacing the spike's parser), with the hold as a backstop. Widen `HasListeners` for hooks | A caller outside the server cannot act as `system:timer` or a hook; with stub latency ≤ 500 ms, p95 time to a definitive answer stays under 3 s with D; kernel write latency is unchanged during a stub outage and while workers are down; a stale prefetched fact is caught by the hold; every hook write carries principal, `on_behalf_of`, originating hook and `causation_id`, and the chain can be rebuilt |
| 7 | `on_complete` invoice with an idempotency key; forced activity timeouts; variant A (every check as a hold) for comparison; variant B (a synchronous ERP call before the transaction, 2 s timeout) on a throwaway branch, numbers only; write up | No duplicate invoices across 1,000 forced timeouts; p50/p95 recorded for A, B and D at 50 ms, 500 ms and 5 s stub latency, with the share answered within `wait`, in `docs/journal.md` |

Questions to settle along the way: does the SLA clock pause during a hold? Is
"requested, not yet applied" a substate or a separate field? How does the UI
show a hold?

**Outcome (Step 7, decided 2026-11-15).** The leaning is firm: connectors run
outside the write transaction, always. Checks are holds with a bounded wait
(the D pattern, prefetch plus guard with the hold as backstop, where the
deciding fact can be prefetched); side effects are action hooks whose
idempotency key reaches the connector. A synchronous call before the
transaction (variant B) is not offered: with a healthy ERP it saved ~100 ms
at the median, but it put the ERP's latency in every checked write (2 s when
the ERP hangs) and answered nothing at 5 s of ERP latency, while A and D kept
writes at ~5 ms and answered everything, late. Across 3,100 forced failures
per run (late or dropped answers, a stopped or killed worker) the invoice was
never duplicated; without keys, once per ticket. Numbers and the three
questions' recommendations: `docs/journal.md`, "Holds and connectors:
write-up".

### Settled direction for holds, approvals and agents

Recorded so Steps 6–7 do not paint us into a corner. Build only what the
step's "done when" needs; the rest is direction, not scope.

- **States are business lifecycle, never plumbing.** A state says where the
  work stands (`triage`, `active`, `waiting`, `done`), never what the system is
  doing (`calling_erp`, `creating_po`). Multi-step automation runs inside a
  state, as holds and connector actions.
- **Untrusted values carry taint, per field.** The Type declares which fields
  hold free text written from outside (intake, sync, a hook's output, customer
  text). An agent session that read an untrusted value, in any scope including
  open ones, cannot act alone on egress or privilege: outbound connector
  actions, widening readers, changing `allowed`, roles or assignee. Those become
  a hold a human clears. Ordinary edits stay free. A connector action declares
  its sensitive parameters (recipient, amount, URL); mapping an untrusted field
  into one makes a hold.
- **An approval covers exact inputs.** The Type declares what it covers
  (`covers: [amount, vendor]`); the token stores a digest of those values, and
  any change makes it stale. No declaration falls back to `item_seq`. The
  requester and the Item's creator cannot complete it; an agent acting for a
  user counts as that user. People and agents complete it the same way.
  *As built (proof of concept, Step 2, part 1):* no token. Approval is a
  business state; what it covers is the state's `frozen` fields, which no
  write may change while the decision is pending, nor after it; the
  requester and anyone who took part in creating the Item are refused by
  `except: [creator]`. See "Approval as a business state".
- **`ApproveWiden` narrows to one held write** in the product: the held write
  stores its payload hash, and the approval completes that write only. If the
  Item moved on, it is stale and the user is asked again.
- **Kill switch per Type version, hook or connection.** A flag in Postgres,
  checked first by the connector activity, which returns "paused" without
  calling out (not in the workflow: it cannot read Postgres, and signalling
  every workflow is a costly fan-out). On "paused" the workflow parks the Item
  in a visible hold and waits for a resume event through the relay; it does
  not retry. It stops the next outbound call, not one in flight, so outbound
  actions carry idempotency keys; it stops side effects, not writes.

**Go checkpoint, after Step 3:** if most time went into fighting the language
rather than the design, stop and decide whether to rerun in Java.

## Proof of concept build order (after Step 7)

Goal (decision log 2026-11-15): prove the kernel is a building block by
running a Bonita-style vacation request and a Jira-style project on it, driven
only through the public API by the `pli` CLI. Scenario scripts in `pli`
commands are the end-to-end tests; every kernel change a step needs is counted
in the journal.

| Step | Build | Done when |
| --- | --- | --- |
| 0 (**done**) | Public API v1 server (`cmd/itemservice`, connect-go) and the `pli` CLI (`cmd/pli`) over what exists: create, get, update (patch, readers, move), transition with `wait`, search with filters, list by container, item history, Types read-only, agent sessions with required scopes and powers. Dev-token auth, the error table, paging, `request_id`, `etag`, plain-JSON field values. `buf lint` and `buf breaking` in CI. `docker compose up` runs Postgres, Temporal, a one-shot `migrate`, the item service, relay, worker and `connstub` (the stub CRM and ERP) | A scripted `pli` session creates, edits, moves (including a held approve with `wait`), searches with filters, lists a container and reads history, acting as two users; every named refusal comes back with its code and reason; an agent session refused outside its scopes; a cross-namespace attempt fails on every operation; a stranger can run it from the README. **Done 2026-11-16**: [journal, Step 0, part 3](journal.md#2026-11-16--step-0-proof-of-concept-part-3-one-command-stack-scenario-readme-finished) |
| 1 (**done**) | Link types and principal fields | Declared link types with cardinality, inverse and allowed targets, enforced; link changes are events with etags; guards and search see links; principal fields checked. **Done 2026-11-16**: [journal, Step 1](journal.md#2026-11-16--step-1-proof-of-concept-link-types-and-principal-fields-finished) |
| 2, part 1 (**done**) | Vacation request: approval as a business state, its primitives, the HR check | Draft -> submitted -> approved/rejected through pli, the approver set by the HR check and never by the requester, the submitted dates frozen, no self-approval, agents unable to approve, every refusal named; through the API, pli and the scenario. **Done 2026-11-16**: [journal, Step 2, part 1](journal.md#2026-11-16--step-2-proof-of-concept-part-1-approval-primitives-and-the-hr-check-finished) |
| 2, part 2 (**done**) | Link reactions, the leave balance, the held deduction and its compensation | Approving a vacation request deducts its days from the requester's balance exactly once, or is DENIED insufficient_balance; two approvals racing never overdraw; every non-allow end of the hold after a deduction refunds it once, never without one; withdrawing an approved request refunds it; the requester never chooses or changes the balance; through the API, pli and the scenario. **Done 2026-11-17**: [journal, Step 2, part 2](journal.md#2026-11-17--step-2-proof-of-concept-part-2-link-reactions-the-leave-balance-the-saga-finished) |
| 2, part 3 | Acting timers, timers deferred during holds, the stub email connector, the full scenario | Planned |
| 3+ | The Jira scenarios | Planned per step when reached |

## Out of scope for the spike

Real UI, email intake, business-hours calendars, multi-tenancy (the
`namespace_id` column exists, with one default namespace), a dedicated search
engine, SLA objects beyond one timer.
