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
  calendar date, `2026-12-01`), `datetime` (an instant: see "Jira part 2,
  step 1 as built"). A field may be `writable_by: kernel`: only the
  internal door (a hook, or another Type's reaction it accepts) writes it
  (see "Approval as a business state"), or `writable_by: [role:<name>, ...]`:
  on the public door only holders of a role write it (see "Link reactions,
  the leave balance and the saga"). A Type may declare `invariants`, rules
  over its fields that every write keeps, and `reactions`, writes on the
  Items it links to. It may name the links other Types declare towards it
  (`incoming`) and declare `rollups`, counts computed when an Item is read
  (see "Jira part 1 as built").
- States: each has a category — `triage, backlog, todo, active, waiting, done, canceled, none`.
  `none` means "not work": work views leave it out. A Type that declares no
  states gets one implicit state, `default`, with category `none`. A state may
  declare `frozen: [fields]`: no write changes them while the Item is in it.
- Transitions: from/to, guard expression, required fields, fields to clear
  (`clears`, see "Jira part 1 as built"), fields to compute (`sets`, see
  "Jira part 2, step 1 as built"), who may act, action references.
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
  Or, instead, one timer at the instant a datetime field holds:
  `{id: active, category: active, at: {field: f_end_at, to: closed}}` (see
  "Jira part 2, step 1 as built"). A state has `timer` or `at`, never both.
- Approvals: a business state, decided by an ordinary transition; the kernel
  has no approval concept (see "Approval as a business state").
- Items stay on the Type version they were created with. Versioning rule:
  **pin the lifecycle, evolve the schema, float the policy.**

Guards are CEL expressions (`cel-go`), compiled when a Type loads. Every field
is a variable named by its ID, typed by the schema and nullable: unset is
`null`, so "is set" reads `f_x != null`. A guard that cannot be evaluated does
not hold. A guard reads links as `links.<name>` (see "Links, containment and
people"). A date field is a CEL timestamp at 00:00 UTC, a datetime field a
timestamp, and `today` is the date the write happens on, **in UTC** (the
item service's clock; a user's local date can differ: Later). Who acts is
not a guard variable: `caller`, `caller_on_behalf_of`, `created_by`,
`created_on_behalf_of`, `today`, `links` and `cause` are reserved names no
field may take.

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
             claim idempotency key (namespace, actor, effective user, key)
               already used → return the stored result, write nothing
             lock Item row (SELECT … FOR UPDATE)
             read the write's time (clock_timestamp()), now that the lock is held
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
  are scoped by `(namespace_id, actor, effective_user, key)`, so two clients
  never collide. The effective user is the person an agent acts for (a
  user or a customer), else the actor itself (Jira part 2, step 4a; it was
  `(namespace_id, actor, key)`, under which one agent acting for two people
  had the second person's write refused as a reuse of the first's key).
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
- **The write's time** (Jira part 2, step 1). An event's `created_at` is the
  database's clock read **after** the write has taken its row locks
  (`clock_timestamp()`), never `now()`, which is the transaction's start: a
  write that waited for the lock started before the write it waited for,
  and would be stamped earlier. The lock runs an Item's writes one after
  the other, so **along `item_seq` an Item's event times never go
  backwards**, and a duration between two of its events is never negative.
  A create and a transition read it once (`stampWrite`) and use it for
  everything the write computes from "now" (`cause.at`, the Item's
  `created_at`); every other write takes it at the event's insert, under
  the same lock. `commit_seq` and the outbox are unaffected. The guards'
  `today` still comes from the item service's own clock.
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
- **Events naming other Items.** An event's `causation_id` (`<item>:<seq>`)
  names another Item when a reaction writes on its target. On the way out
  it is shown only to a reader who can see that Item; to anyone else it is
  omitted (not "restricted": an ID-shaped field should not hint), as is a
  value that does not parse. Link targets and containers are shown or
  restricted the same way. `ListEvents` (behind the API's `ListItemEvents`
  and `pli item history`) is today the only path that returns events, and
  applies this. **Any future event path (a watch, a stream, a webhook)
  must go through `ListEvents`' projection**, never read `item_events`
  raw. (Consolidation after Step 2.)

## Durable behavior (Temporal)

- Most Items never get a workflow. A workflow starts **lazily** when an Item
  enters a state that needs timers or waits.
- **Relay** (`cmd/relay`, single instance): reads undelivered outbox rows and
  delivers each Item's events **in item_seq order**, one at a time, with update
  ID = `item:seq` (a redelivery reuses it). It sends the event's shape only,
  never field values. (For a Type with a timer at a field's instant the
  shape includes the IDs of those fields the event set or cleared: IDs,
  still no value.)
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
  sees the Type itself. **It never sees a field value, with one declared
  exception:** the instant held by a datetime field that the Item's current
  state names in `at`, read by the `LoadInstant` activity
  (`Service.AtInstant`, which chooses the field itself; the workflow names
  none). A timer's length is in the workflow's history anyway, so the value
  could not be hidden; nothing else of an Item's fields reaches Temporal. Duplicate (at or below `lastAppliedSeq`) → accept and
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
  `ErrHeld`, an `ErrStale`, naming the hold. A state timer due while a hold
  is open waits for it to close, then looks again (Step 2, part 3; before
  that, a timer refused as held was spent).
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
  `reaction:<type>:<id>` (a Type's link reaction), and since Jira part 2,
  step 4a `customer:<id>` (a person outside the organization: fixed
  permissions, never a role, never in the namespace's general audience; see
  "Jira part 2, step 4a as built").
  The actor, `on_behalf_of`, every reader and every role holder is one. An
  untyped ID is `ErrInvalid`. Users, agents and customers are readers; `group:`
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
  **One field value does reach our Temporal:** the instant of a datetime
  field that a state names in `at`, and nothing else. It is the declared
  exception (see "Durable behavior"), listed with its reason in the test
  that holds every read to `Project`.
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
- `order_by` sorts by one declared field instead of newest first (see "Jira
  part 1 as built"). **String order is byte order** (`COLLATE "C"`) in every
  sort and every string comparison, whatever the database's locale: rank
  keys depend on it. Locale-aware sorting of human text is Later.
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
"move state" gets its own named, typed call (`ApproveWiden`, `LinkItems`), never
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
and timestamps as ISO 8601 strings, principals as `user:ana`, enums by name.
A `datetime` is an RFC 3339 string: sent with an offset (refused without
one) and at most microseconds, returned as UTC with six fractional digits
(`2026-12-04T17:00:00.000000Z`) whatever was sent. A
mismatch is `INVALID_ARGUMENT` naming the field. A patch changes only the
fields it names; JSON `null` clears one. No field masks.

### Writes

- `request_id` (the idempotency key) is **required** on every write; same key
  and same request returns the stored result, same key and a different request
  is `ALREADY_EXISTS` / `IDEMPOTENCY_KEY_REUSED`. A request refused before any
  work uses no key.
- `etag` on any write: refused as `ABORTED` / `STALE` if something the
  caller could have overwritten changed since they read it. It is derived
  from the Item's `content_seq`, not from `item_seq`, and written `c7`;
  clients treat it as opaque. See "The etag" below. **A customer gets no
  etag** (empty): they never update, and an etag would tell them that
  something they cannot see changed. They transition without one.
- **A customer never sees `commit_seq`** (review of 2026-11-21): a
  customer, or an agent acting for one, gets 0 (absent in JSON) in every
  write's answer (`CreateItem`, `UpdateItem`, `TransitionItem`,
  `LinkItems`, `UnlinkItems`; a replay too) and in `ItemEvent.commit_seq`.
  It counts everybody's writes. **A customer client must not send
  `min_seq`**: a non-zero one is `INVALID_ARGUMENT` on `min_seq`, whatever
  its value. Their read-your-writes rests on the one Postgres, where a read
  sees a committed write at once.
- `requester` on `CreateItem` (Jira part 2, step 4a): a person (a `user:`,
  not an agent, not a customer) opens an Item for a customer, as on the
  phone. It is stored as `creator_on_behalf_of` and on the create event,
  so the customer is the Item's requester. Allowed only in a container
  whose Type version owns the Type, and that the customer can see.
  Anything else is `INVALID_ARGUMENT` on `requester`, in the same bytes
  whether the customer is hidden from the container or never existed.
- The idempotency key belongs to the caller and the person an agent acts
  for (Write path): one agent acting for kim and for lee with one
  `request_id` makes two writes. This also fixes an agent acting for two
  staff users.
- `TransitionItem` takes `wait` (default 0, at most 5 s) and answers `APPLIED`,
  `DENIED` (with a reason code), `ERROR` or `PENDING` with the hold's name. A
  denied held transition is a successful call, not an error.

### The etag (the etag step, 2026-11-20)

**The etag changes only when something a caller could overwrite changes.**
An Item has two counters, in the same row, written by the same statement
in the same transaction: `item_seq` counts every event, and `content_seq`
counts the writes that changed the Item's content. The etag is
`content_seq`. `item_seq`, `commit_seq`, `expected_item_seq` on the
internal door (timers, holds, reactions), `min_seq` and page tokens mean
what they meant.

One function decides (`itemservice`, `content.go`), from the event's kind,
its payload and the Type: never from a value, and never from who wrote.

| Event | Changes the etag |
| --- | --- |
| `create` | yes (the first content: `c1`) |
| `transition`, by anyone, a hold resolved allow and a re-entry of the same state included | yes |
| `update` naming a field a caller may write (role-written fields included), by anyone, a set operation included | yes |
| `update` naming only fields marked `writable_by: kernel` (a reaction's set or add, an acting timer, a hook) | no |
| `update` of the readers, or a move to another container | yes |
| `link_added`, `link_removed` (on the source; the target has no event) | yes |
| `transition_requested` (a hold opens) | only if its patch names a field a caller may write |
| `transition_denied`, `transition_failed` | no |
| `hook_ran`, `hook_failed`, `reaction_applied`, `reaction_failed`, `reaction_skipped` | no |

- **In doubt, it changes:** a field the Type version does not declare, an
  update that names nothing. A refusal costs a refresh; a missed one loses
  an update.
- **It reads the event's shape, not values:** a write of the value a field
  already had changes the etag (no-op updates are not suppressed yet).
- **A field is judged by its declaration, not its writer:** a hook writing
  a field that is not marked `writable_by: kernel` changes the etag. The
  ticket's four hook-written fields are not marked (journal).
- **Holds:** an etag read before a hold opened is still good after it is
  denied or fails (unless the request carried a patch), and stale after it
  is allowed. While the hold is open, `HELD` refuses the frozen fields and
  the moves whatever the etag.
- **A new event kind has no decision until someone writes one:** the
  function has no default and refuses the write; a table test covers every
  kind, and a second test holds the API's list of kinds to the decided
  ones.

**Its text** is `c` and the count, exactly as the server wrote it: no
sign, no leading zero, no spaces. It is not a number, so that nobody
takes it for `item_seq`. An etag this server never made is `STALE`,
including **a decimal etag from before this step**: read the Item again.
Existing Items started with `content_seq = item_seq` (migration 00012).

**Rule for every write path: never UPDATE an Item row twice in one
transaction.** When a transaction updates a row it has already written,
Postgres re-checks that row's foreign keys and takes `FOR KEY SHARE` on
the rows they point to: the container's and the scope's. The write then
waits for any transaction that holds the container's row lock, where one
UPDATE would not, and a reaction that writes a parent and its children can
deadlock. Found when `content_seq` was first bumped by a second statement
(a test that locks a container while a reaction moves its children
stopped passing). Reproduction, in plain Postgres:

```sql
create table items (id int primary key, container_id int references items, n int default 0, c int default 0);
insert into items values (1, null), (2, 1);
-- session A:  begin; select 1 from items where id = 1 for update;   -- holds the parent
-- session B:  begin; update items set n = n + 1 where id = 2; commit;          -- returns at once
-- session B:  begin; update items set n = n + 1 where id = 2;
--                    update items set c = c + 1 where id = 2;                  -- waits for A:
--   SELECT 1 FROM ONLY "public"."items" x WHERE "id" = $1 FOR KEY SHARE OF x
```

So each statement that writes `item_seq` writes `content_seq` too (a
source-reading test holds every one of them to it), and the rule's
decision travels with the event: `appendEvent` takes it in place of the
event's kind. (A narrowed create still writes its row twice, the insert
and its scope: the insert already holds those key locks.)

### Reads

- Paging everywhere: `page_size` (default 50, max 500), `page_token`,
  `next_page_token`. Opaque tokens, no offsets, no exact totals.
- Paging is keyset on `(created_at desc, id desc)` (history: `item_seq`),
  or under `order_by` on (the field's value, id) with the Items without a
  value last, never an offset. A page token is bound to its request (filters, words,
  container), its caller and its namespace: used with anything else it is
  `INVALID_ARGUMENT` (decided in Step 0, part 2).
- A filter with one value (one Type, one state) is `=` in SQL, which walks
  the index in order and reads one page. Several values are `= any(...)`,
  which on Postgres 17 sorts every match before the page (26-36 ms at
  30,000 matches): a known cost, in the journal's Later with its fixes.
- Filters on `SearchItems` and `ListItems`: Type, state, category, field
  equality and ranges (int, date and string), principal fields ("assigned
  to me"), links ("linked to X as `sprint`"). A field filter works across
  the selected Types, or across every Type that declares the field when
  none is selected. A filter alone is a valid search. Link conditions and "me"
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
| `ErrApprovalUnavailable` | `PERMISSION_DENIED` | `APPROVAL_UNAVAILABLE` (no metadata): an agent acting for a customer made a write that would need an approval, which nobody can give; refused at once, never pending |
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
they must exist in the set loaded at start (`ParseDir`); `targets: ['*']`,
alone, means an Item of any Type (Jira part 1). **A link name keeps
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
The names a Type's guards see are its declared names (outgoing), the
inverses of its link types that target the Type itself (incoming), and,
since Jira part 1, the `incoming` names it declares for links of other
Types. Each transition records
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
  Since Jira 2.4b this pass is step 2 of three: the container first, the
  children last ("Jira 2.4b, PR 1 as built", Locks).
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
refund. **Built in part 3** (see "Time and mail"): the field-less
reminder event (`hook_ran`, exempt from `ErrHeld` by construction), and
timers due during a hold deferred, not spent.

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

### Time and mail (proof of concept, Step 2, part 3)

Decided 2026-11-17, built 2026-11-17. `vacation_request@3` reminds its
approver, expires when nobody decides, and emails the requester when it is
approved, rejected or expired, and when a refund is given back. The kernel
gained acting timers, field-less actions, `reaction_applied` and deferred
timers; the email is a stub connector.

**Acting timers.** A state declares `acting: [{id, after, every, max,
hook}]`: the action hook `hook` (`on: timer`) runs `after` the event that
entered the state, then every `every`, **`max` runs in all** (required, at
least 1; `every` required when `max > 1`). Each `on: timer` hook is run by
exactly one acting timer (refused at load otherwise). A run is keyed
`timer:<item>:<timer>:<entry seq>:<n>`, caused by the entering event, one
hop below it, and runs as its hook. Leaving the state stops further runs (a
run already started finishes); a new entry counts from 1 again. Runs do not
wait for a hold: a reminder during the approval hold is the point. A run
that gives up records `hook_failed` naming the timer and run, and the next
run still comes. After a crash, `StateEntry.ActingRuns` (runs with a
`hook_ran` or `hook_failed`, per timer, for the current entry) says where to
go on; overdue runs fire **one at once, then the schedule** from it, never
a burst. In the workflow each acting timer is a coroutine canceled on
leaving the state, so Types without acting timers issue the same commands
as before (no `GetVersion`; the replay test proves it).

**Field-less actions.** An action hook may write nothing only when its
action writes nothing (`email.notify`). It records that it ran with
`hook_ran` (`Internal().HookRan`): payload `{hook[, timer, run]}`, no
field, no state change, under the hook's own key. Naming no field and no
move, it is exempt from `ErrHeld` and frozen fields by construction. Only
such a hook writes it; a timer hook must name its timer and a run within
`max`, any other hook none. An action that makes something (an invoice)
still proves it ran with its write-back. `Update` with no fields stays
refused. Hooks may read `creator` (`created_on_behalf_of`, else
`created_by`), a reserved name.

**`reaction_applied`.** After an undo's write commits, the Undo activity
records it on the source (`Internal().ReactionApplied`), under
`<refund key>:applied`: payload `{reaction, target}` (`<type>:<undo>`, the
target event). Without a committed refund it is refused `not_applied` and
nothing is written, so nothing can follow a refund that did not happen.
Action hooks `on: reaction` (`reaction: <undo>`) run on it: the refund
email. It is written only for Types with such hooks, and for those a
pending undo is done only once both the refund and its `reaction_applied`
exist (a crash between them resumes the undo, a no-op under its key, then
records it). The event starts the workflow even in a final state, and an
action hook is pending from `reaction_applied` until its `hook_ran`
(`StateEntry.PendingActions`), so a run after a crash sends it; a run never
ends with a pending action.

**Deferred state timers.** A state timer due while a hold is open waits
for the hold to close, then looks again: the Item left the state, the
timer is gone; still there, it fires at once. A `Held` answer (a hold the
run has not heard of yet) is not final: like `Stale`, the timer looks again
after the next event. Behind `workflow.GetVersion("timer-waits-for-hold")`:
a run recorded before keeps the old rule (fire anyway; held is spent),
tested by pinning the version to `DefaultVersion`. The SLA clock keeps
running during a hold; `pause_while: held` on a real SLA object is Later.

**The email stub** (`connstub`, `--email-listen :8084`): `POST /messages
{to, template, item, key}`, the template being the hook's ID and `to` a
user or agent principal; the same key and message is stored once, the same
key with another message is 409 `key_conflict`. Opaque IDs only, no text.
`--email-read-listen :8085` serves `GET /messages?item=` read-only; the
compose stack publishes it (dev only) so the scenario can read what was
sent.

**The dev clock.** The worker's `--dev-time-scale <day>` (a day of a Type's
**declared** durations, state and acting timers, lasts `<day>`) is refused
without `--dev-clock-i-know-this-is-not-production` and an explicit
`--dev-time-scale-types` list, each name checked against the loaded Types
at start; never all Types by default. Hook budgets, the undo backoff, hold
waits and every other timeout keep real time, and `today` is never scaled.
The worker warns on every start, naming the Types; the scale is recorded in
the run's Snapshot (`DevDay`). Only the compose stack (the dev stack, with
dev tokens) sets it, for `vacation_request`, a day lasting 2 s. A timer
deferred by a hold is proven by a Go end-to-end test with its own worker
scaled for `ticket` and a slow ERP customer, not by the scenario.

**`vacation_request@3`**: `submitted` reminds the approver after 48 h, then
24 h later (`max: 2`), and expires after 120 h (`submitted -> expired`,
allowed to `principal:system:timer` only; `expired` is canceled). Emails
(`email.notify`, reading `f_approver` or `creator`): `remind_approver`,
`notify_approved`, `notify_rejected`, `notify_expired`, and `refund_email`
on `refund`.

### Consolidation after Step 2 (decided and built 2026-11-18)

An adversarial review of how Step 2's primitives meet found seven bugs;
each was reproduced by a failing test, then fixed. The rules they settle:

- **A held target is not a refusal.** A held reaction whose target is held
  by its own open hold (the reaction writes a field the target's check
  freezes) retries within its budget; when the budget runs out, the
  source's hold resolves as error, `budget_exhausted`. Never left open.
- **A refused correction closes the hold.** When the kernel refuses a
  check's correction (Invalid, Rejected or Forbidden: a value of the wrong
  shape, a frozen field), the check activity closes the hold as error,
  `correction_refused`, under its own key `hold:<id>:refused` (a retry
  sending the correction again under `hold:<id>` never collides with it).
  Only on the correction path: any other refusal is not giving up. A check
  writing a field frozen in its transition's from-state is refused at
  load.
- **React writes where resolution would.** For a resolve link, React runs
  the link's lookup again under the target lock; none, several or another
  Item than the link's target deny the hold with `link_target_changed`,
  and nothing is written. No silent re-resolution: the requester
  resubmits, which resolves again. **A candidate created concurrently is
  not locked (a phantom), at resolution and at React alike:** it can slip
  past both, and the next resolution refuses the two as ambiguous.
  Accepted for the proof of concept; uniqueness is Later.
- **`causation_id` across Items** passes through visibility (see "Events
  and personal data").
- **Escapes and holds.** A check or a held reaction on an `escape: true`
  transition is refused at load (an escape cancels a hold; it may not open
  one). The relay marks an escape's event `Canceled` beside the hold it
  names, and the workflow never reads it as the hold allowed, behind
  `GetVersion("canceled-hold-not-allowed")`, asked only of a `Canceled`
  event, so recorded histories replay unchanged (a fourth, with a canceled
  hold, proves it).
- **Resolve links at load.** `resolve.on_enter` may not be the initial
  state (Create resolves nothing), and a held reaction on a resolve link
  may leave only from that link's `on_enter` state. Links people set are
  exempt.
- **No undo without a run.** An event that closes a hold any other way
  than allowed (denied, failed, canceled), on a Type with a held reaction
  leaving that state, starts the workflow (`lifecycle.StartsWorkflow`), so
  a pending undo runs even if the run that took the request is gone.

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
| 2, part 3 (**done**) | Acting timers, timers deferred during holds, the stub email connector, the full scenario | A submitted request reminds its approver on schedule and expires when nobody decides, emailing the requester; approval and refund emails go out exactly once, none after a denial; a timer due during a hold waits for it; through the API, pli and the scenario on a dev clock. **Done 2026-11-17**: [journal, Step 2, part 3](journal.md#2026-11-17--step-2-proof-of-concept-part-3-acting-timers-deferred-timers-and-the-email-stub-finished) |
| Consolidation (**done**) | An independent adversarial review of how Step 2's primitives combine; seven interaction bugs fixed, each reproduced by a failing test first; the flaky hooks test fixed | Every finding reproduced then fixed in its own commit; all recorded histories replay. **Done 2026-11-18** (PR #25) |
| Jira 1 (**done**) | The PLAT software project: ordering by a field, rank as an app field, incoming links from other Types, rollups, field effects on transitions, watching (see "Jira proof of concept" below) | Scenarios A and D below run through pli; ordering pages correctly; ranking costs measured. **Done 2026-11-18**: [journal, Jira part 1](journal.md#2026-11-18--jira-proof-of-concept-part-1-the-plat-software-project-finished) |
| SLA design (**done**) | A design pass on paper for SLAs and multi-value fields | Decided 2026-11-18: "Decisions for Jira part 2" below |
| Jira 2.1 (**done**) | Date-time fields, a timer at a field's instant (proved by a sprint closing on its end instant), computed field effects | The sprint closes itself when its end instant passes, and moving the end re-arms it; effects computed at the cause's time; through the API, pli, the web UI and the scenario. **Done 2026-11-19**: "Jira part 2, step 1 as built" below, and the journal |
| Jira 2.2 | Reactions that move or create, including those triggered by an Item created inside the source; multi-value fields | "A customer comment reopens the request"; labels with set operations and contains filters |
| Jira 2.3a (**done**) | The kernel pieces for goals kept as contained Items, proved with test Types: working-time arithmetic on a calendar, reactions that move children or set a field on the container, consent by reaction ID, a create reaction reading linked Items, an **acting timer at a field's instant**, creation as an entry, `not_empty` (see "Jira part 2, step 3a as built") | A job starts, pauses, resumes and stops its meters; a meter is marked late at its due instant and never before; a stopped worker changes none of the numbers |
| Jira 2.3b (**done**) | The `sla` package (`sla_goal`, `calendar`, `sla_policy`), the incident Type with first-response and resolution goals, pli and the web UI | Scenario B runs through pli. **Done 2026-11-20**: "Jira part 2, step 3b as built" below, and the journal |
| Etag rule (**done**) | The etag changes only when something a caller could overwrite changes (the state, a field a caller may write, a link, the readers, the container); outcome records, hook records and kernel-written field writes leave it alone | A save made with an etag read right after an incident's creation is accepted once its goals have written their due instants: 10 times in 10 (measured in step 3b: refused 10 times in 10). **Done 2026-11-20**: "The etag" under "Public API (v1)", and the journal |
| Jira 2.4 (**done**) | Customers as principals and public or internal comments (4a); the access request with N-of-M approvers (4b: the building blocks, then the Types) | Scenarios C and E run through pli. **Done 2026-11-22**: "Jira 2.4b, PR 2 as built" below, and the journal |
| Bonita | The company's real Bonita processes, mapped against the BPMN pattern table, then rebuilt | Planned after Jira 2 |

## Jira proof of concept

Two projects test the kernel against Jira: PLAT, a software project (Jira
part 1), and IT, a service desk (Jira part 2, after an SLA design pass). The
real Bonita processes come after both.

### Decisions for Jira part 1

**Ordering is a kernel feature; manual rank is not** (decision log
2026-11-17). Manual ranking fails the kernel membership test: the Jira clone
ranks by hand, Slack orders by time (its sidebar order is per user), the
helpdesk sorts by priority and SLA. Sorting by a field passes it, and only the
kernel can sort correctly with visibility and paging. So:
- the kernel adds `order_by` on a declared field to `ListItems` and
  `SearchItems` (nulls last, ties by Item ID, keyset page tokens that carry the
  sort value), across every Type that declares the field;
- rank is an app field, scoped per container (a backlog or sprint orders its
  own Items). Jira's trouble came from one global rank whose rebalance touched
  and reindexed every issue. Fractional keys (midpoint with jitter, ties by Item
  ID) come from one shared client package that `pli` uses; the app rebalances
  one container at a time;
- accepted risk, to be measured: every drag is an ordinary write (an event,
  and on Types with hooks an outbox row and a workflow wake-up). If it hurts,
  the generic fix is a field attribute meaning "no rule reads this" that skips
  the outbox, never a rank feature.

**Watching is user space** (decision log 2026-11-13). Lasting interest is a
Subscription Item with a `watches` link to the watched Item: visible,
permissioned, audited, removable like any Item. No `Watch` call. Notifications
come later and read these links. The only kernel need is a link type that may
target any Type. Watching an Item you can't see is `NOT_FOUND`, as for any
link.

**Rollups are computed when read, over what the reader can see** (decision
log 2026-11-13 for read time; 2026-11-17 for visibility). An epic's progress
("3 of 5 done") is counted at read time, never pushed or stored. Unlike guards,
which see every linked Item so a rule holds for everyone, a rollup is a read:
it counts only Items the reader can see, because counting hidden ones would
reveal they exist.

**Incoming links from other Types.** A Type can name only its own links and
their inverses today, so an epic cannot name the stories that point to it
through the story Type's `epic` link. One primitive fixes both guards and
rollups: a Type names incoming links declared by other Types, checked across
Types at load. Guards see them as Step 1's links (state and category only).

**Field effects on transitions.** A transition may require fields from its
patch (as today) and clear fields in the same write, e.g. done requires
`resolution`; reopening clears it.

Deferred: a timer at a date rather than after a duration (a sprint closing on
its end date); in Jira part 1 the sprint is closed by hand.

### The PLAT Types

| Type | States (category) | Fields | Links |
| --- | --- | --- | --- |
| story, bug, task | todo (todo) -> in_progress (active) -> in_review (active) -> done (done); canceled (canceled) | summary, description, assignee (principal), priority, points, resolution, rank | epic (at most one), sprint (at most one), blocks / is blocked by, relates, duplicates |
| epic | todo -> in_progress -> done | summary, owner (principal) | progress: a rollup of the stories, bugs and tasks linking to it by `epic` |
| sprint | planned (backlog) -> active (active) -> closed (done) | name, start, end, goal | Items link to it by `sprint` |

Stories, bugs and tasks are contained in the PLAT container; they belong to a
sprint and an epic by links, not by containment. Comments are contained Items.

### Scenario A: a sprint in PLAT

1. lea creates an epic in PLAT, "Single sign-on" (PLAT-1).
2. lea creates stories and a bug in PLAT, each linked to PLAT-1 by `epic`.
3. lea creates sprint "Sprint 12" (end 2026-12-04) and links PLAT-2 to it by
   `sprint`; she assigns PLAT-2 to sam.
4. sam links PLAT-2 `blocks` PLAT-3, moves PLAT-2 to in_progress, and comments
   on it (a comment Item contained in PLAT-2).
5. lea opens the board: a search for the Items linked to Sprint 12 by
   `sprint` (a link filter, not a container listing), ordered by rank.
6. lea ranks PLAT-4 before PLAT-2; the board order changes.
7. sam lists his open work: a search with `assignee = me` and the todo and
   active categories, ordered by priority.
8. lea reads PLAT-2's history.
9. lea shows PLAT-1: its progress counts only what she can see.
10. sam moves PLAT-2 to done with resolution `fixed`; done without a
    resolution is refused; reopening clears it.
11. lea closes the sprint by hand.

### Scenario D: across the project

1. lea searches "VPN" in the active category.
2. lea searches stories, bugs and tasks with priority P1 that are open,
   ordered by a field, 20 per page, and pages through all of them.
3. ana watches PLAT-9: a Subscription Item linked to it by `watches`. Watching
   an Item she can't see is `NOT_FOUND`. Removing the subscription unwatches.

### Jira part 1 as built (2026-11-18)

Eight kernel changes; nothing in the kernel is named rank, epic, sprint or
watch. `kernel.v1` only grew.

**`order_by`** (`SearchItems`, `ListItems`): one field ID, optionally `asc`
or `desc` (AIP-132). Items without a value come last in both directions;
ties are broken by Item ID, ascending in both. Sortable types: string, int
(as numbers), date, enum (by option name: declared order is Later); bool
and principal are refused. The page token carries the sort value as text
(none for an Item without one) and the ID, and the order is part of the
request a token is bound to. Items whose Type does not declare the field
are not dropped: they sort as Items without a value.

**Fields across Types.** A field filter or `order_by` is resolved across
the selected Types, or across every registered Type that declares the
field when none is selected. It is refused (`INVALID_REQUEST`) only when no
Type in play declares the field, or when their declarations disagree on its
type; a string and a principal stay one type for equality (not for order).
Enums with different options are one type. So one field ID should mean one
thing everywhere: `f_priority` is an int on `ticket` and an enum on the PLAT
Types, and a search by it must name its Types. Dates take equality and
ranges; strings take ranges (byte order), which is how an app finds the
rank keys next to a card's.

**Rank is an app field.** `f_rank` is a string field on story, bug and
task, scoped to the Item's container. `internal/rank`, which no kernel
package may import (a test holds it), makes the keys: base-62 fractions
compared byte by byte. `pli rank ITEM before|after OTHER` (or `top`,
`bottom`) plans a move with `rank.Plan` over a few ordered reads of the
container and makes **one write**, an ordinary `UpdateItem`.
- Between two cards the key is near the midpoint; at the top or the bottom
  it is a small step past the last card (1/32 to 1/8 of the room left), so
  always adding at one end uses room up about nine times more slowly. A
  random jitter makes two simultaneous inserts into one gap rarely collide;
  when keys are equal the Item ID decides.
- The key is computed against the container's order, so the card sits next
  to the other one in every view that shows both (the backlog, a sprint's
  board).
- Equal neighbours leave no key between them: the move re-keys the run of
  cards that share the key, at most `rank.MaxTiedRun` (8) of them, and is
  refused past that with a pointer to the rebalance. If cards moved between
  the plan's reads it plans again.
- A move never rebalances. Past `rank.Threshold` (32 bytes) pli says a
  rebalance is due and still succeeds.
- `pli rank rebalance --in CONTAINER` rewrites one container's keys, short
  and even, one update per card under the card's etag, in an order that
  keeps the container in order after every write (`rank.RewriteOrder`).
  **It is a quiet-time batch:** a card moved while it runs is placed
  between keys the batch then moves, and two such moves in three land out
  of place (measured). An online rebalance is Later.

**Incoming links.** `incoming: [{name: issues, link: epic, from: [story,
bug, task]}]`: the Items of the `from` Types that link to this Type's Items
by their link `link`. The name must be the inverse every `from` Type
declares for it, so one name serves reads, search, guards and rollups; it
may not be one of the Type's own link names or inverses, nor name the Type
itself. `ParseDir` checks it across Types (`CheckIncoming`): each `from`
Type is loaded and some version of it declares the link towards this Type.
Guards read it as any link, state and category only. Locking needed no new
protocol: `lockForTransition` and `linkWrite` already order their locks by
UUID, and the Step 1 race tests pass across Types.

**Rollups.** `rollups: [{id: progress, over: issues, count_by: category}]`:
`over` is any link name the Type can read, `count_by` is `category` or
`state`. `GetItem` computes them in SQL, with visibility in the count:
a user's count is what they can see, an agent session's what is inside its
scopes, and counting Items of a narrowed scope is recorded as a read of
it. On `GetItem` only (`Item.rollups`, int64 counts, a key with no Item
absent): not on write answers, searches or lists; never stored, not
searchable. No sums.

**Clears.** `clears: [fields]` on a transition unsets them in the move's
own write. Order of checks, on every door: malformed (a patch that sets a
field the move clears) -> not writable -> frozen -> held -> required
fields and the guard, **before the clear** -> the clear -> invariants. A
cleared field counts as named by the write, so a role-written field is
cleared only by a holder of the role, even when it is empty. The event
lists `cleared_field_ids` for fields that had a value. Refused at load: a
clear of a field the same transition requires, that the state it leaves
freezes, that its own `allowed` names, or that only the kernel writes; and
any clear on a transition a check or a held reaction holds (which of the
two steps of a held move would clear is not designed: Later).

**Watching.** A `subscription` Item, created readable by its owner alone,
links to what it watches by `watches` (`targets: ['*']`, at most one).
Unwatching is `UnlinkItems`; the subscription stays (Archive is Later). The
watched Item shows `watched_by` only to those who can see the
subscription.

**The PLAT Types** (`types/`): `space` (the container: `f_key`, `f_name`),
`story`, `bug` and `task` (three files with one lifecycle; done requires
`f_resolution`, `done -> todo` clears it; links `epic` / `issues`, `sprint`
/ `sprint_issues`, `blocks`, `relates`, `duplicates`), `epic` (`incoming`,
the `progress` rollup, not done while an issue is open), `sprint` (closed
by hand), `comment`, `subscription`. None has listeners: a write costs one
row and one event, no outbox row. Issue keys (PLAT-12) are Later.

**Measured** (journal, Jira part 1): sorting by a field sorts every match
(no per-field index was needed at PLAT's size: the board of a 1,000-card
sprint reads in 3 ms; at 30,000 cards in one container a move's reads are
13 to 14 ms each); a drag is one event, one value row and no outbox row,
and on a Type with listeners one outbox row and six workflow history
events when its workflow is running. The "no rule reads this" field
attribute is not needed yet.

### Decisions for Jira part 2: SLAs and multi-value fields (2026-11-18)

Decided on paper before any code (the design doc "Pli: SLAs and
multi-value fields"), after comparing Jira Service Management, ServiceNow
and YouTrack.

**SLAs are app-level.** Each goal on a request ("time to first response",
"time to resolution") is an Item of an `sla_goal` Type, contained in the
request, one per metric per cycle; a reopened request starts a new cycle
and the old goal stays as history. Containment gives a goal the request's
readers; narrowing hides it from customers. This is ServiceNow's
`task_sla` record, and it keeps the 2026-11-13 membership decision: nothing
in the kernel is named SLA.

- **States:** running (active), paused (waiting), completed (done),
  canceled; a completed goal says how it ended in `f_outcome`, met or
  missed. (Amended in step 3b: the decision first had met and missed as two
  end states. One end state with an outcome field keeps "is it over" and
  "how did it go" apart, and lets one reaction end a goal without knowing
  the result.) The request's events drive them through reactions: start,
  pause, resume, stop, cancel, declared on the request Type and checked at
  load so a pause can never overlap a start.
- **Met or missed is decided on instants** (amended in steps 3a and 3b):
  completed while running, a goal is missed if `cause.at > f_due_at`, so
  exactly at the instant it is met; completed while paused, it is missed if
  its breach mark is still set. A met goal never carries a mark: a stop or
  a pause caused at or before the due instant clears one a timer wrote in
  the meantime (the mail already sent is accepted).
- **Time comes from the cause, never from the reaction.** Reactions run
  after commit, so a goal learns of a pause slightly late. Every elapsed
  time and due instant is computed from the timestamp of the request event
  that caused it; a busy worker delays the bookkeeping, never the outcome.
- **Breach is an event, not the end.** A timer at the due instant records
  `breached_at`; the goal keeps running until its stop condition and then
  completes as met or missed. A breach notification is an action hook,
  through the normal visibility rules.
- **Sorting queues:** each goal writes its due instant onto the request
  (`f_due_first_response`, `f_due_resolution`), kernel-written, through a
  reaction; a queue sorts by one metric with `order_by`. The copy lags the
  goal by one reaction step; the goal Item is the truth.
- **Rejected as the main model:** deadline fields only, on the request
  (YouTrack's way). Paused state, remaining time, cycles and breach would
  each become fields on every request Type, and SLA policy would be pinned
  to the request's Type version.

**Five generic kernel primitives** carry it, each with users beyond the
helpdesk:

1. A **date-time field type** (an instant, stored UTC, filterable, sortable).
2. A **timer at a field's instant**, re-armed when the field changes (also a
   sprint closing on its end date).
3. **Reactions that move or create**: an `after` reaction that transitions
   its target or creates a contained Item, triggered by a source's
   transition or by an Item created inside it (also "a customer comment
   reopens the request").
4. **Computed field effects**: a transition sets fields from a CEL
   expression evaluated at the cause event's time (`cause.at`).
5. **Working-time arithmetic**: pure CEL functions
   `working_after(t, minutes, cal)` and `working_between(t1, t2, cal)` over
   a calendar Item (week schedule, holidays, time zone). In the kernel,
   unlike rank, because a deadline must be recomputed inside the write that
   pauses or resumes, deterministically, on every door.

**Multi-value fields.** A field declared `multiple: true` (enum, principal,
string, date) holds a set: unordered, no duplicates, stored sorted, empty
stored as unset, optional `max:`. Patches may replace the set or send set
operations (`{add: [...], remove: [...]}`) so concurrent additions are never
lost. Equality filters mean *contains*, plus *contains all of* and *is
empty*; sorting by one is refused. Guards see a list. `allowed:
[field:f_approvers]` matches any member, with the agent rule unchanged.
Going from single to multiple is a new field ID, never a type change.

**Open for Jira part 2's Phase A:** a transition fired by a field change on
the same Item (N-of-M approvers moving on); customers as a `customer:`
principal kind with fixed permissions, or narrowed users (decided and
built in step 4a: a `customer:` kind, see "Jira part 2, step 4a as
built"); a running goal
keeping the policy it started with (proposed) or being recalculated; the
clock during holds (keeps running by default, `pause_while: held` opt-in);
sorting by the nearest deadline of any metric (later).

### Jira part 2, step 1 as built (2026-11-19)

Primitives 1, 2 and 4 of the decisions above, with six kernel changes
(journal). Nothing in the kernel is named sprint, resolution or SLA;
`kernel.v1` only grew.

**Date-time fields.** `type: datetime` holds an instant. A value is sent as
an RFC 3339 string with an offset (`Z` or `+01:00`) and at most six
fractional digits. Refused as malformed: no offset, finer than a
microsecond (never rounded), a leap second, a space or a lower-case `t` or
`z`, a year outside 0001 to 9999 once in UTC. It is stored, returned and
recorded in **one form: UTC, six fractional digits, `Z`**
(`2026-12-04T17:00:00.000000Z`); the caller's offset is not kept. The form
is fixed-width, so byte order is time order, and filters (equality,
ranges, bounds in any offset), `order_by` and page tokens use the text
machinery dates use. A `date` and a `datetime` declared under one field ID
disagree across Types. CEL sees a timestamp; a guard may compare it with a
date (00:00 UTC).

**A timer at a field's instant.** `at: {field: f_end_at, to: closed}` on a
state: when the instant the field holds passes while the Item is in the
state, the move is made as `system:timer`, attributed like a duration
timer's (caused by the event that entered the state, key
`timer:<item>:<entered seq>:<expected seq>`). Load rules as for `timer`: a
datetime field, a declared transition, an `allowed` list that names the
timer; a state has `timer` or `at`, not both. A date field is refused: a
date has no zone, and which instant ends that day is not decided.
- *Unset arms nothing*, but the state still needs a workflow
  (`lifecycle.NeedsWorkflow`): the run must be there to hear the field
  being set. So such a Type has listeners.
- *How the workflow learns the instant.* This is the declared exception to
  "the workflow never sees a field value" (see "Durable behavior"). The
  relay adds to an event's shape the IDs of the at-fields it set or cleared
  (`Event.FieldIDs`; nil for every Type without `at`). The workflow runs
  `LoadInstant` on entering such a state, or starting in one, and again
  after an event naming its field: never for another event. A read that an
  event overtook is discarded and made again.
- *Re-arming.* The wait is the instant minus `workflow.Now`, in a timer
  with a cancellable context of its own; an event cancels it and the loop
  starts the next. A cleared field disarms; an instant moved into the past
  fires at once. Today every event of the Item, not only one naming the
  field, cancels and restarts the timer, as duration timers do (Later).
- *Never early.* The item service refuses an `at` timer's move while the
  write's time is still before the instant: `ErrNotYet` with the remainder
  (nothing written, the key not used up), and the workflow arms again for
  that long. So a worker whose clock runs ahead cannot record a closing, or
  later an SLA breach, before its instant. With the field unset the answer
  is `ErrStale`. A person making the same move by hand is not checked.
- *Holds* defer it like any state timer. *The dev time scale* does not
  apply: it scales declared durations, and an instant is not one.
- *Continue-as-new* carries the instant read (`Snapshot.AtInstant`,
  `AtLoaded`): the next run reads nothing again. No `GetVersion`: only a
  run whose Snapshot has an `At` table enters the new branch.

**Computed field effects.** `sets: {f_resolved_at: 'cause.at'}` on a
transition: each expression is evaluated by `lifecycle.TransitionWith`
inside the move's own write, and its value set; `null` unsets. In scope:
the fields, **on the guard's view** (after the patch, before the clears);
`cause.at`, the write's time, which is the `created_at` of the event being
written; and `today`, which to an effect is **the UTC date of `cause.at`**
(guards keep the service's clock in this step). Expressions do not see
each other's results. No links, and nobody's identity: "resolved by" waits
for a declared keyword resolved by `policy`.
- *Order of checks, on every door:* malformed (a patch that names, with a
  value or with null, a field the move sets; or sets a field it clears) ->
  not writable -> frozen -> held -> required fields and the guard -> the
  clears -> the sets -> invariants.
- *The Type writes.* A field written by `sets` is exempt from
  `writable_by` (kernel or roles), and `sets` counts as the writer a
  kernel-written field must have. That is how a kernel-written field is
  unset: `sets: {f: 'null'}` (a `clears` of one is still refused at load).
- *Refused at load:* an unknown field; one the same transition requires or
  clears, that the state it leaves freezes, or that its own `allowed`
  names; a principal field; an expression that does not compile or does
  not yield a value of the field's type; any `sets` on a transition a
  check or a held reaction holds (as for clears: Later).
- *A failing effect refuses the move:* `effect_failed` (`FAILED_PRECONDITION
  / EFFECT_FAILED`, metadata `field`), for an expression that errors (an
  unset field in arithmetic) or a result that is no value of the field (a
  string that is no option, an instant that is not 00:00 UTC for a date).
  The reason names the field, never a value.
- A computed field shows in the event like a patched one (`field_ids`,
  `cleared_field_ids`, its value in `item_event_values`). No write, event
  or outbox row of its own: a Type whose only feature is `sets` has no
  listeners.

**What a Type shows.** `GetType` gives `FIELD_TYPE_DATETIME`, each state's
`timer` (`after`, `to_state`) and `at` (`field`, `to_state`), and each
transition's `sets`. The web UI's form has a `datetime-local` input on the
browser's clock, sent as UTC, with the UTC instant as its title; the
transition panel says "made by the timer at f_end_at" and "sets:
f_resolved_at = cause.at".

**The Types.** `sprint@2` adds `f_end_at` and closes itself (`active` has
`at`; closing by hand stays open to anyone). `story`, `bug` and `task` `@2`
add `f_resolved_at` (kernel-written): both moves to done set it to
`cause.at`, `done -> todo` sets it to null beside clearing `f_resolution`.
Version 1 of each still loads, and Items stay on their version.

### Jira part 2, step 2 as built (2026-11-20)

Primitive 3 of the decisions above and the multi-value fields, with nine
kernel changes (journal). Nothing in the kernel is named comment, checklist
or label; `kernel.v1` only grew; no migration.

**A reaction that moves, on enter.** Declared by the source:

```yaml
- {id: finish, kind: move, on: enter, state: done, from: [building],
   when: 'f_ready == true', link: whole, to: done, target_from: [building]}
```

When the source enters `state` (coming from one of `from`, if given) and
`when` holds (CEL over its own fields, a bool), the Item one `link` away
(max one) moves to `to`, if it is in one of `target_from`. The target's
Type consents: `accepts_reactions: {part:finish: {moves: [done]}}`. Consent
stands in for `allowed` and `except` only: the target's guard, `required`
and frozen fields apply as for anyone. `accepts_reactions` takes the old
list (the fields a reaction may add to) or a map `{fields, moves, creates}`.

**A reaction that moves, on created_inside.** Declared by the *container*,
about the Items created inside it:

```yaml
- {id: reopen, kind: move, on: created_inside, of: [comment],
   when: 'child.f_public == true', by: creator,
   from: [waiting_for_customer], to: in_progress}
```

`of` names the child Types. `when` reads only `child.*` (a field some
version of each `of` Type declares; a missing one is null). `by: creator`
means the child's creator is the container's requester. `from` is the
container's own states. No consent: the Type moves itself. A
source-declared `on: create` was not built. Delivery: `Create` writes one
outbox row for a child whose container's Type reacts to its Type, and the
relay sends it to the container's workflow (update `child`, starting one if
needed). A child in any other container costs no row and no event.

**A reaction that creates.** `kind: create, on: enter` only:
`{state, from, type, version, fields: {f: <CEL>}}`. The new Item is
contained in the source, with the container's readers; its fields are
computed from the source's fields and `cause.at`. Its Type consents with
`creates: [fields]`, which also counts as the writer of a kernel-written
field. It is created by `reaction:<type>:<id>` and has **no human creator**
(`created_on_behalf_of` is empty); the event carries the attribution.

**Two times.** A reaction's write carries two instants. Its event's
`created_at` is the write's own stamp, like any event's. `cause.at`, which
its `sets` and `fields` read, is the time of the event that triggered the
reaction. A delayed worker delays the first and never the second.

**Outcomes.** Exactly one field-less record on the declaring Item per
(reaction, cause event): `reaction_applied {reaction, target}`,
`reaction_failed {reaction, reason, target?}` or `reaction_skipped
{reaction, reason}` with `condition_false`, `not_by`, `not_in_from` or
`no_target`. A target that refuses (guard, required, frozen) is a failure
with the refusal's reason, not retried. Past the hop limit: failed,
`hop_limit`. Through the API every reason is upper case (`NOT_BY`), as all
event reasons are.

**In the workflow.** A state with enter reactions needs a workflow. The
run keeps a queue of pending reactions (deduplicated by reaction and
cause), run in order by one coroutine through `RunAfterReaction`, inside
the hook budget; giving up records `budget_exhausted`. The item service is
the judge, as for timers: the workflow carries shapes, never field values.

**Cycles** among move and create reactions ("T enters s", "a K is created
inside a C") are refused when the Types load.

**Multi-value fields.** `multiple: true` on a string, enum, principal or
date field, with an optional `max`. The value is a set: stored as a sorted
JSON array without duplicates; empty is unset. A write sends a list
(replaces the set), null or `[]` (unsets it), or a set operation
`{add: [...], remove: [...]}` (not on create; a value in both is
malformed). Over `max`: `TOO_MANY_VALUES`. The event records what was
asked in `set_ops`, shown in the history. One field ID
is single or multiple across every Type and version, never both.

**Set operations commute, so they need no etag.** Two people adding
labels at the same moment both land. A client should send a set-only
change without an etag and keep the etag when a plain field changes too.
A set operation still **changes** the etag (the etag step): a set can also
be replaced by a list sent with an etag, and that replace must not drop a
member someone added meanwhile.

**Reading sets.** A filter's equality on a set means *contains*; two
equalities on one field mean *all of*; `empty: true` matches an unset
field of any kind. Ranges and `order_by` on a set are refused. No index:
the filter runs on the candidates the other conditions leave (journal,
EXPLAIN). CEL sees a nullable list (dates: a list of timestamps).
`allowed: [field:f_reviewers]` matches any member.

**The Types.** `ticket@6` adds `waiting_for_customer`, `reopen` and
`open_checklist`; `comment@2` adds `f_public`; `checklist@1` is new;
`story`, `bug` and `task` `@3` add `f_labels` (max 20) and `f_reviewers`.

### Jira part 2, step 3a as built (2026-11-20)

Primitive 5 of the decisions above, the acting timer at an instant, and
what reactions lacked to keep goals as contained Items: ten kernel changes
(journal), proved with test Types (a job in a yard that has hours contains
meters). Nothing in the kernel is named SLA, goal or calendar Item;
`kernel.v1` only grew; no migration. The `sla` package and the incident
are step 3b.

**Working time.** Package `worktime` is pure: a week of local wall-clock
intervals (`mon 09:00-17:00`, several a day, `00:00-24:00` allowed, at most
21), holidays (local dates, at most 366) and an IANA zone. `After(t, d)`
is the earliest instant at which `d` of working time has passed (eight
hours after Monday's opening is Monday's closing); `Between(t1, t2)` the
working time between two instants. A local time skipped by a clock change
does not exist (the interval is shorter); a repeated hour is worked twice.
The zone database is compiled in (`time/tzdata`): the images have none.
Three CEL functions expose it in every expression:

- `calendar(week, holidays, zone)`: an opaque value, built from fields; no
  field holds one. A calendar that is none is an error, so an invariant
  `calendar(f_week, f_days_off, f_zone) != null` holds an Item to being one.
- `working_after(t, seconds, cal)`: an instant. Seconds must be positive
  (a Type that resumes past its due instant keeps the missed instant
  itself); an answer more than five years away is an error.
- `working_between(t1, t2, cal)`: whole seconds, truncated.

An error in a `sets` is `effect_failed`, naming the field and no value.

**Creation enters the initial state.** An enter reaction without `from`,
on the initial state, runs when the Item is created (its cause is the
create event); one with `from` never does. Hooks already worked so.

**A reaction that moves children.** `children: {of: [meter], when:
'child.f_kind == "main"'}` on a move reaction, instead of `link`: on
entering a state, or on `created_inside` (the Item created is the trigger,
the Items moved are its siblings; `from` is then the container's own
states and `target_from` the children's).
- The children are the contained Items of the `of` Types for which `when`
  holds, in a state the reaction moves from; any other is passed over.
- At most 8 (`MaxReactionTargets`): one more and nothing moves, one
  `reaction_failed too_many_targets`.
- One write per child, in the order of their IDs, under the key
  `<reaction>:<source>:<cause>:<child>`, then **one outcome record per
  child** on the source, naming it. Every record but the last carries
  `more`: the one that does not finishes the reaction, so a crash between
  two children leaves it pending, and running it again moves no child
  twice and records none twice. A child that refuses is a failure for that
  child; the others still move. A child that left its state between the
  read and its move is recorded `reaction_skipped not_in_from`; if the
  children changed between two runs so that no record lacks `more`, one
  closing `reaction_skipped not_in_from` finishes the reaction. None to
  move: one `reaction_skipped no_target`.

**`by: not_creator`** (`policy.IsNotRequester`): the Item created inside
was made by a person other than the container's requester. An agent acting
for the requester is the requester; the kernel's own actors are neither
`creator` nor `not_creator`.

**A reaction that sets.** `{kind: set, on: enter, state, from, when,
target: container, set: {f: <CEL>}}`: entering the state sets fields on
the Item's container, each from the source's fields and `cause.at`; `null`
unsets. One write (an `update` event by the reaction, caused by the
source's event), then one record on the source. The container's Type
consents with `sets: [fields]`, which counts as the writer of a
kernel-written field; frozen fields and invariants apply. No container:
`reaction_skipped no_target`. No consent: `reaction_failed
reaction_not_accepted`. No undo. A set enters no state, so it closes no
cycle; cycles now count "a K is created" as running K's from-less enter
reactions.

**Consent by reaction ID.** An `accepts_reactions` key may be `*:<id>`: a
reaction with that ID of any Type. It is honoured **only through
containment**: the Item's container creating it or moving it as a child,
or a contained Item setting a field on it. Over a declared link consent
stays exact, at load and at the write. An exact key wins over the
wildcard, whole. A wildcard for added fields is refused at load. So a
package of Types names no host, and loads without one.

**A create reaction reads linked Items.** Its `fields` may read
`linked.<link>.<field>` (one declared max-one link away from the source)
and `container.linked.<link>.<field>` (from the source's container), and
nothing else of either. The Type read from lists the fields in
`readable_by_reactions` (never a principal field), checked at load across
the Types and at the write against the linked Item's own version. The
read is made inside the reaction's write, with the source, its container
and the linked Items locked FOR SHARE in the order of their IDs. **It is
not subject to the requester's visibility**: the list is the consent. No
container, no link, or no consent: `reaction_failed` with `no_container`,
`not_linked` or `read_not_accepted`. A create reaction may now set a
multi-value field (a set copied whole); principal fields stay refused.
What is copied is pinned: the new Item never reads the linked one again.

**An acting timer at an instant.** `acting: [{id, at: <datetime field>,
when, sets, hook}]` on a state (no `after`, `every` or `max`): when the
instant passes while the Item is in the state, the item service sets the
timer's `sets` in one `update` event by `system:timer`, **without a
transition**, then the timer's action hook runs. `sets`, a hook, or both.
- *cause.at is the instant itself*, whenever the write is made: a mark
  written seconds late by a worker that was down holds the instant.
- *Never early.* `ActAt` refuses while the write's own time is before the
  instant (`ErrNotYet` with the remainder; nothing written, the key not
  used up), and the workflow arms again.
- *Once per instant.* The key is `timer:<item>:<timer>:<entry
  seq>:<instant>`; the hook's adds `:hook` (`hook_ran` names the timer, run
  0). A new instant acts again; a new entry arms again, and `when` (over
  the fields, judged in the write) says whether it then does anything.
- *Stale.* The Item left the state, entered it again, or the field holds
  another instant or none: `ErrStale`, nothing written; the workflow reads
  again after the next event.
- *In the workflow*, each entry has one coroutine that reads the instants
  (`LoadActingInstants`: with `LoadInstant`, the declared exceptions to
  "the workflow never sees a field value") and one per timer that waits on
  `workflow.Now`; leaving the state cancels them, an event naming one of
  the fields (`Event.FieldIDs`) has them read again. What already acted is
  in the Snapshot (`ActingAtDone`), carried across continue-as-new; a cold
  start rebuilds nothing: `ActAt`'s key answers again. It does not wait
  for a hold. No `GetVersion`: only a run whose Snapshot has an `ActingAt`
  table starts these coroutines (a recorded history,
  `meter_late_at_an_instant.json`, is replayed).
- *The race with a late reaction.* A timer can fire before a pause that
  was caused before the instant is heard of. The pause's `sets` take the
  mark back (`cause.at >= f_due_at ? f_late_at : null`); a hook that
  already ran is accepted.

**`not_empty`** in filters: the field has a value (a member, for a set).
Alone, like `empty`; a separate field because a proto3 bool cannot tell
false from unset.

**Types in subfolders.** `ParseDir` reads one level of subfolders (a
package of Types with its README); IDs stay global; a folder two levels
down is an error; dot-folders are skipped.

**What a Type shows.** `GetType` gives a reaction's `children_of_types`,
`children_when`, `target`, `set_fields` and kind `SET`; a consent's `sets`;
an acting timer's `at_field`, `when`, `sets`, `hook`; and
`readable_by_reactions`.

### Scenario B: the IT service desk

lea runs the desk (role `sla_admin`), ana opens incidents, sam works on
them. Targets are seconds for P1 and hours for P3, on a calendar that is
always open, so a P1 breaches while the script watches.

1. lea creates a calendar, an SLA policy (a target per metric and priority)
   and the IT desk, and links both to the desk. Nobody else can: not sam,
   not ana, who can neither link nor edit a target.
2. ana opens a P3 incident in the desk. Two goals start from its creation,
   first response and resolution, with the policy's P3 targets, timed from
   the incident's own create event. Each shows its due instant on the
   incident. Without a priority the incident is refused.
3. sam is assigned and answers with a public comment: first response is
   met. ana's own comment and sam's private note answered nothing.
4. sam sets the incident waiting for the customer: resolution pauses and
   its due instant leaves the incident. sam's comment does not bring it
   back; ana's public comment does, and resolution resumes with a later
   due instant.
5. ana opens a P1 assigned to sam. Each goal is marked breached at its due
   instant, never before, and goes on running; sam is emailed once per goal.
6. The breach of a P1 nobody is assigned to tells nobody: one
   `HOOK_FAILED` / `NO_RECIPIENT`, no email, nothing retried when someone
   is assigned later.
7. The queue: lea sorts incidents by resolution due instant; "overdue now"
   is a range on it; "ever breached" is a search for goals whose
   `f_breached_at` is not empty.
8. sam links the bug that caused the P1 (`caused_by`) and resolves it,
   late: both goals are missed. Resolving needs a resolution; closing by
   hand is refused. Fixing the bug changes nothing on the incident.
9. The P3 is resolved in time (met), then reopened by ana: a second
   resolution goal starts, and only that.
10. An incident canceled at once ends its goals with no outcome; nothing
    breaches and nobody is told.

### Jira part 2, step 3b as built (2026-11-20)

The `sla` package (`types/sla/`: `sla_goal@1`, `calendar@1`,
`sla_policy@1` and a README that states the contract), its first host
(`desk@1`, `incident@1`), scenario B, pli and the web UI. **Three kernel
changes**; nothing in the kernel is named SLA, goal, incident or desk;
`kernel.v1` only grew; no migration; no workflow code changed.

**Offers.** `offers: {breach_contact: f_assignee}` on a Type: a named
principal field (one value) offered to the hooks of the Items contained in
its Items. An **action** hook there reads `container:<name>` and gets the
field's value from its container **when the hook runs**, read as that
principal sees the container (visibility, then `policy.Project`). So a
package tells "whoever the host says" without naming a host or a field.
- Nobody to tell (no container, no such offer, the field unset, or a
  principal who cannot see the container) is `ErrNoRecipient`: the action
  activity records `hook_failed` / `no_recipient` once, under the hook's own
  key, and succeeds. Nothing is sent, nothing is retried, and nobody is
  told after the fact. The record names no principal.
- Load rules: an offer names a declared, single-value principal field; only
  an action hook reads one (a check's reads are what its hold freezes); no
  hook writes one. Across Types, a Type that creates Items inside its own
  must offer what those Items' hooks read; any other container is judged
  when the hook runs.

**A link only a role writes.** `writable_by: ['role:sla_admin']` on a link,
as on a field: `LinkItems` and `UnlinkItems` by anyone else are
`PERMISSION_DENIED / LINK_NOT_WRITABLE` (an agent and its user must both
hold a role). Its fixed message is now "the caller may not set links of
this type".

**A reaction's record names its target.** `ItemEvent.reaction_target` (an
`ItemRef`: the name, or restricted) on `REACTION_APPLIED`, `FAILED` and
`SKIPPED`: the Item the reaction wrote, created or was refused by; unset
when it names none. The per-child records of a children move are now told
apart. A reaction record no longer fills `link_target` (it did, with an
empty link type).

**What a Type shows.** `GetType` gives `offers` and a link's
`writable_by_roles`. pli gains one flag, `--not-empty FIELD` (the API's
`not_empty`, since step 3a); `--empty` and `--not-empty` of one field are
sent as one filter, which the server refuses.

**The package** (its README is the contract). A goal is an Item contained
in its host; every field is the kernel's.
- The host declares reactions with fixed IDs, which `sla_goal` accepts
  from any Type through containment (`*:<id>`): `sla_start_first_response`,
  `sla_start_resolution`, `sla_restart_resolution` (create), `sla_pause`,
  `sla_resume`, `sla_stop`, `sla_answered`, `sla_cancel` (move children).
  When each runs is the host's business.
- The target and the calendar come from the host's container: it links to
  one `calendar` and one `sla_policy` (seconds per metric and priority,
  written by `sla_admin`), both links written by `sla_admin` only. They
  are **copied** onto the goal at creation: a running goal keeps what it
  started with (recomputing is Later).
- The host declares `f_due_first_response` and `f_due_resolution`
  (kernel-written) and consents to the goal's eight set reactions: a goal
  sets its field while it runs and unsets it when it pauses, completes or
  is canceled. A queue sorts by them.
- At `f_due_at` an acting timer sets `f_breached_at` and the breach hook
  emails `container:breach_contact`. Resumed after its breach, a goal keeps
  the instant it missed; resumed with no time left, it is due at the
  resume instant.

**The host.** `incident@1`: new -> in_progress <-> waiting_for_customer ->
resolved -> closed (a timer, 120 h, and nobody else), reopening from
resolved (clears `f_resolution`), cancel from new and in_progress;
priority P1 to P4 required from creation; resolving requires
`f_resolution`. Only resolution pauses, only while waiting for the
customer; someone else's public comment stops first response; the
requester's public comment brings a waiting incident back; reopening
starts a new resolution goal only. `caused_by` links bugs, with no
reaction: the page shows the bug's state by reading it.

**Web UI.** Still no screen that knows a Type. A date-time value says how
far away it is, labelled "(wall clock)"; a link shows the linked Item's
state, read through visibility on load and on Refresh; a reaction record
links to its target.

**Measured** (journal). The queue and the breached search sort every match
after a sequential scan (5,000 incidents: 4 to 9 ms). A goal's row is 0.8
kB, and 4.3 kB when its calendar has 366 days off.

**Found: the etag changes too often.** A save sends the etag, and the etag
is the Item's `item_seq`, which every event bumps: a caller's write, but
also a reaction's outcome record, a hook's record and a write of a field
only the kernel writes. One ordinary cycle of an incident writes 20 events
on it, 15 of them the kernel's own (6 due-instant writes, 9 records). A
save made with an etag read right after an incident's creation was refused
`ABORTED / STALE` 10 times in 10, at 100 ms, 500 ms and 2 s; the goals'
writes land about 320 ms after the create. Not fixed in this step.
**Fixed in the next one (the etag step, 2026-11-20):** the etag changes
only when something a caller could overwrite changes; the same save is
accepted 10 times in 10. See "The etag" under "Public API (v1)".

### Jira part 2, step 4a as built (2026-11-21)

Customers: a customer opens an incident in a portal and sees only their
own requests and the public conversation on them; staff see everything as
before. **Six kernel changes** (journal); `kernel.v1` only grew; one
migration (00013); nothing in the kernel is named desk, incident, reply or
note.

**The principal.** `customer:<id>` is a kind of principal, like YouTrack's
reporters, and not a user narrowed some other way:
- **Fixed permissions.** A customer creates what a container admits
  (below), takes the transitions whose `allowed` names `creator`, as the
  requester (an empty `allowed`, a role or a principal never admits one),
  and nothing else: an update, a move, a policy change or a link is
  `PERMISSION_DENIED / NOT_ALLOWED` on an Item they see, `NOT_FOUND` on one
  they do not. A move with fields is an update.
- **Never a role**: the dev-token file and `EnsureRoles` refuse one, and
  `policy.Roles` reads none for a customer, whatever the table says.
- **Never in the general audience**: an open scope alone shows a customer
  nothing. A customer may be a reader of a narrowed scope; that is needed,
  never enough.
- **Never a principal field's value**, so never a breach contact or a mail
  recipient.

**What a container admits** is declared by Types, never per Item:
- `own: [incident]` on a container's Type (desk@2): customers create
  Items of these Types inside its Items, and see their own. A Type version
  that owns something is **a portal**: every customer of the namespace sees
  it, through its customer fields, unless it is narrowed (then only its
  customer readers do; `group:` readers are Later).
- `shared: [reply]` on a request's Type (incident@2): inside a request of
  theirs, customers see all Items of these Types and create them.
- `customer_fields: [...]` on any Type whose Items a customer may see: the
  field IDs, link names and rollup IDs they see and may set at creation
  (never a field only the kernel writes). Fail-closed: anything else, a
  field added in a later version included, is hidden. A Type that lists
  nothing shows the header only (ID, Type, state, create time, creator).
- Load rules: what `own` and `shared` name is loaded; a Type is not both
  own and shared in one container; `customer_fields` names the version's
  own fields, link names or rollups.
- **`own` and `shared` name a Type ID with all its versions: changing what
  a Type means for the audience takes a new Type ID, not a new version.**
  So the public conversation is `reply@1` and the internal one `note@1`
  (shared by nobody); `comment` stays as it was for PLAT and the ticket.

**The rule** ("their own"), depth 1, from the current container: a
customer sees an Item when (a) its Type version owns something, or (b) its
effective creator (`created_on_behalf_of`, else `created_by`, as
`IsRequester`) is the customer and its container's Type version owns its
Type, or (c) its container is such a request of theirs and shares its
Type. On top, the scope test applies as for anyone. Moved out of a portal,
a request is no longer theirs to see. An agent acting for a customer gets
the customer's view and its own scopes; the kernel's own actors are never a
customer (their `on_behalf_of` is attribution only).

**Two SQL forms of one rule, in package `policy`**, beside
`VisibleCondition`: `CustomerCondition`, a predicate over one Item row (Get,
links, rollups, history references, writes), and `CustomerRelation`, the
same Items as a relation that drives a customer's Search and List (their
own requests from the index `items_requester` on the effective creator,
what those share, the portals). The predicate never drives a list: on
500,000 Items it would scan every one (1,086 ms on Postgres 17, against
0.3 ms; journal). The property test holds both to a naive evaluator (Get,
Search and List of every container), and the plan test fails if a
customer's Search or List plan scans `items`; it explains the very query
Search and List build (`pageSQL`).

**What a customer sees of an Item** (`policy.Project`, with the Item's
Type version; without one, a customer gets nothing): its customer fields,
no etag, no hold; outgoing links whose name is listed and whose target they
see, incoming ones whose source they see and whose source's Type lists the
name, never a restricted reference; listed rollups only, counting what they
see. Every path, and its rule:

| Path | For a customer |
| --- | --- |
| Get, List, Search | the rule in SQL before the limit; page tokens and counts from visible rows only |
| Fields | `customer_fields` only, on every read |
| Filters, `order_by`, link conditions | the per-row rule below; a name no version lists for them is refused exactly as an unknown one |
| Words | matched over the fields they see, on their own rows |
| History | the create, the moves and the updates that set or cleared a field they see, with those values only; reader changes, moves, links, holds, hooks and reaction records left out; a cause naming an Item they cannot see is dropped |
| Links, rollups | as above |
| Not found | a hidden Item and a missing one answer the same bytes, by name and by key |
| Refusals | `NOT_ALLOWED` with no metadata |
| Types | readable, as for anyone |

**The per-row rule** (review of 2026-11-21). For a customer, a field or a
link name counts on a row only when **that row's own Type version** lists
it in `customer_fields`. A field filter or a link condition (outgoing: the
row's version; incoming: the condition's source Item's version) never
matches a row whose version does not list it; in `order_by` such a row
**sorts as empty, after the rest**. A name that no version lists for them
is refused in the same bytes as one nobody declares. Words already match
each row's own customer fields only. So a field or link a later version
shows them reveals nothing of the earlier versions' rows.

`updated_at`, `item_seq` and gaps in a customer's history show that
something hidden changed, never what: **gaps in `item_seq` on their own
Items are by design.** `commit_seq`, which counts other people's writes,
is never shown to them (Public API, Writes).

**Writes, checked again under the lock.** A customer's create (in a
container) and transition apply the customer rule a second time inside
the write's transaction, after the row lock: an Item moved out of what
admits them since the first check is hidden, and nothing is written. A
`requester` must see the Item as written (its scope, a narrow on create
included, and the rule), checked in the same transaction. A listed rollup
must have its link listed too (a load rule).

**Staff for a customer.** `requester` on `CreateItem` (Public API, Writes):
sam takes kim's call and opens the incident as hers. Refused from an agent
session in this step (Later).

**Agents.** A session acting for a customer holds at most a customer's
powers (`read`, `create`, `comment`, `transition:*` or
`transition:<state>`): more is `NOT_ALLOWED`, whatever the grant says; the
dev-token file refuses such a grant too. The idempotency key now includes
the person an agent acts for (Write path). A write by such an agent that
would need an approval is `APPROVAL_UNAVAILABLE` at once: a customer never
approves (a customer approving their own bot is Later).

**The Types.** desk@2 (a portal: `own: [incident]`, its name and key
shown, never its calendar or policy), incident@2 (`shared: [reply]`; the
requester's reply brings a waiting incident back, someone else's reply
answers, with no field read; the requester cancels a new incident and
reopens a resolved one, staff with `role:desk_agent`; a customer sees and
sets the title, priority and impact, and sees the resolution), reply@1,
note@1. Scenario B runs on the `@1` Types, unchanged; scenario C1 runs the
customers.

### Jira 2.4b, PR 1 as built: the building blocks (2026-11-22)

What the access request (PR 2) needs, built and tested on test Types (a rack
holds jars, a jar holds beans; a crate holds peas). **Eleven kernel changes**
(journal); `kernel.v1` only grew (`Reaction.for_each`, `Reaction.on_guard`,
`Field.unique_among_siblings`, `Type.placement_writable_by`); no migration;
nothing in the kernel is named request, approval or resource. The Phase A
and its answers: `docs/proposals/2026-11-21-jira-2-4b-phase-a.md` (on its
own branch).

**1. `container.<field>` in `sets` and create reactions.** A transition's
`sets` and a create reaction's fields read the container's own fields, as
`container.<field>`. A field reads as its value only if the container's
Type version lists it in `readable_by_reactions`; any other field, and
every field at the top level, reads as unset. At load: some loaded Type must
list the field; `container.state` and `.category` are a guard's, not an
effect's. For a create reaction, the container is the source's container
(the rack, for the beans a jar makes). The container is locked FOR SHARE
while it is read.

**2. Who, by plain reference only.** A principal field, and any field
holding a set, is set only by a plain reference to a field of its shape:
`container.<field>`, one of the Item's own fields, `each`, or one linked
read. Never computed, never a literal. Three parts:
- `sets` may now write a principal field, and a multi-value field (both
  were refused), by plain reference;
- `readable_by_reactions` may list a principal field (it was refused); a
  reader copies it by plain reference into a principal field only;
- a create reaction may set a principal field of the Item it makes (it was
  refused), by plain reference.
One value or a set, and the type, are judged across the Types loaded
together.

**3. `for_each`.** A create reaction with `for_each: <multi-value field>`
makes one Item per member, which its fields read as `each`. Each Item has a
key of its own (`<reaction>:<source>:<cause>:create:<hash of the member>`:
a member may be free text, so only its hash is written) and a record of its
own on the source; every record but the last says `more`, as a children
move does. More than `MaxReactionTargets` (8) members: none is made,
`too_many_targets`. A principal member is copied as it is, never computed.

**4. Guards read the container and its children.** `container` is
`{state, category}` (null at the top level); `children.<type>` is the list
of the contained Items of that Type, each `{state, category}`. Nothing else
of them: a guard that reads a child's field, or the container's, is refused
at load. **No visibility filter**: a guard judges the data, not the reader,
so a narrowed child still counts (a refusal can tell someone that something
they cannot see blocks the move, as with links). A guard reading them with
nothing read does not hold (fail safe).

**5. A move at the container.** A move reaction with `target: container`
(on: enter) moves the Item's container, which must consent
(`accepts_reactions: {<type>:<id>: {moves: [...]}}`, or `*:<id>` through
containment). At the top level: `reaction_skipped no_target`.

**6. `on_guard: skip`.** On a move reaction, opt-in: a target guard that
does not hold yet is recorded as `reaction_skipped guard_false` instead of
`reaction_failed guard_failed`. "Any move when ready": the first N−1
decisions' reactions skip, the N-th moves. The default is unchanged.

**7. `except: [container_creator]`.** Refuses anyone who took part in
creating the Item's container (`policy.TookPartInCreation` on the
container's creator): the requester never decides an approval inside their
own request. In `except` only.

**9. `unique_among_siblings: [states]`.** On a single principal field: no
two Items of the Type in one container, both in one of those states, hold
the same value. Judged on create, transition, update and a hold's allow,
under the container's lock (FOR NO KEY UPDATE), so two hand-overs to one
person at once are judged in turn and one lands. Refused:
`FAILED_PRECONDITION / NOT_UNIQUE`.

**10. `placement_writable_by`.** Who may move or narrow (or un-narrow) Items
of a Type version: `[kernel]` (no person; they stay where they were made) or
a list of `role:<name>` (an agent and the user it acts for must both hold
one). Leaving it out keeps the old rule: anyone who sees the Item. Checked
after visibility, so a hidden Item stays `NOT_FOUND`. Refused:
`PERMISSION_DENIED / PLACEMENT_NOT_WRITABLE`. desk@3 uses it; the existing
hole for every other Type is on the Later list.

**11. A hand-over.** A transition's `sets` may write a field its own
`allowed` names (`field:f_holder`), by plain reference only:
`waiting -> waiting`, `sets: {f_holder: 'f_next'}`. `allowed` is judged on
the value stored before the write, so only the holder hands over.

**12. `allowed: [nobody]`.** No person, agent, customer, nor the kernel's own
actors (the timer included) passes it; only a reaction, which stands in for
`allowed` by consent, makes the move. It stands alone in `allowed` and is
not an `except` entry. **Why the word `nobody`:** it is not a principal
(`principal:system:timer` was a principal no person can be, a kludge that
also let the timer in); it says what it means; it cannot collide with a
role, principal or field entry, which all have a prefix. sla_goal@1 keeps
its gap (anyone who sees a goal may complete it, as met); sla_goal@2 uses
`nobody` on every move. The web greys such a move out, like a timer's.

**Not built: #8 (`excludes` on a field) and #13 (reading two levels up).**
**A documented weakness (#8 and Q4c together):** an approval handed over to
the requester, or created for a requester who is also an approver, cannot be
decided (`except: [container_creator]`) until `access_admin` reassigns it.
With N = M such a request cannot be approved until then.

**Locks.** One order everywhere, container before child:
1. the container, FOR SHARE, when the write reads it (a guard reads its
   state, an effect its fields), or FOR NO KEY UPDATE when a field of the
   Item is unique among siblings;
2. the Item together with its linked rows, in one pass in UUID order, as
   before (and the resolution candidates);
3. its children, FOR SHARE, in UUID order, when a guard reads them.
- A container read before its lock is read again after the Item's lock: if
  the Item moved in between, the transaction starts again (`errRetry`).
  Children cannot be created in, nor moved into, a container locked FOR
  UPDATE (both take its key share); a child moved out between the read and
  its lock starts the transaction again.
- A set reaction takes its target (the container) FOR UPDATE, then the
  source FOR SHARE, whatever the IDs' order (before: in UUID order, which
  could deadlock against a child's decision).
- A set reaction copies the source's fields **as they are when it writes**,
  with the time of its cause (`cause.at`): a goal paused again before its
  `show_*` runs writes null, and the `hide_*` queued behind it does too.
- A children move picks its targets on a state read without a lock
  (`target_from`); the transition checks the state again after its lock
  (`not_in_from`), so this is safe.
- **Rows both linked to and contained in each other** can still deadlock:
  the link pass locks them in UUID order, containment container first.
  Postgres breaks the deadlock and `write()` runs the transaction again.
- `write()` counts the deadlocks it retried (`deadlockRetries`); three
  concurrency tests (decisions, set reactions on older children, Items
  linked both ways with a child decided) want the count unchanged.

**The Types.** desk@3 (desk@2, placed by `role:sla_admin` only: under
desk@2 anyone who sees a desk may narrow it and hide every request under
it). sla_policy@2 (an approval target, `f_approval_s`, `sla_admin`'s).
sla_goal@2 (the `approval` metric and its four set reactions for a host's
`f_due_approval`; every move `allowed: [nobody]`; the invariant
`metric_missing` refuses a goal with no metric). The `@1` versions are
unchanged.

**The API.** GetType shows `for_each`, `on_guard`, `unique_among_siblings`,
`placement_writable_by` and `nobody`. Two reasons: `PLACEMENT_NOT_WRITABLE`
(PERMISSION_DENIED) and `NOT_UNIQUE` (FAILED_PRECONDITION).

### Jira 2.4b, PR 2 as built: the access request (2026-11-22)

Someone asks for access to a resource; N of its M approvers must approve;
the desk fulfils. Four Types, built on PR 1's building blocks, with **no
kernel change**: `resource@1`, `access_request@1`, `approval@1`,
`fulfilment_task@1`. The dev-token file gives lea `access_admin`. Scenario E
runs it through pli.

**resource@1.** Who approves (`f_approvers`, a set of up to 8, the reaction
target cap), how many must (`f_required`) and who is told when a decision
is late (`f_contact`): all three written by `access_admin` only, and kept
sane by the invariant `quorum_invalid` (1 ≤ N ≤ M). Its calendar and SLA
policy links are `sla_admin`'s. Placed by `access_admin` only. It lets
reactions read all four of its fields (`f_name` for the fulfilment task).

**access_request@1.** Made inside its resource; `draft → pending → approved
| rejected | withdrawn`; placed by the kernel only.
- **submit** (`draft → pending`, the requester only) copies `f_required`,
  `f_approvers` and `f_contact` (as `f_desk_contact`) from the resource by
  plain reference, in its own write, and marks `f_submitted_at`. All four are
  the kernel's. The resource changing later changes no submitted request.
- **A submit with no quorum** (outside a resource, say) is refused by the
  invariant `submit_without_quorum`, judged after the copies: "submitted
  means N and M are set and 1 ≤ N ≤ M". **Not a guard, because a guard is
  judged before the transition's `sets`**, on the fields as they were: it
  would never see the copies. The marker `f_submitted_at` is what tells
  the invariant the request has been submitted (an invariant reads fields,
  not the state).
- **Entering pending** makes one `approval` per member of `f_approvers`
  (`for_each`, `f_approver: each`, the request's title copied for the
  Inbox) and an approval goal (`sla_goal@2`, metric `approval`) from the
  resource's calendar and policy.
- **The two moves out of pending** are guarded by counts over its
  approvals, whoever can see them: `approved` when
  `children.approval.filter(c, c.state == "approved").size() >= f_required`,
  `rejected` as soon as one approval is rejected. Anyone who sees the
  request may make them by hand; the guards make the result the same as the
  reactions'.
- **Leaving pending** closes the leftover approvals (`close_on_approved`,
  `close_on_rejected`, `close_on_withdrawn`: children moves to `canceled`,
  at most 8).
- **The goal measures the time to a decision**, so a rejection completes it
  as an approval does: `sla_stop` on entering `approved`, `sla_answered` on
  entering `rejected`, `sla_cancel` on `withdrawn`. The second reuses the ID
  `sla_answered` because sla_goal@2 consents **by reaction ID**
  (`*:sla_stop`, `*:sla_answered`, `*:sla_cancel`), and a reaction runs on
  entering one state: two states need two reactions, two IDs. No
  sla_goal@3. The request offers `breach_contact: f_desk_contact`.
- **Approved**, it makes a `fulfilment_task` naming the resource
  (`container.f_name`).

**approval@1.** One approver's decision, in its request; placed by the
kernel only.
- **Decided** (`pending → approved | rejected`) by its approver
  (`field:f_approver`), never by whoever created the request
  (`except: [container_creator]`), only while the request is pending
  (guard `container.state == "pending"`).
- **Each decision moves the request** (`move_approved`, `move_rejected`:
  `target: container`, `target_from: [pending]`, `on_guard: skip`). Until
  enough approvals have decided, the move is recorded as `reaction_skipped
  guard_false`. **Two decisions at the same moment**: the request's row lock
  lets one move leave pending, and the other move is skipped, as
  `guard_false` if it ran before the other decision committed, or
  `not_in_from` if the request had already left pending. Which one depends
  on timing; the end-to-end tests accept either, and hold what matters:
  exactly one `reaction_applied`, exactly one skip, and exactly one outcome
  of the request.
- **A late decision**: `GUARD_FAILED` in the window before the leftovers are
  closed (the request is no longer pending), `NO_TRANSITION` after
  (`canceled` has no move to `approved`).
- **The hand-over**: `pending → pending` by its approver or `access_admin`,
  `required: [f_delegate]`, `sets: {f_approver: 'f_delegate'}`, guard
  `f_delegate != f_approver` (to oneself is `GUARD_FAILED`). `f_approver` is
  `unique_among_siblings: [pending, approved]`, so to someone who already
  holds a live approval on the request is `NOT_UNIQUE`. A delegate set any
  other way breaks the invariant `delegate_not_approver`. The history shows
  the move, by whom, and the new approver.
- **Cancel** is `allowed: [nobody]`: only the request's reactions close one.
- `approver_missing` refuses an approval made by hand (`f_approver` is the
  kernel's).

**fulfilment_task@1.** `open → in_progress → done`, `role:desk_agent`;
`f_resource` is the kernel's, and `resource_missing` refuses one made by
hand. Desk agents find it by its Type and state.

**What the requester cannot do** (scenario E7, the item service tests):
approve her own request; write the copies or the resource's fields; create
an approval naming herself or none; move or narrow an approval, the request
or the resource. The weakness of PR 1 (#8 and Q4c) stands: an approval
handed over to the requester waits for `access_admin`.

**Visibility.** Requests are not narrowed: the whole namespace sees who
asked for what (Later: groups as readers). Approvers find their approvals in
the generic web Inbox: approval@1's decisions name `field:f_approver`, so
the Inbox asks for "approvals in pending whose f_approver is me", with no
code that knows the access request.

### Hardening after Jira 2.4, as built: who places Items, and goals no person moves (2026-11-22)

The two high-priority holes Jira 2.4b left on the Later list, closed in new
Type versions, with **no kernel change**: any principal who saw an incident,
a reply, a note, a comment or a goal could move it or narrow it (hiding it
from the desk, or a reply from its conversation), and anyone who saw a goal
under sla_goal@1 could complete it by hand, as met.

**Which versions carry a placement rule (`placement_writable_by`), and why:**

| Version | Placed by | Why |
|---|---|---|
| incident@3 | `role:desk_agent` | Agents move an incident to another desk and narrow it; its requester does not (a customer never updates anyway). Otherwise incident@2. |
| reply@2, note@2 | `kernel` | A reply or a note stays in the incident it was written in, with the incident's readers. |
| comment@3 | `kernel` | The same for a comment (story, ticket, incident@1). |
| sla_goal@3 | `kernel` | A goal stays in its host, with its host's readers: no one hides a goal from the desk. Otherwise sla_goal@2: every move `allowed: [nobody]`, `metric_missing`. |

incident@3 starts sla_goal@3 goals. sla_goal@3 accepts every reaction the
incident aims at its goals (its starts, and its pause, resume, stop,
cancel and answered moves) by reaction ID through containment, and the
incident accepts each of the goal's set reactions for exactly the field it
sets; a load test checks both. PLAT's work Items (story, bug, task, epic,
sprint, space) are unchanged: who places them, and whether narrowing an
Item at its creation is placement, are open (Later).

**Existing Items keep their version's rules.** The kernel has no operation
that moves an Item to another Type version: an incident@2 created before
this step is still narrowed by its requester, its sla_goal@1 goals are
still completed by hand by anyone who sees them, and a reply@1 is still
moved. Moving Items to a new version is a kernel change (Later, before
real use).

**Which version a client creates.** A client names the version it
creates. The **generic web UI creates the latest version** of the Type
chosen (staff may pick another in its Version list), so it went to
incident@3, reply@2 and note@2 with no code change. The PoC apps, the
scenario (C) and the web e2e tests pin versions, and were switched:
apps/poc creates incident@3, reply@2 and note@2, and its queue lists
incident@2 and @3 together (incident@1 is left to the admin UI). It reads
goals by Type ID whatever their version, so its Breached view and SLA panel
needed no change. Scenario B keeps the @1 Types.

## Web UI as built (2026-11-18)

A generic web UI for playing with the proof of concept, in `web/`. It is
not the real UI: it is a second client of the public API, beside `pli`,
and it tests the claim that the API and the Types are enough to build one.
It needed no kernel change, no proto change and no new call.

**Generic, from the Types.** No screen names a Type. Forms are built from
`GetType`'s fields, one input per field type; a field its state freezes or
that only the kernel writes is read-only, a role-written field is editable
and names its roles. Transitions are the Type's, offered whatever their
rules say: `allowed`, `except`, `guard`, `required` and `clears` are shown
as text and **never evaluated in the UI**, which shows the server's
outcome or refusal (code, reason, metadata) instead. One label is read
from the Type: a transition whose `allowed` names only
`principal:system:...` is greyed out. The inbox is every transition whose
`allowed` names `field:X`: the Items in its from-state where X is the
caller. A board is the Items linked to one Item by one link, ordered by a
string field.

**What the Types do not say** (and the UI does without): a field has no
label and no `required` of its own; links an Item can receive from other
Types are found by scanning `ListTypes` (a sprint declares nothing); there
is no "who am I".

**Rank stays app code, now in two languages.** `web/src/rank` is a port of
`internal/rank` (keys and `Plan`), held to it by golden vectors that the Go
tests write and both suites read (`internal/rank/testdata/vectors.json`).
A drop is one write, planned against the moved card's container through
`ListItems`, as `pli rank` does. The UI never rebalances; past
`rank.Threshold` it says the container needs one.

**Sets (Jira part 2, step 2).** A multi-value field is a row of chips
with remove buttons and one add input (a select for an enum, a date input
for dates); at its `max` the add is disabled. The form holds the members;
a save sends what changed as `{add, remove}`. A save made only of set
operations goes **without an etag**: they commute with other people's
changes. A save that also changes a plain field keeps the etag, and can
be `STALE`. The history shows what was asked ("changed ...").

**The form and the etag (the etag step).** The etag no longer changes
when the kernel writes a field only it writes, so the Item page keys its
form on the etag, the state **and the fields**: a refresh that brings a
new due instant rebuilds the form. And a save never sends a field the
user cannot write, whatever the form holds: a kernel-written field, or a
field reserved to roles the acting user does not hold (the page knows the
dev user's roles; such a field is read-only, with a hint naming the role).
A save made on a page opened before the kernel wrote goes through without
a refresh; a real conflict still shows `ABORTED / STALE`.

**Customers (Jira part 2, step 4a).** The act-as picker offers the dev
file's customers, labelled "(customer)". A customer meets every Type as
it is shown to them (`customerView`): its customer fields, its listed link
names, and only the moves whose `allowed` names `creator`; New Item offers
the Types the container admits (`own` and `shared`), nothing at the top
level, and no readers. Still read from the Types, never decided in the UI:
the server refuses the rest. `#/items` alone is the top level, as `#/` is.
A customer's view of a move keeps only the `field:X` entries whose field
they see, so their Inbox never searches on a field hidden from them.
A page titles an Item by its Type's **first string field**, so desk@2
declares `f_name` before `f_key`: a portal is shown by its name.

**Serving.** A `web` container serves the built site and proxies `/api` to
the item service: one origin, no CORS on the item service. Nothing updates
by itself (there is no `Watch`): pages reload after an action and on
demand.

**Dev only.** Acting as a dev user needs the dev tokens in the browser:
the stack mounts the dev-token file read-only into the web container,
which serves it, and the picker is compiled in only by the stack's build
(`VITE_DEV_USERS=1`). Because that port hands out every dev token, the
stack publishes the web UI and the API on 127.0.0.1 only. Real sign-on
replaces the picker as it replaces the API's interceptor.

## PoC apps as built (2026-11-21)

**PoC apps: a product-shaped client.** `apps/poc` is a third client of the
public API, beside `pli` and the generic web UI: three apps (and, since
2026-11-22, Requests, below) whose screens
know their Types, the way a product would. A service desk for agents
(`desk@2`, `incident@2`, `reply@1`, `note@1`, `sla_goal@1`), a customer
portal on the same Types, and a tracker for the PLAT Types (`space`,
`story`, `bug`, `task` at any version, `sprint`, `epic`, `comment@2`). The
generic UI stays as it is, the admin view. No kernel, proto or Type
change, and no new call: everything it needed is a list of gaps below,
each worked around in the app. It is not the real UI either (Out of scope).

**What the apps know, and what they do not decide.** They know their
Types' words: state names (a customer's own, "Received", "Waiting for
you"), field and option names, move labels ("Start work", "Send to
review"), urgency in plain words for the portal. They still read from
`GetType` which moves exist, which fields a move requires (asked for in a
dialog before the call) and which moves a customer may take (`allowed`
naming `creator`); they evaluate no guard, `allowed` or invariant. A
refusal is said in plain words chosen by its reason (a screen may add the
sentence for a guard it knows: "An epic is done only when all its issues
are done or canceled."), with the code, reason and metadata behind
"Details". A drop on a board that the Type does not declare is refused in
the app before any write: the only refusal the app makes, from the Type's
declarations.

**Service levels come from the goals.** The queue sorts by the due instant
a goal copies onto its incident and shows a countdown on the wall clock;
Breached is the goal's `f_breached_at`, Met or Missed its `f_outcome`, never
worked out in the browser. The app adds one state: Overdue, between the due
instant and the breach the goal records (the timer has not run yet). In the
queue, Paused comes from the incident's state (waiting for the customer,
with no resolution due instant shown): the queue reads no goal for it. The
incident page and its timeline read every goal and its history.

**A reply and a note are never confused.** The composer has two tabs; the
tab changes the colour, the label, the hint ("kim will see this", "Only
staff can see this"), the button and the Type written (`reply@1` or
`note@1`). Notes are drawn amber with a lock in the conversation.

**The portal is built on what a customer receives.** No etag (a customer
transitions without one), `commit_seq` never read, `min_seq` never sent,
filters on Types and categories only (never a field or link name, which
the server refuses for a name the customer's version does not list). Its
journey scans every API response kim receives: no note or `sla_goal` Item,
no assignee or due-instant field, none of their values. Type definitions
(`GetType`) are left out of the key and Type checks, as Types are readable
by anyone; values are checked in every response. An Item's creator is part
of what a customer sees: replies show "sam · Support".

**The board.** Columns by state, rank order inside each. A drop inside a
column is a rank move; across columns it is the transition first (with its
required fields), then a rank write next to a neighbour, **only once the
transition succeeded**: a refused move writes no rank. Ranks are web/'s
port (`web/src/rank`, imported read-only, held to Go by the vectors)
against the card's container, as `pli rank` does. The keyboard moves a card
too (Space, the arrows, Space), announced in a live region.

**Issue keys are a stand-in** until the kernel has its own (Later): the
space's key and the **last** 8 characters of the Item's ID. An ID is a
UUIDv7, whose first characters are a timestamp shared by every Item made in
the same moment.

**Serving and dev-only parts** are web/'s: a `poc` container (nginx, `/api`
proxied to the item service) on 127.0.0.1:8091, the dev-token file mounted
for the sign-in screen, the sign-in list compiled in only with
`VITE_DEV_USERS=1` (CI checks a build without it holds no dev-user code).
Light and dark colour tokens are held to WCAG AA by a test, and axe checks
each journey's screens.

### PoC apps as built: what the API lacked

Each gap was worked around in the app, without a kernel change; what the
workaround cost is the case for a call or a field later (journal, Later).

1. **No "who am I", roles or display names.** People and roles come from the
   dev-token file; a person is shown by their ID with an initials avatar.
   *Cost:* the apps depend on a dev-only file for who may open the service
   desk and whom to offer as an assignee; real sign-on must bring both.
2. **Rollups come with `GetItem` only.** *Cost:* the epics list makes one
   extra read per epic after listing them.
3. **Search and list results carry no links.** *Cost:* a backlog row cannot
   say its sprint or epic; the app reads each open sprint's issues with a
   link search to know which issues they hold.
4. **No search within a container, and no "has no link" filter.** *Cost:*
   the backlog is the space's open issues minus those the open sprints hold,
   worked out in the browser (a page can come back short); a word search
   reads the whole namespace and drops other spaces' issues in the browser,
   so its page can come back short too.
5. **No container filter for the goals.** *Cost:* the queue's Breached state
   is one more search per read, of every open breached goal the agent can
   see, matched to the page's incidents by container; the incident page
   reads its goals and one history per goal.
6. **No `update_time` on an Item, and no sort by it.** *Cost:* no "recently
   updated" order and no "last activity" column; only histories say when.
7. **No issue keys.** *Cost:* a stand-in key from the Item's ID, which
   nobody can type or search for.
8. **No Watch.** *Cost:* each visible page reads again every 30 s (several
   calls for an incident: the Item, its children, its history and one per
   goal), and a change made elsewhere shows up to 30 s late.
9. **`incident@2` has no description field.** *Cost:* a request is two
   writes, the incident then its description as the requester's first
   reply; if the second fails the request exists without it (the page asks
   for it); and the reply leaves two `reaction_skipped` records staff can
   see. A new incident version can add the field (Later).
10. **Refusal messages are fixed and technical.** *Cost:* the app keeps its
    own sentence per reason (18 of them) and per known guard; a reason it
    does not know falls back to a generic sentence and the details.
11. **No totals.** *Cost:* a view past one page says "50+", not a count.
12. **Due instants are working time on a calendar the app does not read.**
    *Cost:* countdowns are wall-clock time and must say so ("(wall clock)" in
    a table's column header, on each value on a detail page).

### The Requests area (2026-11-22)

**A process app on the kernel.** A fourth area of `apps/poc`, on
`vacation_request@3` and `leave_balance@1`, to see how a BPM-style app
feels before the real Bonita processes arrive. Built on the public API
only, like the other areas: no kernel, proto or Type change. **Who sees
it:** every staff member, as the last area: the vacation request names no
role for asking (`creator`) or deciding (`field:f_approver`, whoever the HR
check names; dana decides bob's without a role; bob's `approver` role is the
ticket's). Its **Balances** screen is for those who hold `hr_admin`
(leave_balance's only role). Customers never see it.

**The screens.**
- *My leave*: the balance (allowance, used, left, days waiting for
  approval), my requests with their status in the employee's words
  ("Waiting for bob", "Checking with HR", "Being approved"), and why a
  waiting request's approval was refused.
- *Request time off*: dates, days, leave type, reason. Days follow the
  dates (weekdays, no holiday calendar) until the person types in the
  field; then a date change leaves them and shows the count as a hint. The
  form checks blanks only. Sending creates a draft, then asks to move it to
  submitted, waiting up to 5 s for the HR check; a refusal keeps the draft,
  and the next send moves that same draft with the form's fields.
- *Waiting for you*: the requests whose approver I am, oldest first by when
  they last entered submitted, with how long each has waited (wall clock).
- *The decision*: the request, the requester's balance (or why it cannot be
  shown), their other requests in the same period; approve or reject with
  a comment (needed to reject).
- *The case*, for everyone: the facts, the viewer's moves, the process map
  and the timeline.
- *Balances* (`hr_admin`): every balance HR reads, with days waiting.
  Read-only.

**The process map is drawn from `GetType` alone**, so it is generic over
the Type (it draws the ticket, the incident and the PLAT Types too, and
should serve the Bonita processes). States are boxes in category order, one
column per category (triage, backlog, todo, active, waiting), with the end
states (done, canceled, none) sharing the last column. Moves are
right-angled arrows: to the next column through the gap, skipping columns
along a track below the boxes, going back along a track above, inside a
column in a loop on the right; each arrow has its own port, lane and
track, so no two arrows that share no end run along each other (a test
holds every Type to it). Labels say the move and who may take it, read
from `allowed` and `except` ("Approver (not the requester)"). **System
steps** are numbered on the map and listed under it: a checked move, a
state timer ("After 5 days"), an acting timer ("Reminder to the approver
after 2 days, then every day, 2 at most"), a link the kernel sets ("Links
the requester's leave balance"), a reaction ("Adds Days to Used on the
balance, before the move applies", "Gives back what Deduct took"). A move
no person makes (`principal:system:*`, or `allowed: [nobody]` since Jira
2.4b) is dashed, and is never offered as a button anywhere in the apps.
The current step is highlighted, the steps done ticked, the moves made
drawn in the accent colour, all from the history. "The process as a list"
says the same in text. No diagram library: a state machine of ten states
does not need one; a larger Bonita process may.

**The timeline** says who did what when, from the request's history, the
decision comments inside it and what its reactions wrote on the balance. A
move the kernel finished (after the HR check, after the deduction) is said
as the person's who asked for it (`on_behalf_of`); what the kernel did by
itself is a system step: who the HR check named, the balance it linked,
reminders, emails, days taken and given back, expiry.

**What the app decides, and what it does not.** It warns before sending
when the days are more than the days left, and refuses nothing: the server
refuses. A refusal is said in plain words by its reason and, for the date
rule (`GUARD_FAILED`), the part that failed is named from the values sent
("The last day is before the first day."): an explanation of the server's
refusal, never one the app makes. A decision carries the **etag** the
approver read: a request recalled, edited and sent again meanwhile comes
back `STALE`, is read again, and nothing applies. Verified against the
stack: two reminders and a refused approval leave the etag as it was. The
comment is a `comment@1` inside the request, written only once the
decision has applied. A case page offers the moves whose `allowed` names
the viewer plainly (the requester, the person a field holds, a role they
hold, anyone), never a system move; guards and `except` stay the server's.

**The dev clock.** The stack's worker runs `vacation_request` with a day of
2 s (a submitted request expires after 10 s): right for the scenario and
the journeys, too fast for a person. `compose.demo.yaml` (`docker compose
-f docker-compose.yml -f compose.demo.yaml up`) makes a day 2 minutes:
reminders at 4 and 6 minutes, expiry at 10. The journey keeps the fast
clock: bob's tab is open before ana sends, and each of the two requests is
decided within its 10 s.

**Learned from the stack:**
- `GetType` marks a move **checked** when a held reaction holds it, not
  only when a check hook does: Approve is checked (the deduction holds it),
  so the map says so beside the deduction.
- An action hook's record (`hook_ran`) carries the root human as
  `on_behalf_of` (bob on the approval email, ana on the reminders): a
  timeline must not read it as that person's act.
- `order_by` takes no principal field: Balances sorts by owner in the
  browser.
- A balance write is tied to its case by causation: its `causation_id` is
  `<request id>:<item_seq>`, the deduction's the approval's ask
  (`transition_requested` toward approved), the refund's the move into
  withdrawn. The case keeps a balance write only when its cause is in the
  request's history and moves, or asks to move, into the reaction's state.

#### The Requests area: what the API and the Types lacked

Each gap was worked around in the app, without a kernel, proto or Type
change. The four marked **Bonita** are in the journal's Later list as needed
for the real processes.

1. **`GetType` lists no hooks** (**Bonita**). It says a move is `checked`,
   names an acting timer's hook, and lists reactions; an action on entering
   a state (the emails) and a check's name are invisible. *Cost:* the map
   shows an unnamed check and no emails; emails appear only in the timeline,
   after they ran (`hook_ran`), in words the app keeps per hook ID. *A real
   process needs:* hooks in `GetType` (id, trigger, kind, action, state).
2. **No balance check at submit, no reservation for pending days**
   (**Bonita**). The balance is checked only when an approval deducts it.
   *Cost:* the form can only warn; the refusal comes at approval, to the
   approver, and the employee learns it from the request; two requests that
   do not both fit can both wait for approval (the app sums the days
   waiting, for display only). *A real process needs:* a check at submit
   and a reservation, released by a rejection or an expiry.
3. **No decision comment** (**Bonita**). `f_comment` is the requester's
   reason; the approver may write it, which would overwrite the reason.
   *Cost:* a `comment@1` inside the request, a second write after the
   decision; if it fails, the decision stands without it (the approver is
   told); the timeline shows a comment once its author's move is in the
   history. *A real process needs:* a decision field only the approver may
   write.
4. **No filter on the creator.** *Cost:* My leave reads every request the
   person can read (three pages of 500, newest first) and keeps theirs; past
   that it says older ones are not shown. The decision screen's "other
   requests in this period" and Balances' days waiting pick the requester
   out the same way. A link filter on the balance would find submitted
   requests, never drafts.
5. **No time-in-state, no `update_time`, no sort by create time.** *Cost:*
   the inbox reads one history per row to know when each request entered
   submitted, and orders one page of 500 in the browser.
6. **An approver cannot see the requester's balance** (**Bonita**).
   Balances are narrowed to their owner and HR. *Cost:* the journey's seed
   (HR) adds the manager as a reader; otherwise the decision screen says it
   cannot show the balance, and a refusal for lack of days comes without
   the numbers. *A real process needs:* readers derived from a field (the
   approver of a linked request joins the balance's readers), with groups
   as readers (Jira 2.4b's Later item).
7. **Every staff member can read every request** (dates and reason): the
   approver is known only after the HR check, so requests are not narrowed
   (as in the scenario). *Cost:* none in the app; a privacy hole. The same
   readers-from-a-field would close it.
8. **The deduction lives on the balance**, not on the request. *Cost:* the
   case page reads the balance's history and ties writes to the case by
   causation; someone who cannot read the balance sees "Days given back"
   from the request's own record, and no amounts.
9. **Unpaid leave is deducted too**: the deduction has no condition on the
   leave type. *Cost:* the form says so when Unpaid is chosen.
10. **No working-day calendar, and `f_days` is not checked against the
    dates.** *Cost:* the form suggests the weekdays, which the person may
    change; HR and the app can disagree on a request's length.
11. **No Watch.** *Cost:* pages read again every 30 s; on the dev clock an
    inbox can show a request that has since expired. Deciding it comes back
    `STALE` (the etag changed with the state) and the page reads it again:
    the etag keeps an approver from deciding a request that changed since
    they opened it.
12. **No "who am I", no display names** (as for the other areas). *Cost:*
    the dev-token file decides who sees Balances; before a request is sent
    the form can only say "your manager decides": the HR check names them.

## Out of scope for the spike

The real UI (the generic web UI and the PoC apps above are
proof-of-concept clients, not it), email intake, multi-tenancy (the
`namespace_id` column exists, with one default namespace), a dedicated search
engine. (Business-hours calendars and SLAs moved into Jira part 2.)
