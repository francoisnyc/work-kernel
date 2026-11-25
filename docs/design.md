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

## Architecture style

- **Current state plus an event log, not event sourcing.** The Item row is the
  truth. `item_events` records every accepted operation, for history, audit and
  notification; state is never rebuilt from it, and events carry shapes and
  opaque IDs, never field values.
- **A modular monolith.** The item service owns every rule. Every other process
  (relay, worker, and later the event reader) exists for a technical reason
  (durable timers, ordered delivery), never to split the domain.
- **A reporting database apart from the operational one.** Analytics read a
  copy (a warehouse fed by change data capture), never the kernel's Postgres
  directly, and the copy is held to the same erasure rule.
- **A semantic model apart from its syntax.** The `typedef` structs are what a
  Type means. YAML, a builder or plain English are ways of writing one; checks,
  readings and the semantic diff work on the model, whichever wrote it.
- **Hooks hang off the lifecycle, never off "any change".** A hook fires on a
  transition (a `check`, before commit) or on entering a state (an `enrich` or
  `action`, after commit), so reading a Type shows every call it makes. No
  trigger on "any field changed" and no exit hooks: when something must happen
  because a field changes, that change becomes a named transition with its own
  rules. Systems that fire on any change end with rules triggering rules.
  Outbound webhooks (`docs/roadmap.md`, theme 5) are subscribers to the event
  stream, not hooks, and change nothing.
- **An effect re-checks what caused it.** A workflow's input is a request,
  never proof. Before an action with a side effect (a contract with
  `sideEffects: some`) calls its connector, the activity asks the item
  service to confirm the cause it names: that event exists on that Item, at
  the Type version named, and it is the event the hook is bound to (the entry
  into its state, or the acting timer's run). A cause that does not check out
  is refused for good, recorded on the Item, and nothing is sent. Found by
  the first bake-off (`docs/research/camunda-bakeoff.md`): a forged workflow
  start made an action place an order for an Item still waiting on its
  manager.

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
  (see "Approval as a business state"), or a list of its writers on the
  public door: `people`, `agents` and `role:<name>` (see "Link reactions,
  the leave balance and the saga" for roles). **Without `writable_by`, a
  field is people's only: no agent writes it unless its Type opens it**
  (see "Fields closed to agents by default, as built"). A Type may declare `invariants`, rules
  over its fields that every write keeps, and `reactions`, writes on the
  Items it links to. It may name the links other Types declare towards it
  (`incoming`) and declare `rollups`, counts computed when an Item is read
  (see "Jira part 1 as built").
- States: each has a category — `triage, backlog, todo, active, waiting, done, canceled, none`.
  `none` means "not work": work views leave it out. A Type that declares no
  states gets one implicit state, `default`, with category `none`. A state may
  declare `frozen: [fields]`: no write changes them while the Item is in it.
- Transitions: from/to, guard expression, required fields (and those of
  them the move itself must give, `given`: see "Required means given by
  the mover, as built"), fields to clear
  (`clears`, see "Jira part 1 as built"), fields to compute (`sets`, see
  "Jira part 2, step 1 as built"), who may act, action references.
  - Who may act is the transition's `allowed` list. An entry is `role:<name>`,
    `principal:<id>`, `field:<id>` (whoever a principal field holds when the
    move runs) or `creator` (the requester: see below). Absent or empty means
    any staff member who can see the Item, never a customer: a customer
    takes only the moves whose `allowed` names `creator` (Jira part 2, step
    4a; corrected here by Ventoux V6, whose reading of it a witness holds).
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
`internal/hooks` or a connector service, never in the Type) with `reads` and
`writes`, matched to the action by position. Since Ventoux V1 every action
has a contract in `contracts/`, and a hook is checked against it at load:
its arity, its kind, and the types of what it reads and writes (see
"Ventoux V1, as built: action contracts"). Since the hook
protocol, an action is `<connector>.<operation>[@<major>]` (no major means
1), and the connector is compiled in or a remote service, by configuration:
see "Hook protocol (pli.hooks/v1), as built". Besides fields,
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
  an action hook may write it. (Two more reasons came later: `no_recipient`,
  Jira part 2, step 3b; and `cause_not_confirmed`, which only the item
  service's `ConfirmCause` writes: see "An effect re-checks what caused it,
  as built".) Its write-back's key coming back with a
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
    stored; v1 enforces read versus any write power, except `Create`,
    which needs `create`: without one, every write is `NOT_ALLOWED`, and
    without `create`, every create is.
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
| `ValidateTypes` | Check draft Types against every registered version; registers nothing ("Ventoux V2b and V2c, as built") |
| `ListActions` | The action catalogue: every action a hook may name, with its contract ("Ventoux V1, as built") |
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
- `waiting_on` (`me`, or `role:<name>`): only the open Items that wait on
  the caller, or on a role; one more condition, with the others. See
  "Waiting on me, step 1, as built".
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
field, never a value or an ID: a refusal must not repeat what someone
typed into a field. A request's own count and size are not that, so a
refusal over a limit may state them, as the draft limits do ("this
request has 214 sources, 1.3 MiB"). A test fails if a message contains an ID.
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
| `ErrForbidden`, a write names a field it may not write (kernel-written, closed to the writer by `writable_by`, e.g. to an agent by default, limited by the Item's state to its requester (`requester_only`), or named by its transition's `allowed`) | `PERMISSION_DENIED` | `FIELD_NOT_WRITABLE` (+ fields) |
| `ErrRejected`, a write names a field the Item's state freezes | `FAILED_PRECONDITION` | `FIELD_FROZEN` (+ fields, state) |
| `ErrRejected`, a transition's required field is not set, or one its `given` lists is not given in the move itself | `FAILED_PRECONDITION` | `MISSING_FIELDS` (+ fields, from_state, to_state); its message: "the transition needs fields that are not set or not given in the move" |
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

### Type problems

A Type definition is checked in full when it loads (`typedef.Parse`,
`FromJSON`, `ParseDir`), and every problem comes back as data
(Ventoux item 2a), not as the first one in a string:
`{source, path, line, column, severity, code, message}`.

- `path` points into the YAML: `transitions[3].allowed[0]`,
  `accepts_reactions["part:finish"]`. `line` and `column` are the key's
  when the path ends in a key, the item's when it ends in an index, and
  the nearest enclosing node's when the path names something missing (0
  when unknown: a stored definition, a syntax error's column).
- `code` is stable and documented below; tools match on it. Codes are the
  kernel's, not declared in Type YAML.
- `message` says what is wrong, then what to do. Unlike a refusal's, it
  may name fields, states and Types: a definition holds no personal data.
- `severity` is `ERROR` for now; warnings (Ventoux item 3) will not stop
  a load.

Checking runs in three stages, and a stage runs only when the ones
before it found nothing:

1. **The YAML**: syntax, unknown keys, values of the wrong shape.
2. **Each Type on its own**: every declaration is checked, and a
   declaration with a problem still counts as declared, so nothing that
   only follows from it is reported.
3. **The Types together** (`ParseDir`): links, inverses, reactions and
   consent, shapes across versions, a version declared twice. **This stage
   runs only when every Type is sound on its own**: a tool repairing a
   draft fixes the single-Type problems first, then sees these.

As a log line, a problem reads
`<source>:<line>:<col>: <CODE> typedef <id> v<n>: <message>`.

The same stages check drafts against the registered versions
(`ValidateTypes`, `typedef.Check`; "Ventoux V2b and V2c, as built"):
stage 3 then also runs with every registered version, and adds
`VERSION_CHANGED`.

| Code | Stage | Meaning |
| --- | --- | --- |
| `YAML_SYNTAX` | 1 | The file is not YAML |
| `UNKNOWN_KEY` | 1 | A key the Type format does not have (often a typo) |
| `WRONG_SHAPE` | 1 | A value of the wrong kind: text for a number, a map for a list |
| `REQUIRED` | 2 | Something that must be there is missing (an ID, a link's inverse) |
| `DUPLICATE` | 2 | Declared twice: a field, a state, a transition, an ID, a list entry |
| `RESERVED_NAME` | 2 | A name the kernel keeps (today, caller, links, ...; a Type named top, which start.in uses for the top level) |
| `INVALID_NAME` | 2 | A name or ID that is not well formed (lower case, digits and _) |
| `INVALID_VALUE` | 2 | A value outside what the key allows (a category, a field type, a version below 1, a duration that is not positive) |
| `KEY_NOT_APPLICABLE` | 2 | A key that does not apply here (options on a string field, a fallback on a check) |
| `UNKNOWN_FIELD` | 2 | Names a field the Type does not declare |
| `UNKNOWN_STATE` | 2 | Names a state the Type does not declare |
| `UNKNOWN_TRANSITION` | 2 | Names a transition the Type does not declare |
| `UNKNOWN_LINK` | 2 | Names a link the Type cannot read |
| `UNKNOWN_HOOK` | 2 | Names a hook the Type does not declare |
| `UNKNOWN_REACTION` | 2, 3 | Names a reaction that does not exist (or reaches nothing) |
| `UNKNOWN_ACTION` | 2 | A hook's action is not in the catalogue (`contracts/`, `ListActions`) |
| `ACTION_ARITY` | 2 | A hook's reads or writes do not match its action's |
| `ACTION_KIND` | 2 | A hook of a kind its action does not serve (a check naming an enrich action) |
| `WRONG_FIELD_TYPE` | 2, 3 | A field of the wrong type for this use (an at-timer on a string; a hook reading an int into a string input) |
| `EXPRESSION_INVALID` | 2, 3 | A CEL expression that does not compile, or a form not allowed (a computed principal) |
| `EXPRESSION_TYPE` | 2, 3 | A CEL expression that yields the wrong type (a guard that is not a bool) |
| `READ_NOT_ALLOWED` | 2, 3 | An expression or reaction reads what it may not read here |
| `WRITES_FROZEN_FIELD` | 2 | Something writes a field a state freezes |
| `WRITES_KERNEL_FIELD` | 2 | Something other than a hook writes a field only the kernel writes |
| `HELD_TRANSITION` | 2, 3 | A held transition, or a move into one, does what a hold forbids |
| `CONFLICT` | 2, 3 | Two declarations contradict each other (requires and clears a field; own or shared naming a Type whose latest start.allowed is [nobody]) |
| `ONLY_ONE_ALLOWED` | 2 | A second one where only one is allowed (two checks on a transition) |
| `MISSING_COUNTERPART` | 2, 3 | A declaration needs another that is missing (a kernel field no hook writes, a held reaction with no undo) |
| `TIMER_NOT_ALLOWED` | 2 | A timer's transition does not allow `principal:system:timer` |
| `UNKNOWN_TYPE` | 3 | Names a Type, or a Type version, that is not loaded |
| `CONSENT_MISSING` | 3 | A reaction moves, creates or writes Items of a Type that does not consent (`accepts_reactions`) |
| `INVERSE_CHANGED` | 3 | A link name has another inverse in another version (reported on the later one) |
| `INVERSE_MISMATCH` | 3 | An incoming declaration does not match the link it names |
| `SHAPE_CHANGED` | 3 | A field holds one value in one version and several in another |
| `REACTION_CYCLE` | 3 | Reactions form a cycle (reported on the first reaction of the cycle, one cycle at a time) |
| `DUPLICATE_VERSION` | 3 | Two files declare the same Type version (reported on the second) |
| `FOLDER_TOO_DEEP` | 3 | A folder more than one level below the Types folder |
| `VERSION_CHANGED` | 3 | A draft declares a registered Type version with another definition (`ValidateTypes`; reported on the draft's `version`) |
| `START_NOT_IN` | 3 | Something creates Items of a Type where its latest version's start.in does not let them be created: a container's own or shared entry, or a create reaction (starting work, step 2) |

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
affected. An agent that holds the role writes the field: a role is how a
deployment opens a field to a particular agent. Since fields closed to
agents by default, the list may also name `people` and `agents`; see
"Fields closed to agents by default, as built". On `leave_balance`, `f_owner` and `f_allowance` are `hr_admin`'s
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
| Hook protocol (**done**) | `pli.hooks/v1`: the envelope, outcomes, error classes, Standard Webhooks signing, the descriptor and the major check, a connectors file the worker follows; the HR directory moved out of the worker as the first remote connector | The vacation scenario passes unchanged against the remote HR; its HR tests pass against it; a conformance test any connector runs; HR 1.4 switched to 1.5 while a request waits, with no restart and no Type change. **Done 2026-11-22**: "Hook protocol (pli.hooks/v1), as built" below, and the journal |
| Bonita | The company's real Bonita processes, mapped against the BPMN pattern table, then rebuilt | Planned after Jira 2 |

## Ideas from Ventoux build order

See `docs/plans/ideas-from-ventoux.md`.

| Step | Build | Done when |
| --- | --- | --- |
| V1 (**done**) | Action contracts: each hook action declares `name@version`, typed reads and writes, side effect, risk and the reason codes it may answer (idempotency is the protocol's, for every connector); hooks name `action@version` (no major means 1, as built); the catalogue is readable through the API. The hook protocol work (`pli.hooks/v1`: the descriptor and the compiled-in declaration `hr.manager_of@1`) is the first version of these contracts | Every action has a frozen contract in `contracts/`; a hook of the wrong kind, or reading or writing a field of the wrong type, is refused at load (`ACTION_KIND`, `WRONG_FIELD_TYPE`); every connector's answers are held to its contract; `ListActions` and `pli action list`; CI refuses a published contract that changed. **Done 2026-11-23**: "Ventoux V1, as built" below, and the journal |
| V2a | Every problem of a Type or a folder of Types as data: `{source, path, line, column, severity, code, message}`, in three stages (the YAML, each Type, the Types together), with no cascades; the codes in "Type problems" | Each code has a case with its exact path and position; a folder reports every file's problems at once; a source walk finds no problem without a code and a what-to-do; every test written before V2a passes unchanged |
| V2b (**done**) | `ValidateTypes`: draft Types checked against every registered version, nothing written; `pli type validate`; exempt from `policy.Project` like `ListTypes`; users and agents (in a session), not customers; 1 MiB and 200 sources at most (raised from 50 and 256 KiB for build_step, 2026-11-24) | A draft with problems is a successful call listing them; an identical registered `id@version` passes, a different one is `VERSION_CHANGED`; pli prints `source:line:col: CODE message` and exits non-zero on errors. **Done 2026-11-22**: "Ventoux V2b and V2c, as built" below, and the journal |
| V2c (**done**) | The Type format as JSON Schema, by hand, in a checked-in file; checked with `santhosh-tekuri/jsonschema/v6` (test only) | Every file in `types/` validates; every key the schema allows is a key `typedef` knows; the schema is never stricter than `typedef`. **Done 2026-11-22**: "Ventoux V2b and V2c, as built" below |
| V5 (**done**) | Stories in simulated time (item 5): a walkthrough as data (the Types, the people and their roles, a fake for every action from its contract, a timeline of one-line steps, the whole expected timeline), run as a Go test against the real Item workflow and item service in Temporal's test environment; `make stories`; the runner's `Parse`, `Check`, `Run` and `Compare` for item 6's witnesses | The vacation request (approve, expiry, refund, the HR outage and a denial) and the incident desk (reopen on a reply with a paused goal, breach) run in seconds; any unexpected event, call, refusal or read fails a story with a report a person can read; a fake cannot answer what its contract forbids; continue-as-new and a validator's refusal behave in the harness as on the server. **Done 2026-11-23**: "Ventoux V5, as built" below, and the journal |
| V6 (**done**) | Plain-language readings, each with a witness (item 6): every state rule, move, hook, reaction, timer, hold, link and visibility rule of a Type as one sentence, from the Type and the contracts it pins; a closed list of kinds of sentence and their clauses; a witness for each, a test that runs the engine through internal/story's `Run`; `ExplainType` (a registered version or a draft) and `pli type explain` | Every part has a witness, every key of the Type format is read or left out with a reason, every element of every Type in `types/` is cited by a sentence; condition words held to the engine's evaluation; `readings/vacation_request@3.txt` and `readings/incident@3.txt` approved by the owner and compared exactly. **Done 2026-11-24**: "Ventoux V6, as built" below, and the journal |

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
of a Type, **of every version: the latest registered version's rule judges**
(since starting work, step 2b; see "Starting work, step 2b, as built"):
`[kernel]` (no person; they stay where they were made) or
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
| comment@3 | `kernel` | The same for a comment, wherever it was written. |
| sla_goal@3 | `kernel` | A goal stays in its host, with its host's readers: no one hides a goal from the desk. Otherwise sla_goal@2: every move `allowed: [nobody]`, `metric_missing`. |

incident@3 starts sla_goal@3 goals. sla_goal@3 accepts every reaction the
incident aims at its goals (its starts, and its pause, resume, stop,
cancel and answered moves) by reaction ID through containment, and the
incident accepts each of the goal's set reactions for exactly the field it
sets; a load test checks both. PLAT's work Items (story, bug, task, epic,
sprint, space) are unchanged: who places them, and whether narrowing an
Item at its creation is placement, are open (Later).

**Existing Items keep their version's lifecycle, not its placement rule.**
The kernel has no operation that moves an Item to another Type version: the
sla_goal@1 goals of an incident@2 created before this step are still
completed by hand by anyone who sees them. Moving Items to a new version is
a kernel change (Later, before real use). **Corrected by starting work,
step 2b:** this paragraph used to say that an incident@2 is still narrowed
by its requester and a reply@1 still moved. Placement now follows the
latest version: only desk agents move or narrow an incident of any version,
and no one moves or narrows a reply, a note, a comment or a goal of any
version.

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

## Hook protocol (pli.hooks/v1), as built (2026-11-22)

Background: `docs/research/hook-protocol-versus-package-system.md`. Its
recommendation is taken: a small versioned wire contract for connectors
now, no package system. The spike proves it on one connector: the HR
directory (`hr.manager_of`) left the worker and became a service speaking
the protocol, with the vacation scenario unchanged and **no Type file
changed**. The CRM, ERP and email connectors stay compiled in, behind the
same interface. Code: `internal/hookproto` (the envelopes, signing, the
conformance test), `internal/hooks` (the worker side), `internal/connstub`
(the HR connector).

**Bindings.** Two, with the same envelopes: HTTPS (`POST {base}/hooks/v1`,
signed), and the Go interface `hooks.Connector`, `Handle(ctx, Request)
(Response, error)`. A compiled-in connector is an adapter over the existing
action code, so a Type cannot tell a remote connector from a compiled-in one.

**Names and majors.** A hook's action is `<connector>.<operation>[@<major>]`;
no major means 1, so every existing Type names `hr@1`, `crm@1`, `erp@1`,
`email@1`. The worker's catalog declares each operation: its kinds, its
input and output names in order (a hook's `reads` and `writes` map to them
by position, as before, so an operation never sees a Type's field IDs),
whether it has a side effect, and whether it is remote (no code in the
worker: `hr.manager_of@1`). Since Ventoux V1 the catalog is the contracts
in `contracts/`, with code attached to the compiled-in ones. A Type naming a major the catalog does not
declare is refused at load, as an unknown action is (a kernel change). A
new major needs a new Type version naming `@2` (specified, not built): Items
keep their version's major, and configuration may hold `hr@1` and `hr@2`
side by side until no `@1` Item waits.

**The request envelope.** JSON, lowerCamel, the same for every kind:

| Field | What |
| --- | --- |
| `protocol` | `pli.hooks/v1` |
| `kind` | `check`, `enrich` or `action` |
| `requestId` | new for every send; echoed in the answer |
| `idempotencyKey` | the hook's key (`hold:<id>` for a check, `hook:<item>:<seq>:<hook>` for an enrich or action hook), stable across retries and reruns, no personal data. A read-only call without a key (tests, the bench) uses its request ID |
| `attempt` | the activity's attempt |
| `deadline` | the activity context's deadline, absolute; left out when there is none |
| `hook` | `binding` (`vacation_request@3#hr_check`), `connector` (`hr@1`), `operation` (`manager_of`) |
| `event` | the cause: `id` (`<item>:<seq>`), `type` (`item.transition.requested`, `item.state.entered`, `item.timer.fired`, `item.reaction.applied`), `subject` (`item/<id>`) |
| `item` | `id`, `type`, `typeVersion`; never a field value |
| `transition` | a check's `from` and `to`; left out otherwise |
| `inputs` | the operation's declared inputs by name, nothing else (unset is `null`); the values come from `HookInput`, behind `policy.Project` |
| `trace` | `correlationId`, `causationId` |
| `connection` | `slot` (the connector name) and `ref` (the configured connection): a reference, never a secret |

Reserved, not sent: `actor` (who caused the event; Later), `traceparent`
(when the stack has tracing), `dryRun` (with action hooks over the wire).
Both sides ignore fields they do not know. Within v1 the envelopes only
grow: `internal/hookproto/testdata/v1.0` holds frozen fixtures that every
later version must read and write back unchanged (a test).

**Outcomes** (HTTP 200: a business answer, never retried):

| Kind | Outcomes in v1 | Payload |
| --- | --- | --- |
| check | `allow`, `deny`, `correct` | `reason.code` on deny; `patch` on correct (allow with values: the HR check writes the approver). `correct` is Pli's addition to the report's allow/deny |
| enrich | `ok` | `patch` |
| action | `ok` (specified; actions are not yet built over the wire) | `patch` |

`pending` (asynchronous) and `business_error` are specified below, not
built: today either is a permanent failure. A patch must give every
declared output and nothing else. A deny must name a reason the descriptor
declares (`errors`); an undeclared one, or none, is `connector_mismatch`,
so the hold still fails closed. A reason's `message` is logged, never
stored: only the code reaches the Item. The answer carries
`connectorVersion`, the implementation that answered.

**Failures** are RFC 9457 `application/problem+json` bodies with a `class`;
the status decides, unless the problem's `retryable` says otherwise:

| Status | Class | The worker |
| --- | --- | --- |
| 400, 404, other 4xx | `invalid` | permanent |
| 401, 403 | `config` | permanent: `connector_unauthorized` |
| 409 | `in_flight` (the key is being answered) | retried within the budget |
| 410 | `gone` | permanent |
| 422 | `key_conflict` (the key with a different request) | permanent, `ErrKeyConflict` |
| 429 | `rate_limited` | retried (`Retry-After` is not honoured yet: Later) |
| 5xx, a network failure, the deadline | `transient` | retried within the budget |
| 3xx | | permanent: a redirect is never followed |

An answer that breaks the protocol (another `requestId` or `protocol`, an
outcome its kind does not take, an undeclared output) is permanent. A check
that fails permanently resolves its hold as error at once; three causes have
their own reason, so a misconfiguration shows at once instead of using up the
budget: `connector_unconfigured` (no connection for a remote-only
connector), `connector_unauthorized`, `connector_mismatch`. The others stay
`check_failed`, and anything retryable still ends as `budget_exhausted`. An
action hook records any of the three as `action_failed`: `hook_failed`
takes four reasons (Later).

**Signing and replay.** Standard Webhooks v1.0.0, symmetric (`v1`),
written on the standard library: `webhook-id`, `webhook-timestamp` (unix
seconds), `webhook-signature: v1,<base64 HMAC-SHA256 of id.timestamp.body>`.
The secret is per connection, `whsec_<base64>`, read from the environment
variable the connectors file names; it never travels. `webhook-id` is
`msg_` and the first 16 bytes of SHA-256 of the idempotency key, in hex:
stable across retries, as Standard Webhooks wants, and without the key's
text; the key also travels in the envelope and the `Idempotency-Key`
header. A receiver checks the timestamp within five minutes either way,
accepts any `v1` signature matching any secret it trusts (rotation: several
signatures, several secrets), and skips entries it cannot read. Replay
protection is the tolerance plus the connector's success store: a replayed
request inside the window gets the stored answer and causes nothing new, so
Go's own resend of a dropped request is harmless too. The spec has since
deprecated `v1a` (which the report names) for `v2a`, ed25519 with an
endpoint ID: Later, with signed answers (today TLS covers them) and the
signed callback.

**The descriptor** is served unsigned at `GET /.well-known/pli-connector`:
`connector` (the name Types use), `version` (the implementation's semver),
`supportedProtocolVersions`, and `operations[]` with `name`, `kinds`,
`inputSchema` and `outputSchema` (a JSON Schema subset: an object with typed
properties; `type` read as a string or a list), `sideEffects` (`none` or
`some`), `async` (`forbidden`, `optional`, `required`) and `errors` (the
reason codes it may answer). The worker reads it once per URL, before the
first call there, and checks it against the catalog: the connector's name,
the major of its version, `pli.hooks/v1`, the operation, its kinds, and its
input and output names (since V1, the whole contract: types, side effects,
reasons). A mismatch fails at once (`connector_mismatch`)
and nothing is posted; a descriptor it cannot read is retryable and not
remembered. An answer whose `connectorVersion` has another major is a
mismatch too, and the URL's descriptor is read again on the next call
(another major deployed behind the same URL).

**Configuration and reload.** The worker's `--connectors FILE`
(`deploy/connectors.yaml` in the stack):

```yaml
connectors:
  hr@1: {url: "http://connstub:8083", secret_env: HR_HOOK_SECRET, ref: hr-directory}
```

A key is `<connector>@<major>`; a connector not listed stays compiled in.
Unknown keys, a bad key or URL, or a `secret_env` unset or not a `whsec_`
secret are errors naming the file. The worker reads the file at start (an
error stops it) and again on any call after its modification time or size
changes; a file that no longer parses keeps the last good one and is logged
once. So pointing `hr@1` at another implementation of the same major (HR
1.4, then 1.5) takes effect on the next call, with no restart and no Type
change: a test switches it while a request waits in its hold, between the
check's retries. Without `--connectors`, every remote-only connector is
unconfigured.

**Success-only stores.** A connector stores each success under its
idempotency key, with a digest of what was asked (operation, kind,
inputs). The same key asking the same again gets the stored answer, with
its own `requestId`; asking something else is 422; a key still being
answered is 409. A failure is never stored. The store must survive deploys
within a major (a shared store, not a process's memory): the stubs keep
theirs in memory, so the 1.4-to-1.5 test switches while every 1.4 attempt
had failed.

**Retry and rerun, and how they differ from the report.** A **retry** sends
the same request again: the same key, the inputs as first sent. A **rerun**
reads the inputs again, under the same key. Because stores keep successes
only, a rerun can never make a second effect: if every attempt failed,
nothing is stored and the rerun does the work, with corrected inputs if
someone corrected the Item; if one attempt succeeded and its answer was
lost, the stored success comes back for the same inputs, and other inputs
are 422 `key_conflict`, which is right: the effect happened with the old
ones. The report has a rerun mint a new key, audited, because it can create
a second invoice. Here a deliberate second effect means entering the state
again, which is a new entry and a new key. (Incidents, which offer retry,
rerun and skip, are the next step, below.)

**The conformance test** (`internal/hookproto/conformance`) is test code
any connector runs over HTTP, `conformance.Run(t, Target{URL, Secret,
Cases, Outage})`. It checks the descriptor's shape and that the cases fit
it; each case's answer (envelope, echoed request ID, an outcome valid for
the kind, declared outputs and reasons, the version the descriptor
reports); idempotency (a replay gets the identical answer, a resend the same
answer, other inputs under the key 422, and after a failure the key is
still free: success-only stores); signatures (unsigned, tampered, another
secret, six minutes old or ahead, only an unknown scheme: 401; unreadable
entries beside a good one: ignored); the protocol (another version, a
malformed body, a kind the operation does not take: 400; an unknown
operation: 404; unknown envelope fields ignored; every refusal a problem
body). A target without two answered cases of one operation, or without an
outage switch, is itself a problem: the test never passes by checking less.
Its own test runs it against six broken connectors and finds each.

**The HR connector** is connstub's HR server: connector `hr`, operation
`manager_of`, a check, beside its REST endpoint on 8083. It decides what
the worker's `hr.manager_of` decided before: the requester is `onBehalfOf`
if set, else `createdBy`; `correct` with the requester's manager, or `deny`
`unknown_employee` or `no_eligible_approver`; bad inputs are 400. It
verifies every request (`--hr-hook-secret-env`; without it, every hook
request is 401), keeps the stubs' knobs (latency, error rate, outage, a
dropped first answer), and its version can be set for tests.

**Specified, not built.**
- *Action hooks over the wire.* Only after the transition commits; the key
  reaches the connector, which stores successes only. `ok` with declared
  outputs and `externalRefs`; `business_error{code}` routed to a
  Type-declared error transition. `dryRun` must not actuate anything.
- *Asynchronous completion.* An answer `202 {outcome: pending, taskId,
  pollAfter, expiresAt, message}`. The connector completes with a signed
  `POST /hooks/v1/tasks/{taskId}/complete` carrying the original key and a
  normal answer, or the kernel polls `GET {connector}/tasks/{taskId}`. The
  callback completes the activity by workflow ID and activity ID
  (`CompleteActivityByID`): a connector never sees a task token. A task ID
  is bound to the connector that made it; a second callback is a no-op; one
  after the hold expired or was canceled is refused and recorded as a
  discard event, never applied. A heartbeat extends `expiresAt` up to the
  binding's `maxHold`; `cancel` is idempotent. A check answering `pending`
  is the visible hold the kernel already has.
- *Failure policy per binding, in the Type* (the report's table: timeouts
  by kind, `fail`, `ignore` audited, `degrade`). Today: a check fails
  closed, an enrich hook with a fallback degrades to it, an action records
  `hook_failed`.

**Namespaces later: the connector name as an alias.** The report wants
namespaced connector names (`acme.hr-directory`); renaming would change
Types. Instead, a Type's connector name can become a local alias that
configuration maps to a namespaced connector (`hr` to
`acme.hr-directory@1`), and the worker checks the descriptor's name against
the namespaced one. Bundles can then add namespaces without changing a Type.

**Packages: bundles, later.** No package system now. When one is needed, a
package is a **bundle**: a git directory, the single source of truth for one
namespace, applied with `pli apply`, holding YAML Types and connector
descriptors (vendored, pinned by digest), per-environment endpoints and
golden fixtures, and no code the kernel loads (connector code is at most an
image reference the operator deploys). Its rules: a mandatory namespace on
Type, field and connector names; one channel per namespace (a namespace a
bundle manages is not edited by hand in production); `apply` adds by
default, and a removal needs an explicit flag, deprecates rather than
deletes, keeps Items readable and reserves the name forever; version bounds
for Pli and the protocol; a lockfile of descriptor digests. Build a real
package system (dependency resolution, a registry, signing) only when one of
these becomes true:

1. Untrusted third parties ship connector code to other organisations
   through a shared catalogue.
2. Cross-bundle Type dependencies appear in practice (a shared
   `hr.employee` several bundles extend); even then, a "requires X >=
   version" check before a solver.
3. Pli is offered as a multi-tenant hosted service.
4. Extensions need to run code inside Pli's UI or transaction path (after
   exhausting CEL).
5. Bundles are installed across many independent deployments and upgrades
   start breaking Items in flight.

### Next step: incidents

What happens after a hook's budget runs out, so that nothing is dropped and
someone can act. Specified here; to build next.

- **Checks are unchanged.** A check fails closed: its hold resolves as
  error (`budget_exhausted`, or a connector reason), the request stays where
  it was, and the requester asking again is their rerun (a new hold, a new
  key). `TestE2E_HRCheck_OutageIsAnError` holds this.
- **An incident opens** on the Item for an action hook, and for an enrich
  hook with no fallback, when it gives up (its budget ran out, or it failed
  for good). An enrich hook with a fallback writes it, as today, and opens
  none. The action's `hook_failed` is still recorded; the incident is the
  open part. While it is open the Item says so (`Get`, history), and the
  action is not complete for its entry.
- **Three moves close it:**
  - *retry*: run the hook again with a fresh budget, the same key and the
    inputs exactly as first sent (kept with the incident). For a fault fixed
    since (the connector was down, misconfigured).
  - *rerun*: run it again with a fresh budget and the same key, reading the
    inputs again. For an Item someone corrected (a wrong customer ID).
    Success-only stores make it safe: see "Retry and rerun" above.
  - *skip*: close it without running the hook; `hook_skipped` is recorded,
    and the action counts as complete for its entry.
- **Who:** retry and rerun, anyone who may move the Item in its current
  state, or the `ops` role; skip, `ops` only, since it gives up an effect on
  purpose. Each move is recorded with the person as actor, the hook as
  `originating_hook`, and a reason code.
- **What it takes:** an `item_incidents` table or open-incident events (a
  schema change); three operations in `kernel.v1` (which only grows) and pli
  commands; a workflow update that schedules the hook again, behind
  `GetVersion` so recorded histories replay; the `ops` role in the dev
  tokens.

## Ventoux V2b and V2c, as built (2026-11-22)

Item 2 of `docs/plans/ideas-from-ventoux.md`, after V2a ("Type problems"):
a draft Type can be checked against the Types already registered, without
registering it, and the Type format has a JSON Schema.

### V2b: ValidateTypes

**One code path.** `typedef.Check(sources, registered)` runs every stage
on draft sources (`{name, content}`) with the registered versions beside
them, given as their **stored JSON definitions**. `ParseDir` reads its
folder and calls it with nothing registered, so a folder at start and a
draft through the API are checked by the same code. Check is pure; the
item service's `ValidateTypes` reads every stored definition and calls it.
Actions are checked against the compiled-in catalog only, as at load: an
undeclared major (`hr.manager_of@2`) is `UNKNOWN_ACTION`, with no network
and nothing from the worker's connectors file.

**The rules.**
- Stages 1 and 2 run draft by draft; stage 3 only when every draft is
  sound on its own. Two drafts of one `id@version`: `DUPLICATE_VERSION`.
- A draft of a registered `id@version` must be **identical**, as
  `RegisterType` decides it: the same definition, compared with the stored
  one as JSON values (key order and spacing do not matter, as for jsonb);
  else `VERSION_CHANGED` on the draft's `version`. An identical draft
  stands in for the registered version.
- Stage 3 runs on the registered versions alone, then with the drafts;
  **only the problems that are new are reported** (by code, Type version
  and path). The store may hold versions a Types folder no longer has, and
  their problems are not the draft's.
- A new problem can land on a **registered** version (a draft
  `leave_balance@2` without the vacation request's consent breaks
  `vacation_request` v2 and v3). It has no position; the API gives it the
  version's name in the caller's namespace as its `source` and `type`.
- A registered definition that cannot be read is the kernel's failure,
  not the draft's: an error (`INTERNAL`), not a problem.

**The operation.** `ValidateTypes {parent, sources[]}` returns
`{problems[]}`, each `{source, path, line, column, severity, code,
message, type}`. A draft with problems is a **successful call**. The API
sorts the problems by source, then position, once every source has a name
(a registered version's included). `code` is a string, like a refusal's
reason, so a new code needs no proto change.
- Limits, each `INVALID_ARGUMENT` / `INVALID_REQUEST` on `sources`: 1 to
  200 sources, 1 MiB of content in all (raised from 50 and 256 KiB for
  build_step, 2026-11-24), a non-empty name of its own each. Outside the
  limits (no source, more than 200, or more than 1 MiB), one refusal names
  both limits and the request's own count and size, in MiB rounded up to
  one decimal ("give 1 to 200 sources, 1 MiB in all; this request has 214
  sources, 1.3 MiB"), so a tool can split the request without guessing. The
  limits are checked before the names; a nameless or repeated name keeps
  its fixed refusal. Checked in the handler (a server-wide read limit would
  bind every operation).
- Who: users, and agents in a session, with no particular power: it
  writes nothing and reads no Items. Customers, and agents acting for one:
  `PERMISSION_DENIED` / `NOT_ALLOWED`.
- Exempt from `policy.Project`, like `ListTypes`: a Type definition holds
  no Item data.
- It is not a write: no `request_id`, no `commit_seq`, nothing stored.

**pli.** `pli type validate [--json] PATH...`: a PATH is a file or a
folder, read as the item service reads its Types folder (one level of
subfolders, names starting with a dot skipped); a deeper folder is
`FOLDER_TOO_DEEP`, found by pli itself, which walks folders on its own so
it does not link typedef. Each source is named by its path as given. It
prints one line per problem in the log-line format above, or the response
with `--json`; it exits 1 on any ERROR, 0 when clean, 2 on a usage error.

### V2c: the Type format as JSON Schema

`schema/type.schema.json` (draft 2020-12), written by hand. **Its
purpose:** an editor or an authoring tool learns the format from it (every
key, its shape, the closed lists of categories and field types, short
descriptions), and can catch a typo before asking the kernel. It is
**not** a second checker: typedef stays the authority, and the schema is
checked in tests only (`santhosh-tekuri/jsonschema/v6`, imported by
`_test.go` files only).

**Never stricter than typedef**: whatever typedef loads, the schema
accepts. The tests (`internal/typedef/schema_internal_test.go`, an
internal test so they read typedef's own key table) prove it this way:
- **Keys.** At every level the schema's properties are exactly the keys
  the stage 1 walker knows, both ways, and `additionalProperties` is false
  wherever typedef refuses unknown keys.
- **Shapes.** At every position of the format, eleven kinds of value
  (null, text, a number, a bool, lists, maps, `kernel`, `[role:clerk]`) are
  placed in a document; each one stage 1 lets through, the schema must
  accept (more than 500 checks).
- **enum** only at `states[].category` and `fields[].type`, each equal to
  typedef's list; typedef loads every listed value and refuses another
  one, and null. **required** only `id` and `version`, which typedef
  requires (missing or null).
- **Keywords.** The schema may use keys, shapes, `enum`, `required`,
  `$ref` and descriptions only; `pattern`, `minimum` and the like are
  refused by a test, so nothing stricter creeps in.
- **Files.** Every file of `types/` validates, and so does a corpus
  (`internal/typedef/testdata/schema`) that, with `types/`, uses every key
  and both shapes of `accepts_reactions` and `writable_by`; each corpus
  file loads.

**As lenient as typedef, for now.** typedef reads any scalar where a
string is meant (`id: 5` loads) and lets null through for most keys
(`fields: ~` loads), so the schema does too. Tightening typedef's stage 1
and the schema together is a Later entry in the journal.

## Ventoux V1, as built: action contracts (2026-11-23)

Item 1 of `docs/plans/ideas-from-ventoux.md`: every action a hook may name
has a contract, checked when a Type loads and when a connector answers,
and readable through the API, so an authoring tool knows the menu it may
choose from. The hook protocol's descriptor and catalog were the first
version; V1 makes the contract the single source of truth.

**Contracts as data.** One JSON file per operation,
`contracts/<connector>.<operation>@<major>.json`, compiled into the item
service and the worker (`contracts/embed.go`, read by
`internal/contracts`), so a Type is checked against exactly the contracts
the worker runs, with no network. The format is the descriptor's operation
(`hookproto.Operation`) plus its `connector` and `major`, with the
contract's additions, all optional on the wire:

- `inputs` and `outputs`: the property names in order (a hook's `reads`
  and `writes` map to them by position; a JSON object keeps no order);
- `description`, required on the operation and on every input and output;
- `risk`: `low`, `medium` or `high`, required;
- on a property, JSON Schema's `format` (`pli-principal`, `date`,
  `date-time`) and `enum`, and on an input `x-pli-trust` (below).

The schemas use a subset: each property's `type` is `string`, `integer` or
`boolean`, alone or with `null`; `format` and `enum` only on a string,
never together. Other rules: only an action has `sideEffects: some`, and
it is its operation's only kind; `async` is `forbidden` (asynchronous
completion is not built); only a check has `errors`, each a reason code;
an enrich operation has an output. Idempotency is not a field: the
protocol keys every call and every connector stores its successes only.
A contract that breaks a rule stops the item service and the worker at
start; `go test` finds it first. Versions stay by major, as built: a
connector's implementations float within a major (HR 1.4 to 1.5); a
change to a contract is a new major.

| Action | Kind | Inputs, in order | Outputs | Side effects | Risk | Reasons |
| --- | --- | --- | --- | --- | --- | --- |
| `crm.get_plan@1` | enrich | `customerId` string (any) | `plan` enum free, pro, enterprise | none | low | |
| `erp.get_credit_status@1` | enrich | `customerId` string (any) | `onHold` bool | none | low | |
| `erp.check_credit@1` | check | `customerId` string, `amount` int (trusted) | `costCenter` string | none | low | `customer_on_hold`, `over_credit_limit` |
| `erp.create_invoice@1` | action | `customerId` string, `amount` int (trusted) | `invoiceId` string | some | high | |
| `hr.manager_of@1` | check | `onBehalfOf`, `createdBy` principal or null (trusted) | `manager` principal | none | low | `unknown_employee`, `no_eligible_approver` |
| `email.notify@1` | action | `to` principal (trusted) | none | some | medium | |

`hr.manager_of@1` has no code in the worker (a remote connector); the
others do. Code without a contract stops the worker at start.

**Frozen once published.** `contracts/contracts.lock` holds one line per
contract, `<action> sha256:<hex>`: the digest of its **machine contract**,
everything a Type or a connector relies on (the names, the inputs and
outputs with their types, format, enum and trust, the kinds,
`sideEffects`, `async`, `errors` and `risk`), as canonical JSON (keys
sorted, no spaces; an input without `x-pli-trust` counts as `any`). **The
descriptions are not in the digest**: they are required, but stay
editable, and their changes show in the pull request's diff. Two guards:

- `internal/contracts` (`Load`, run by `go test` and at every start)
  checks files and lock both ways: a contract without its line, a line
  without its contract, or a digest that differs is an error naming the
  action. Editing a contract fails at once, with no git needed.
- CI, on pull requests beside `buf breaking`, runs `contractsfrozen` on
  the base branch's lock and this one: a line that changed or went is
  refused; a new line publishes a contract. A base branch without a lock
  has published nothing.

The digest is also what `ListActions` serves, so a tool can tell that the
contract it read is the one it pinned.

**Checked when a Type loads** (stage 2, each Type on its own; `typedef`
reads the catalogue, and no longer links the worker's connector code):

- the action is in the catalogue (`UNKNOWN_ACTION`) and the hook gives it
  as many reads and writes as it takes (`ACTION_ARITY`), as before;
- **the hook's kind is one the action serves** (`ACTION_KIND`, at
  `hooks[i].kind`): a check naming an enrich action is refused;
- **each read goes into an input of its type, and each output into a
  field of its type** (`WRONG_FIELD_TYPE`, at `hooks[i].reads[k]` or
  `hooks[i].writes[k]`):

| Contract value | Field it matches |
| --- | --- |
| `string` | string |
| `string` with `enum` | enum |
| `string` with `format: pli-principal` | principal; also `created_by`, `created_on_behalf_of`, `creator` and `container:<offer>` |
| `string` with `format: date` / `date-time` | date / datetime |
| `integer` | int |
| `boolean` | bool |

  A string input takes a string or an enum field; an enum input an enum
  field whose options are all among its values. A string output goes into
  a string field; an enum output into a string field, or an enum field
  that has every one of its values. Otherwise the types are equal.
  Nullability is not checked at load: any field may be unset (proving one
  set from `required` is Later). A multi-value field is refused on any
  hook, as before.
- No cascades: a read or write that is already a problem (an unknown
  field, a multi-value field, a write of `created_by`), an unknown action
  or a wrong arity gets no type problem on top.

No reason code is checked at load: no Type names a reason yet.
`UNKNOWN_REASON` comes with the first place a Type can name one (a
`business_error` routed to a transition). `pli type validate` needed no
change: the new problems come through `typedef.Check`. No Type in
`types/` changed.

**Checked at run time, compiled in or remote alike** (the worker,
`hooks.RunCall`): every output a connector answers must have its type (an
enum one of its values, a principal a principal ID, a date a date), and a
check's deny reason must be one its contract declares. Either breach is
`connector_mismatch`, permanent: a check's hold closes as error with that
reason. Before V1 only a remote connector's reasons were checked, against
its descriptor, and no output's type. A remote connector's descriptor must
fit the contract (`contracts.Matches`): its kinds include the contract's;
its inputs and outputs are the contract's names with the contract's type
lists, and the contract's `format` and `enum` where it gives them (a
descriptor written before V1 has neither, and still fits); its
`sideEffects` is the contract's; its `errors` are among the contract's. A
misfit is `connector_mismatch` with nothing posted. connstub's HR
connector serves the contract itself as its descriptor, so the two cannot
drift. Inputs are not checked at run time: the connector decides (a null
it does not take is its 400, as before).

**Risk is data.** `low` (no effect outside), `medium` (an effect that can
be corrected, or of low stakes: a message), `high` (an effect that cannot
be undone: money, access). No kernel rule reads it: nothing yet needs it
to behave the same everywhere (the membership test). It is served, so a
reviewer and a tool can see it, and item 3's proofs may read it later, as
warnings.

**Trust is declared, not enforced until Ventoux item 4.** An input's
`x-pli-trust` is `trusted` (the value must come from a trusted source) or
`any` (the default). V1 reads it, freezes it in the digest and serves it,
and checks nothing. It is declarable now so that item 4 can enforce it
without a new major for every action. Today's rule: every input of a
check or an action is `trusted`, since it decides a move or causes an
effect (`email.notify`'s recipient included); an enrich input is `any`.
The API field's comment, `pli action list` and this section all say
"declared, not enforced until Ventoux item 4".

**ListActions.** `ListActions {parent}` returns `actions[]`, ordered by
action, each with `name` (`namespaces/{ns}/actions/hr.manager_of@1`),
`action`, `connector`, `operation`, `major`, `description`, `kinds`,
`inputs` and `outputs` in hook order (each `{name, type, options,
nullable, description, trust}`, `type` the `FieldType` it matches, `trust`
on inputs only), `side_effects`, `risk`, `reasons` and `digest`.
- The catalogue is the kernel's, the same in every namespace. It says
  nothing of whether this deployment configured an action's connector
  (that shows as `connector_unconfigured` at run time; Later).
- Who: users, and agents in a session, with no particular power.
  Customers, and agents acting for one: `PERMISSION_DENIED` /
  `NOT_ALLOWED` (it describes the organisation's integrations).
- It reads no Item and holds no Item data: nothing goes through
  `policy.Project`, as for `ListTypes`. It is served by the API from the
  compiled-in catalogue; the item service gained no method.
- `kernel.v1` grew one operation, three messages and three enums
  (`HookKind`, `ActionRisk`, `ActionInputTrust`); `buf breaking` green.
- `pli action list [--json]`: one block per action (its kinds, risk and
  side effects; its description; each input and output with its type, an
  enum with its values, "or null" when nullable; each input's trust; its
  reasons), or the response.

## Ventoux V5, as built: stories in simulated time (2026-11-23)

Item 5 of `docs/plans/ideas-from-ventoux.md`. A **story** is a walkthrough
of one or more Types as data, run as an ordinary Go test in simulated time:
no stack, no dev clock. Stories live in `stories/<domain>/<name>.yaml`;
the runner is `internal/story`, a test harness no binary links (a test
checks it). Stories are written or approved by people, never generated
alongside the Type they test: each says who in `author:` (the first six were
drafted by Claude and approved by the owner on 2026-11-23), and nothing ever
writes expected lines into a story (there is no `-update`). They
complement `scripts/scenario.sh`, which runs the real stack.

**The runner's four exports**, for item 6's witnesses: `Parse` (a story
file into the model, every problem at once with its line), `Check` (the
model against the Types and the action contracts), `Run` (returns what
happened as data: the timeline, every fake call, the failures), `Compare`
(the expected lines against the seen ones). YAML is one way of writing a
story; a witness builds the same `Story` in Go.

### The file and its grammar

```yaml
story: an approved request takes its days from the balance, once
author: drafted by Claude from vacation_request@3's comments; approved by Francois (owner), 2026-11-23
day1: 2026-12-07                  # day 1; every time is UTC
observer: <person>                # whose reads make the timeline; default observer
people:
  alice: {}                       # user:alice
  hana: {roles: [hr_admin]}       # roles in the story's namespace
  kim: {customer: true}           # customer:kim; customers hold no roles
names:                            # the Items the kernel creates
  first: sla_goal in inc where f_metric = first_response
hide_values:                      # values left out of lines; the field stays
  sla_goal: [f_week, f_days_off, f_zone]
show_skipped: false               # reaction_skipped lines (below)
fakes:
  hr.manager_of@1:
    - "when createdBy = alice: correct manager = bob"
  email.notify@1:
    - ok
given:                            # setup at day 1 00:00; its own events are not compared
  - hana creates balance as leave_balance@1 with f_owner = alice, f_allowance = 20
steps:
  - day 1 09:00 alice creates request as vacation_request@3 with f_start = day 15, f_days = 5
until: day 10 00:00               # default: a year after the last step
expect:
  - "day 1 09:00 request: alice created it in draft"
```

Steps, fake rules, name rules and expected lines are one-line strings in
a closed grammar:

```
time   = day N HH:MM[:SS]
value  = INT | true | false | null | "text" | word | day N | day N HH:MM[:SS] | [value, ...]
values = field = value, field = value, ...

person = NAME | AGENT for NAME   (an agent acting for a person; since fields closed to agents)
step   = time person creates ITEM as TYPE@V [in ITEM] [with values]
       | time person updates ITEM with values
       | time person moves ITEM to STATE [with values]
       | time person links ITEM LINK ITEM | time person unlinks ITEM LINK ITEM
       | time person sees ITEM with values             (each field holds that value)
       | time person sees ITEM fields f, f, ...        (exactly the fields set and visible)
       | time person cannot see ITEM
       | time person finds waiting ITEM, ITEM, ...     (the person's "waiting on me" view holds exactly these)
       | time person finds nothing waiting             (that view is empty)
       a write may end in "-> refused REASON" (the API's reason: NOT_ALLOWED, ...)

rule   = [when input = value, ...] [from time] [until time]: answer
answer = allow | deny REASON | correct values | ok [values]
       | fail transient | invalid | unauthorized | key_conflict

name   = TYPE [in ITEM] [where field = value]

line   = time ITEM: sentence
       | between time and time ITEM: sentence
```

A word takes its meaning from the field, input or output it is given to:
an enum option, a person (for a principal), or text. **A sentence** says
who did what; who is a person, `timer`, a hook's ID or a reaction's ID,
then `for PERSON` when it acted for someone else:

| Event | Sentence |
| --- | --- |
| create | `who created it in STATE[ inside ITEM][: values]` |
| transition | `who moved it FROM -> TO[: values][; linked LINK to ITEM]` |
| transition_requested | `who asked FROM -> TO[: values]` |
| transition_denied / _failed | `who denied FROM -> TO: REASON` / `who failed FROM -> TO: REASON` |
| update | `who updated it[ (TIMER)]: values` |
| link_added / _removed | `who linked LINK to ITEM` / `who unlinked LINK from ITEM` |
| hook_ran / hook_failed | `who ran[ (TIMER[ run N])]` / `who failed[ (TIMER[ run N])]: REASON` |
| reaction_applied / _failed | `who applied to ITEM` / `who failed on ITEM: REASON` |
| reaction_skipped | `who skipped: REASON` (with `show_skipped: true` only) |
| a side effect a fake made | `CONNECTOR: OPERATION for ITEM (HOOK): inputs[ -> outputs]` |

Values are `field = value`, sorted by field ID, a reaction's addition as
`field +N = value`, a hidden value as `field = ...`; a step's own event
leaves out the values the step sent. A side effect is one line per
idempotency key, so a retry adds none. The package documentation
(`internal/story/doc.go`) holds the full grammar; item 6's readings can
reuse its wording.

**Comparing.** Lines are grouped by instant, to the second. The order of
instants is strict; within one, lines compare as a multiset, because
Items' workflows work concurrently. **The window form** (`between TIME and
TIME ITEM: sentence`) matches one line seen within it, both ends included,
once the exact lines are matched. It is only for timing the kernel does
not promise: when a check gives up within its budget depends on its
retries' rhythm (the HR outage story uses it, and nothing else). A failing
story prints its failures with their lines, then, instant by instant, the
lines expected and missing (`-`) and those seen and not expected (`+`).

**Skipped reactions are hidden.** Every move of an Item records a
`reaction_skipped` for each of its reactions whose condition did not hold
(two or three on every goal move of an incident), which would swamp a
story. Nothing is lost by hiding them: a reaction that should have applied
and was skipped leaves its effect's line missing, and the story fails on
that. `show_skipped: true` shows them.

**Fakes** answer by their rules, in order; a call no rule matches fails
the story. `Check` holds every answer to its action's contract (its kinds,
its reasons, every output and its type, the inputs a rule matches on), and
the worker holds it again when the fake answers, as it holds any
connector (`hooks.result`). Every action the story's Types' hooks may
call, those of the Types their reactions create included, has a fake.
`fail transient` is retried within the hook's real budget, in simulated
time; `invalid`, `unauthorized` and `key_conflict` map to the worker's
permanent errors.

**Reads and refusals** go through the item service's public reads as the
person (`Get`, so `policy.Project`); a write's refusal is named as the API
names it (`api.Refusal`). A write refused that the story did not expect,
or expected and not refused, a read that sees something else, a call no
rule answers: each fails the story at its line and stops the run. The
timeline is read through `ListEvents` as the observer; an observer who
cannot see an Item of the story fails it.

### How a story runs, and where that differs from production

- **The same code:** the real `ItemWorkflow` and its activities, registered
  by `workflow.Register` exactly as the worker registers them; the item
  service on Postgres (testcontainers), with every Type of `types/`; the
  real relay's `RunOnce`. Each run has a namespace of its own.
- **Simulated time:** Temporal's in-process test environment, whose clock
  skips ahead whenever nothing is working. The item service stamps writes
  with that clock (`itemservice.Options.Clock`, tests only), so a timer
  counted from an event's time fires at the right simulated instant.
- **Fakes in process:** each faked connector is a `hooks.Connector` reached
  through `hooks.Connectors.WithConnector`, the Go binding, ahead of remote
  and compiled-in code.
- **Differs from production:** a **driver workflow** (harness code) starts
  each Item's workflow as its child: it stands in for the server's
  Update-With-Start. The **relay runs inside a settle step** the driver
  runs after every step, and again whenever one of the kernel's activities
  ends: it delivers until the outbox is empty and none of the kernel's
  activities is scheduled or running. While it runs, simulated time cannot
  move, so everything a step causes happens at the step's instant (an
  activity waiting to retry does not hold time: its retry comes later in
  story time). Each delivery attempt has an update ID of its own (the test
  environment would never answer a refused ID again; the workflow's
  `lastAppliedSeq` still drops a duplicate). An email fake does not apply
  the compiled-in email code's own rule (only users and agents get mail):
  contracts cannot say it yet (Later).
- **scripts/scenario.sh still covers the real path:** the Temporal server,
  Update-With-Start, the relay as its own process, the worker's connectors
  over HTTP, the dev clock. Stories do not replace it.

**Counting the kernel's activities** is what makes a settle exact: a
workflow interceptor counts each activity when it is scheduled, a wrapper
counts each attempt while it runs, and the cancel listener clears one
canceled before its first attempt.

**The two risks Phase A named**, each with a test: a continue-as-new
(100 failed undo attempts) carries the refund into the new run, which then
happens once (the test environment reruns a child that continued as new
without reporting its completion; the harness counts a start while the
workflow runs as one); and a delivery the workflow's validator refuses (a
gap in `item_seq`) reaches the relay as an error, and the harness reports
it as the story's failure instead of waiting.

**Two harness bugs, found and fixed in Phase C:**
- *Settling too early.* An update's answer can reach the settle step while
  the test environment is still in the workflow task that handled it,
  before that task has scheduled the activity it starts: a step at the same
  instant then ran before the reaction (seen once in a full `-race` run: a
  read saw the balance before its deduction). When the outbox is empty and
  nothing runs, settle now lets the environment finish its task (a round
  trip through its main loop), then looks again.
- *A stalled delivery.* An update sent to an Item's workflow that completes
  before handling it is never answered in the test environment (the server
  would refuse it): a delivery waited out its 30 s timeout (seen once). A
  delivery now notices that its workflow completed meanwhile and treats it
  as not listening, as the server's answer would.

**The first stories:**

| Story | What it shows |
| --- | --- |
| `vacation/approve` | HR names the approver; no self-approval; the approval takes the days once and emails the requester; no reminder, no expiry |
| `vacation/expiry` | Two reminders (48 h, then 24 h later), expiry at 120 h, the email; nothing taken |
| `vacation/refund` | Withdrawing an approved request refunds once, records it, emails it |
| `vacation/hr_outage_and_denial` | The check fails closed within its budget while HR is down; asking again succeeds; HR not knowing someone denies |
| `incident/reopen_on_reply` | A P3 in a portal on office hours: first response met; waiting pauses resolution over a weekend; the customer's reply resumes it; reopening starts a new goal; the timer closes it |
| `incident/breach` | Each goal breached at its due instant, the assignee at that moment emailed once; both goals missed; the customer sees her fields only |

Each runs in under a second under `-race` (30 rounds of the whole package:
no failure; the slowest run, `incident/reopen_on_reply`, 0.84 s).

**pli:** tests only (`make stories`, `STORY=vacation/approve` for one): a
story runs the worker in process, and pli links nothing of the kernel. A
`pli story run` against a server would need a simulation API (Later).

**Kernel changes (four seams):** `itemservice.Options.Clock` (tests only;
a guard test reads the module's source), `hooks.Connectors.WithConnector`,
`workflow.Register` (pulled out of `NewWorker`, no behaviour change), and
`api.Refusal`. No schema, proto or write-path change.

## Ventoux V6, as built: plain-language readings with witnesses (2026-11-24)

Item 6 of `docs/plans/ideas-from-ventoux.md`. A **reading** renders a Type
as numbered sentences a person approves, from the Type and the action
contracts it pins, nothing else: what a non-developer reads instead of the
YAML. Each sentence has a kind, and every kind has a **witness**, a test
that runs the engine and shows it does what the sentence says, so the
reading cannot lie about what the kernel does.

**The package.** `internal/reading` reads the semantic model
(`typedef.Type`), never the YAML: `Read(type, catalog)` returns the
sentences under their sections, a key, the field list, the actions and what
no reading covers; `Text()` lays them out for a person; `Condition` reads
one expression. Each sentence carries its kind and the clauses it uses (its
**parts**: `move`, `move.guard`, `who.creator`, ...) and the paths of the
Type it reads (`transitions[1]`, `states[2].timer`). Which moves wait on a
check or a held reaction comes from `lifecycle.CheckFor` and
`lifecycle.HeldReaction`: the reading asks the lifecycle, it does not
decide.

**The order of a reading.** The heading (`incident@3`); a key defining
only the terms its sentences use (the requester, staff, a customer, an
agent, the kernel, the timer, a hold, a reaction); the fields; then the
sections, each left out when empty: *In short* (where it starts, that no
other move is possible, its invariants); *Who starts it* (its start block); *Who writes what* (fields only the
kernel or a role writes, and who the kernel's writers are; unique fields);
*Links* (its link types, the names it gives incoming links, its rollups);
*one section per state*, in the Type's order, headed with its category
("submitted (waiting)"): what entering it does (a link the kernel
resolves, hooks, reactions and the hooks on them), what happens while it
is there (frozen fields, timers, acting timers), each move out followed by
what it waits for (a check or a held reaction, and the hold's rules), or
that nothing leaves; *When something is created inside it*; *What other
Types may do to it* (consents, fields readable by reactions, offers); *Who
sees it* (customer fields, own, shared, placement). Then the actions it
calls, as their contracts describe them, and what no reading covers.

**The kinds of sentence** (91 parts: 38 kinds and their clauses, the start
block's 8 included; 95 parts and 40 kinds since fields closed to agents;
the list is closed, `reading.Parts()`):

| Kind | Wording (clauses in brackets) |
| --- | --- |
| starts, closed, no_lifecycle | A {type} starts in {s}. / It moves only as listed below; any other move is refused. / A {type} has no lifecycle: it stays in `default`. |
| invariant | Every write keeps this true: {rule}; a write that would not is refused ({id}). |
| field_kernel, field_roles, field_unique | Only the kernel writes {f}: {writers}. / Only holders of {roles} write {f}. / No two {type}s in one container, both in {states}, hold the same {f}. |
| field_agents_only, field_agents (fields closed to agents by default) | Only agents[ and holders of {roles}] write {f}. / {No agent writes its fields / An agent writes only {fields}}[, except one that names a role the agent holds / , and a field that names a role the agent holds][; an agent acting for a customer sets only what the customer may]. |
| link, incoming, rollup | {Anyone who can see it / Only the kernel / Only holders of R} links it to a {T} as {name}, {at most one / any number}[; a {T} takes at most one]; a {T} sees it as {inverse}. / It reads the {T}s linked to it as {link} under the name {name}. / When it is read, {id} counts its {over} by {count_by}. |
| frozen, final | While it is in {s}, nobody changes {fields}. / Nothing leaves {s}. |
| timer, at_timer | {d} after it enters {s}, if it is still there, the timer moves it to {to}[; if a hold is open then, the timer waits for it to close and looks again]. / When it is in {s} and the instant in {f} has passed, the timer moves it to {to}. |
| acting, acting_at | {d} after it enters {s}[, then every {e}], {hook} calls {action} with {inputs}, at most {n} times while it stays there. / When it is in {s} and the instant in {f} has passed, [if {when}, ][{id} sets {f} to that instant and ]{hook} calls {action} with {inputs}. |
| enrich, action, resolve | On entering {s}, {hook} calls {action} with {inputs} and writes its answer into {fields}[; with no answer within 15 minutes it writes {fallback}]. / ... [and writes what it made into {fields}], once per entry, even if it has moved on. / On entering {s}, the kernel links {link} to the requester's {state} {T} (the one whose {f} is the requester); if there is none, or more than one, the move into {s} is refused. |
| move | {Who} moves it from {a} to {b}[, once {fields} are set][ and / , once][ the mover gives {fields} in the move itself][, only if {guard}][, even while a hold is open on it, which cancels the hold][; the move clears {fields}][; the move sets {f} to {value}]. Who: Any staff member who can see it / The requester / Anyone with the role R / Whoever {f} names / `principal` / Only the timer / Only the hook {h} may move it / Only a reaction; [but never anyone who took part in creating it / its container]. |
| check, held, hold, undo | Asking for this move opens a hold instead: {hook} calls {action} with {inputs}; if it allows[, or corrects {fields},] the move happens; it may deny it ({reasons}); with no answer within 15 minutes the move fails. / Asking for this move opens a hold instead: {id} adds {v} to {f} on the {T} linked as {link}; the move happens only if that Item accepts the write, else it is denied with that Item's reason. / While that hold is open, [nobody changes {fields}, and] {the only move out of {a} is to {escape} / nothing moves it}. / On entering {s} from {from}, {id} gives back what {held} added. |
| move_children, move_container, move_link, create, set, reaction_hook | On entering {s}[ from {from}][, if {when}], {id} moves the {T}s inside it[ whose {when}] from {states} to {to} / moves its container from {states} to {to}[; if its container's rules refuse the move, it is skipped] / moves the {T} it links to as {link} from {states} to {to} / creates a {T}@{v} inside it[, one for each value of {f}], with {values} / sets {f} on its container to {value}. / When {reaction} has applied, {hook} calls {action} with {inputs}. |
| created_inside | When {the requester / a person other than the requester / anyone} creates a {T} inside it[ while it is in {s}], {id} moves it to {to} / moves the {T}s inside it[ whose {when}] from {states} to {to}. |
| consent, readable, offer | {T}'s reaction {id} [/ Any Type's reaction {id}, from its container,] may {change / set / create it, setting / move it to} .... / Another Type's create reaction may read {fields} from it. / Items inside it may read {f} as {offer}. |
| start_in, start_who, start_latest (starting work, step 2) | A {type} is created only inside a {T}[, or at the top level] [/ only at the top level]. / {Any staff member who can see the container / Anyone with the role R / `principal` / Whoever {f} of the container names / Anyone who took part in creating the container} creates one; a customer creates one only where a container lets them. [/ No person creates a {type}: only a reaction does.] / While this is the latest version of {type}, these rules apply to creating a {type} of any version. |
| customer_fields, own, shared, placement | A customer who sees it sees only {fields}, and sets only those when creating it. [/ ... sees none of its fields, only its header.] / A customer may create a {T} inside it, and sees the ones they requested. / The customer who requested it sees every {T} inside it, and may create one there. / {Anyone who can see it / Only holders of R / No one} moves it to another container or narrows who sees it. |

Field IDs are written in code font; labels replace them when presentation
hints exist (roadmap, theme 1).

**Expressions in words.** A closed subset reads in words: fields and the
kernel's variables (today, the time of the write; "that instant" in an
acting timer at an instant), literals, plus and minus, the comparisons
("at least", "on or after" for dates and instants), "is set" and "is
unset", "is x or y", and a chain of "and" or of "or" (never both: a reader
cannot see the brackets). Anything else reads as what it reads, "a rule
over `f_a` and today holds", "computed from `f_priority`, the time of the
write and its container's `sla_policy`": never a guess at its meaning.

**Left out, each for a reason** (`reading.LeftOut`, held to the JSON
Schema's keys): a state's category is a label in its heading (it decides
which views show the Item, not what the engine does to it); a field's type,
options and count are the field list (the schema checks them on every
write); who can see a given Item is set per Item and container, not by the
Type; the kernel's own promises (retries, each write once, recovery) are
the same for every Type; and, since starting work step 2, who may create
an Item of the Type and where, and who may move or narrow one, once a
later version exists (the latest version's start block and placement rule
decide for every version, so no reading of one version can say it). These three are said in every reading's closing note.

**Witnesses and the guards.** Twelve witnesses (eleven, and step 2's `start`)
(`internal/reading/witness_*_test.go`), each naming the exact sentences it
relies on in the reading of a fixture Type (26 small Types in
`internal/reading/testdata/types/`, written for the witnesses, so
`types/` can change without them) and running a story through
internal/story's `Run` whose whole timeline is expected, both sides where
there are two (the named person can, someone else is refused with the
API's reason). Written, never generated: a generator reading the Type would
share the renderer's misreading. The guards: every part has a witness
(both ways); every `Part` constant is on the list (a source walk); every
key of `schema/type.schema.json` is read by a part or left out with a
reason; every element of every Type in `types/` (and every fixture) is
cited by a sentence; the words of conditions are held to
`lifecycle.TransitionWith`, the function the item service's moves call at
run time, on 28 boundary cases, and a false guard is refused
`GUARD_FAILED` through `Run`.

**Readings approved by the owner**, `readings/vacation_request@3.txt`,
`readings/incident@3.txt` and, since step 2, `readings/incident@4.txt`,
compared exactly; a change to a Type or the
renderer that changes them fails, and the new text is pasted in by hand
and approved again in the pull request (no `-update`, as for stories).

**ExplainType** (`kernel.v1` grew one operation and six messages, reusing
`HookKind` and `ActionRisk`): a registered version by `name`, or a draft
(`parent`, `sources`, `type_id`, `version`) checked by the item service's
`ValidateTypes` with its limits; a draft with problems is a successful call
listing them, with no reading. The reading comes whole: sentences with
number, kind, parts, text and paths; key; fields; actions; what is not
covered; the text. Users, and agents in a session; customers and agents
acting for one, `PERMISSION_DENIED` / `NOT_ALLOWED` (a reading names the
organisation's integrations, as `ListActions` does). No Item is read, so
nothing goes through `policy.Project`; the item service gained no method.

**pli.** `pli type explain [--json] TYPE@VERSION` prints the text exactly;
`--draft TYPE@VERSION PATH...` reads the PATHs as `type validate` does,
and prints a draft's problems as it does (exit 1).

**Stories grew** (harness, not kernel): `person places ITEM in ITEM | at
the top level`, `person narrows ITEM to person, ...` and `sees ITEM with
ROLLUP.KEY = N`. And a fix the witnesses found: a step naming an Item the
kernel created (a name rule) went to no Item, so `cannot see` of one passed
whatever the person could see; `kim cannot see first` in
`stories/incident/reopen_on_reply.yaml` was vacuous, and is now a real
check that passes.

**Found by writing the witnesses:** an acting timer at an instant sets
`cause.at` to the field's instant, not to when it runs, and runs at once
when the Item enters after the instant; guards are judged before `allowed`,
so a move `allowed: [nobody]` with a false guard is refused
`GUARD_FAILED`. **Found by the readings** (Later): incident@3 lets any
staff member who sees it start, wait, resolve or cancel an incident (only
placement is kept for desk_agent); vacation_request lets anyone who sees it
narrow it.

**Kernel changes (three):** `internal/reading`; `ExplainType` in
`kernel.v1`; `pli type explain`. No schema, write path or item service
change; `ValidateTypes`'s draft checks are now shared helpers in the API,
with no change in behaviour.

## Starting work, step 2, as built: the start block (2026-11-24)

Part 1, sections 1.1 to 1.8, of `docs/plans/starting-work.md`, without the
intake rows (intakes do not exist yet). A Type may say where its Items are
created and by whom; nothing changes for a Type without the block.

```yaml
start:
  in: [desk, top]        # container Type IDs (any version of each); top = no container. Absent: anywhere
  allowed: [role:desk_agent, principal:user:ana, field:f_lead, container_creator]   # or [nobody]. Absent or empty: anyone who sees the container
```

**The latest version governs.** The latest registered version's block
judges a create of **every** version of the Type; the Item stays pinned to
the version the caller names. Which version is latest is read in the
create's own transaction every time (`select max(version) from types
where id = $1`, one indexed lookup, never cached, so a version another
process registers counts at once); the definition then comes from the Type
cache. A later version without a block loosens every version at once. Types
are global for now (journal, Later: Types per namespace), so a block
governs every namespace. **What 1.3 promised `pli type validate` would say**
(which earlier versions a new block governs; that dropping one loosens
them) **is deferred, not built**: it needs a severity other than ERROR in
`kernel.v1` (journal, Later, with Ventoux item 3).

**Each entry reads the container**, never the new Item (`policy.MayStart`,
which shares its entry matching with `MayTransition`: one function,
`matchesEntry`, matches role:, principal: and field: for both):

| Entry | Lets in |
| --- | --- |
| `role:<name>` | a holder of the role; an agent and the user it acts for must both hold it |
| `principal:user:<id>` | that user; an agent acting for them does not pass (both must) |
| `field:<id>` | the principal the **container's** principal field holds now (any member of a set). A field matches only the principal it holds: one holding `user:bob` never matches `agent:x`, so an agent acting for bob does not pass, and an agent named in the field passes only acting for itself |
| `container_creator` | anyone who took part in creating the container (`TookPartInCreation`), judged once for the principal as a whole |
| `nobody` | no person, no agent, no customer: only a create reaction, by the target's `creates:` consent |

**Customers** (and agents acting for one) are not judged by `allowed`:
their gate is the container's `own` and `shared`, as before; `in` applies
to them; `nobody` refuses them too (fail closed). A person opening an Item
for a customer (`requester`) is judged as the person.

**The order of a create's checks** (inside the transaction that locks the
container; this corrects plan 1.6, which put the start block before the
session's scopes, against "NOT_FOUND before any other check" above):

1. What answers `NOT_FOUND`: the container visible to the caller (and to
   `on_behalf_of`), a customer's container checked again under the lock,
   an agent session's scopes.
2. `in`, against the container's Type, read under `FOR SHARE` (taken after
   Create's own container locks, so never an upgrade): `START_NOT_IN`.
3. `allowed`, by `policy.MayStart`, with the container's fields and creator
   and the roles of the actor and its user (read only when an entry names a
   role): `START_NOT_ALLOWED`.
4. The rest, in their old order: placement for a narrow, the write and
   create powers, the agent write rule, uniqueness, invariants. (The
   placement check moved after the session's scopes, so `NOT_FOUND` now
   comes before `PLACEMENT_NOT_WRITABLE` too.)

A customer's `own`/`shared` check still runs before the transaction, so for
a customer it refuses before `in`; over the API both read the same.

**Refusals.** `*StartRefusedError{Reason, TypeID}`, an `ErrForbidden`;
its message names the reason (`START_NOT_IN`, `START_NOT_ALLOWED`) and the
Type ID, never a principal or an Item. The API answers `PERMISSION_DENIED`
/ `NOT_ALLOWED` with that reason's fixed message, byte for byte a
read-only agent session's: no contract change. Saying why over the API is
Later.

**Load checks** (`typedef.Check`, so `ParseDir`, `ValidateTypes` and
`pli type validate` alike): stage 2 (`checkStart`): a Type named `top`
(`RESERVED_NAME`); `start: {}` (`REQUIRED`); `in: []`, an entry of another
kind, `creator`, or a `principal:` other than `user:` (`INVALID_VALUE`); a
malformed `in` entry (`INVALID_NAME`); an entry twice (`DUPLICATE`, on the
second); `field:` or `container_creator` without `in` or with `top` in it,
and `nobody` beside another entry (`CONFLICT`, on that entry). Stage 3
(`CheckStart`): `in` names a loaded Type (`UNKNOWN_TYPE`); `field:` names a
principal field of every version of every Type in `in` (`UNKNOWN_FIELD`,
`WRONG_FIELD_TYPE`); `own`, `shared` and create reactions are judged
against the target's latest block: `START_NOT_IN` (new code) when its `in`
excludes the container's Type (for a reaction, the source's: the Item is
created inside it), `CONFLICT` when its `allowed` is `[nobody]`. A draft
that drops a registered source from `in` is refused with the problem on the
registered source. **1.5's refusal at registration holds for
`ValidateTypes`, `pli type validate` and the Types folder read at start,
not for `RegisterType`**, which runs no checks of the Types together (as
for every other stage 3 check); at run time `Create` refuses anyway.

**Readings.** A Type with a block reads a section *Who starts it* after *In
short* (see "Ventoux V6, as built"), and every reading's closing note says
that the latest version decides who may create one, and where.

**incident@4** is incident@3 with `start: {in: [desk]}` and no `allowed`
(the owner's decision, option B): an incident of any version is created
only inside a desk, by anyone on staff who sees it, or by a customer where
a desk's Type owns incidents. `role:desk_agent` would have shut out the
employee with the problem, who opens the incident in scenario B (ana, not
a desk agent); who may open incidents is a later product choice (journal,
Later: a `role:employee`, say).

**Kernel changes (four):** the `start` block in `typedef` (parse, load
checks, `START_NOT_IN`, `Start.Admits`); `policy.MayStart` (with
`matchesEntry` shared with transitions); the check in `Create` with the
per-create latest-version lookup; the readings' *Who starts it* and closing
line. No schema, proto, migration or go.mod change; the JSON Schema gained
`start`.

## Starting work, step 2b, as built: placement follows the latest version (2026-11-24)

**The rule (the owner's decision, a kernel rule with no opt-in on the
Type):** the lifecycle stays pinned to the Item's version; **where an Item
starts and who places it follow the latest registered version** of its
Type. Step 2 did the first half (the `start` block). Step 2b does the
second: `placement_writable_by` of the latest version judges a move, a
narrow and an un-narrow of an Item of **any** version, and a narrowed
create **whatever version it names**.

**Why, and why no opt-in.** Placement is access control: who may hide an
Item from the desk, or move it out of its container. Before this step a
client could dodge a rule a later version added by creating the older
version, and an Item pinned to an old version kept the old rule for ever.
A Type that had to ask for the new behaviour would fail open: the Types
that forget are the ones left exposed.

**How.** `checkPlacement` reads the Type through `latestType`, as the
start check does: `select max(version) from types where id = $1` in the
write's own transaction, never cached, so a version another process
registers counts at once. Two callers: `Create` (only when the create
narrows) and `Update` (a move or a change of readers, not the kernel's own
writes, which never place). The order of checks, the refusal
(`PlacementNotWritableError`, over the API `PERMISSION_DENIED` /
`PLACEMENT_NOT_WRITABLE`) and the contract are unchanged. A narrowed create
reads the latest version twice (start, then placement): two indexed
lookups, kept apart for simplicity.

**Both ways.** A later version that drops the rule loosens every version
at once; one that says `kernel` stops everyone, role holders included.
What `pli type validate` should say about either (the "governs earlier
versions" and "loosened" notes) stays deferred with step 2's (journal,
Later; Ventoux item 3).

**What follows the latest version, and what does not:**

| | Judged by |
|---|---|
| where an Item is created, and by whom (`start`) | the latest version |
| who moves, narrows or un-narrows it (`placement_writable_by`) | the latest version |
| its states and moves, guards, reactions, timers, invariants | the Item's own version |
| who may make a move (a transition's `allowed`) | the Item's own version |
| who may write a field (`writable_by`), what a customer sees | the Item's own version (but that a field without `writable_by` is closed to agents is a kernel rule, for every version: "Fields closed to agents by default, as built") |

**What this does not close.** A create may still name an older version, and
that version's lifecycle then applies: a requester creating on an older
version skips a rule a newer version put in the lifecycle (the roadmap's
open item, found by the first bake-off, where a CFO approval was skipped
this way). Step 2b closes that door for placement only. Retiring a version
for creates needs its own step, decided before it is built.

**What changed for the Types in `types/`** (the only ones whose versions
differ in the rule):

| Type | Older versions said | Now, for every version |
|---|---|---|
| incident | @1, @2: anyone who sees it | `role:desk_agent` (as @3, @4) |
| desk | @1, @2: anyone who sees it | `role:sla_admin` (as @3) |
| reply, note | @1: anyone who sees it | no one (as @2) |
| comment | @1, @2: anyone who sees it | no one (as @3) |
| sla_goal | @1, @2: anyone who sees it | no one (as @3) |

**`GetType` on an older version still shows that version's
`placement_writable_by`**, which may not be the rule in force: read the
latest version to know who places an Item. No contract change was made for
it (journal, Later). **Readings** say so in their closing note: "Who may
create an incident, and where, and who may move or narrow one, once a later
version exists: the latest version of incident decides, for every version."

**Kernel changes (one, and a line in the readings):** `checkPlacement`
reads the latest version. No schema, proto, migration, JSON Schema or
go.mod change.

## build_step as built (2026-11-22)

`types/build_step.v1.yaml`: how one step of this repository is built, as
a Type, so the process in `CLAUDE.md` ("How we work") is a rule the kernel
enforces. It replaces the story for this repository's work (it keeps an
`epic` link) and runs on the long-lived tracker stack, in `acme`, in the
ORI space. Proposed in `docs/plans/build-step-type.md`; the loop that uses
it is `docs/plans/autonomous-loop.md`. **No kernel, schema or API change:**
every rule below already existed.

- **Lifecycle.** questions -> answering -> writing_tests -> tests_proposed
  -> building -> in_review -> done; building -> writing_tests when a test
  must change; the owner drops a step from any state before done, and
  restarts a dropped one at questions.
- **The owner's moves.** Every approval (answering -> writing_tests,
  tests_proposed -> building or back, in_review -> building or done, every
  drop and restart) is `allowed: ['field:f_owner']`. A `field:` entry never
  matches an agent, so an agent acting for the owner, whatever powers it
  holds, gets `NOT_ALLOWED`, as does any other user.
- **Only a maintainer creates a step.** `f_owner` is
  `writable_by: ['role:maintainer']` and the invariant `owner_missing`
  refuses a step without one. An agent holds no role: naming an owner is
  `FIELD_NOT_WRITABLE`, leaving it out is `INVARIANT_FAILED`, so an agent
  cannot create a step at all, and cannot change its owner.
- **Required:** f_questions to answering, f_tests_ref to tests_proposed,
  f_pr_url to in_review, f_journal to done.
- **Frozen:** f_questions once the step has left answering (writing_tests
  through done; not in dropped, so a restarted step can rewrite them);
  f_tests_ref from tests_proposed on; f_owner from building on; f_pr_url
  in in_review and done; f_journal in done. The move entering a state may
  set what it freezes; the move leaving it may not.
- **Six X -> dropped lines**, one per state: the loader has no `from:`
  list (Later, with Ventoux item 8 and V2c).
- **What it does not enforce:** the kernel governs the record, not the
  repository: it freezes the tests' reference, not the files. Keeping the
  two in step is CI's job (later). It holds only while no agent is given
  `maintainer`.

## An effect re-checks what caused it, as built (2026-11-24)

The rule is under "Architecture style". The first bake-off found the hole
(`docs/research/camunda-bakeoff.md`, test 2c): a workflow started by hand
on a Temporal with no sign-on told the kernel's own workflow that a
purchase still waiting on its manager was approved, and the worker placed
the order. Everything an activity is given comes through Temporal (the
start input, an update, an earlier activity's result), so all of it can be
forged, by a forged start or by forged workflow code on the task queue.

**The check.** `RunAction` first calls `Internal().ConfirmCause`
(`internal/itemservice/cause.go`), before it reads the hook's inputs or
calls the connector. From committed state only, it confirms:

- the Type and version named are the Item's (an Item never changes version);
- the hook is an action hook of that Type (by the contract rules, "action"
  and `sideEffects: some` are the same set);
- the event named is an event of this Item, and the one the hook is bound
  to. Which event that is, is the Type's to say: one pure function,
  `lifecycle.BoundTo`.

| Hook | The event it is bound to | What else is confirmed |
| --- | --- | --- |
| `on: enter` | the create or transition that entered its state | |
| `on: reaction` | the `reaction_applied` event of its reaction | |
| `on: timer`, on a schedule | the entry into its timer's state | the timer is that state's and runs this hook; the run is 1 to `max` |
| `on: timer`, at an instant, with `sets` | the entry into its timer's state | the same, and the timer's own write for that instant exists (`ActAt`'s idempotency record, written with its event) |
| `on: timer`, at an instant, no `sets` | the entry into its timer's state | the timer is that state's and runs this hook; nothing else (below) |

- **The idempotency key is the one the kernel derives** from the confirmed
  cause. Before this, the key came from the workflow's input, so one true
  cause could be used under many keys, each a new order. The workflow and
  `ConfirmCause` build keys with the same functions (`lifecycle.HookKey`,
  `TimerRunKey`, `TimerAtKey`, `TimerAtHookKey`).

**The Item's state now is not asked.** The check is "this entry really
happened", not "the Item is still there": an action runs to completion for
its entry even after the Item has moved on, and a retry must confirm again.
Events never change, so the same cause confirms every time.

**A refusal** is `ErrCauseNotConfirmed`: final, a non-retryable
`CauseNotConfirmed` in the worker, and nothing is read for the connector
or sent. It is recorded on the Item as `hook_failed` with the reason
`cause_not_confirmed`:

- written by `ConfirmCause` itself, through the normal write path (the
  Item locked, the event and its outbox row, an idempotency key, a
  `commit_seq`), as the hook;
- the payload is the hook and the reason, nothing else: no caller-supplied
  text. It has no `causation_id` (the claimed cause is not its cause; and
  an action counts as done for an entry once an event of its hook names
  that entry, so naming it would let a forged call suppress a real action)
  and no correlation ID (that too comes from the workflow's input);
- once per Item, hook and event named, however often it is asked;
- only when the event is real and the Item's Type declares the hook.
  Otherwise (a made-up event, an undeclared hook, another Type's hook) it
  is refused and logged, and nothing is recorded: there is nothing true to
  say it about. A wrong version of the Item's own Type is recorded, as the
  Item's own hook.

Only `ConfirmCause` writes that reason: `HookFailed` does not accept it.
The workflow does not record `action_failed` beside it.

**An action's own records confirm their cause too.** `hook_ran`,
`hook_failed` and the write-back are refused, under the Item's lock, for
an event the action hook is not bound to (`confirmBound`); nothing is
written. Checks and enrich hooks are unchanged: a check's write needs its
open hold, its decider and its request; enrich is open (below).

**Cost:** two reads by primary key (the Item, the event), a third for a
timer at an instant with `sets`; the Type comes from the cache. About
0.25 ms per side-effecting action on a local Postgres (p50 0.25, p95 0.36,
p99 0.46 ms over 3,000 calls; the existing `HookInput` read measured 0.27
ms in the same run). It runs in the worker's activity, in process.

**No contract change:** no proto, schema, migration, dependency or
hook-protocol change. One method on the internal door, one reason code (a
value of the API's existing `reason` string), one worker error type.

**Not confirmed** (each is in `docs/roadmap.md`, "Holes found by the
bake-off", and pinned by a test named `TestResidue_...`, so closing one
shows as a change to a test):

- **That a scheduled acting timer's run was due.** A run leaves no event
  until its hook has finished. A forged start can send a reminder early,
  for an Item that really entered that state: at most once per run (the
  key is the kernel's).
- **That a timer at an instant with a hook and no `sets` acted.** It writes
  nothing when it acts. No Type in `types/` has one.
- **A forged `hook_failed` that names the hook's true entry.** It checks
  out, and the action then counts as done. It withholds an effect; it
  never makes one.
- Outside this rule's wording: **state timers trust the workflow's clock**
  (a forged start can make a duration timer's move early; the move is then
  real, and its hooks confirm), and **an enrich hook's cause is not
  confirmed** (a forged run re-reads through a read-only action and
  rewrites the hook's own fields).

Authenticating Temporal closes these at the root; this step makes the
worker safe for side effects even where Temporal is not.

**Tests:** `internal/workflow/cause_e2e_test.go` (15; the first is the
bake-off's attack), `internal/lifecycle/cause_test.go`. The bake-off's own
script, re-run against its own stack: "the purchase system received
nothing".

## Fields closed to agents by default, as built (2026-11-24)

Track 1, item 2 of `docs/roadmap.md`. Both bake-offs found the same two
holes in every kernel Type (`docs/research/temporal-bakeoff.md`, measure 4;
`docs/research/camunda-bakeoff.md`, test 2b): a compromised agent with the
right to edit fields changed a requester's draft before she submitted it
(what the manager then approved was the agent's version), and planted the
reason for a decision, so the decider could reject without giving one
though the Type requires it. A role every person holds and the agent does
not (`writable_by: ['role:staff']`) closed both, but only for an author who
knew the trick. Now the kernel closes them by default.

**The rule.** A field's `writable_by` lists who writes it on the public
door: `people` (any user or customer who may write the Item), `agents`
(any agent with a write power), and `role:<name>` (its holders, agents
included), in any mix. `kernel` still stands alone. **A field with no
`writable_by` is people's only: an agent writes a field only where its
Type says so**, by `agents` or by a role the agent holds.

- `[people, agents]`: anyone. `[agents]`: an agent acting alone (its user,
  a person, has no right there). `[people, role:x]`: any person, and an
  agent holding x. `[role:x]`: holders of x, as before.
- **An agent and the user it acts for both pass**, as for `allowed` and
  roles: the agent as `agents` or holding a role, its user as `people` or
  holding a role. An agent acting for ana, who may write a closed field
  herself, is refused.
- **An agent acting for a customer is judged as that customer**, under
  the customer rules (`customer_fields`, on create only); they were already
  closed.
- `policy.MayWriteField(field, who)` is the one pure answer;
  `typedef.Field.PeopleWrite` says whether people write a field.

**The owner's decisions (Phase A):**

- **A kernel rule, for every version at once**, like step 2b's placement
  rule. A field without `writable_by` in an Item of any version is closed
  to agents now; `writable_by` itself stays the Item's version's. An
  opt-in per new version would have left the bake-offs' Types open. Items
  in flight lose agent writes at once; a version that needs agents gets a
  new version (and its open Items wait for track 1 item 6).
- **Field by field.** No Type-wide opening: it would silently open every
  field a later version adds (Later, if an agent-first Type finds it
  painful).
- **New versions only**, never an edit to a registered one:
  `build_step@2` opens what its agent writes (`f_title`, `f_questions`,
  `f_tests_ref`, `f_pr_url`, `f_journal`, `f_rank`; `f_owner` stays
  `role:maintainer`'s) and `ticket@7` (ticket@6 with `f_priority` open: the
  triage bot's field). `vacation_request` stays closed: a person fills
  their own leave. The bake-offs' Types are unchanged.

**What is closed, and what stays the kernel's.** Every field an agent
names on the public door: on Create, on Update, in a transition's patch
(a held transition's patch included, judged when asked), and **the fields
a move `clears`** (a clear is a write of that field, as for roles). Not:
a move's `sets` (the Type computes those values); a Create with no fields;
links, readers and placement (their own rules; links closed to agents are
Later). The kernel's own writers are untouched: hooks (a check's
correction, an enrich, an action's write-back), timers and reactions all
use the internal door.

**The order of checks.** On Update and on Transition nothing moved: the
field rule is still the first part of "not writable", before frozen and
held. So an agent's write to a frozen closed field is now
`FIELD_NOT_WRITABLE`, not `FIELD_FROZEN`, and on a transition the agent
write rule (`NEEDS_APPROVAL`) still comes before the field rules, as before.
**On Create**, who may write each field is now asked inside the
transaction, after what is not found (the container, the session's
scopes), the start block and the create power, and before the agent write
rule; a kernel-written field is still refused at once.

**The refusal.** The existing `PERMISSION_DENIED` / `FIELD_NOT_WRITABLE`
with `fields` (no new code, no contract change). The item service's
message, for an agent: "an agent writes only the fields its Type opens to
agents, or to a role it holds: the request may not write f_amount,
f_vendor".

**Load checks and schema.** A field's list takes `people`, `agents` and
`role:<name>`; anything else is `INVALID_VALUE` naming the three; an
entry twice is `DUPLICATE` at the second (paths index the list as
written); a link's list still takes roles only. The JSON Schema's
`writableBy` says so. The stored definition gains `writable_by_people`
and `writable_by_agents` only when set, so every registered version is
byte-identical.

**The contract.** Comments only: `FIELD_WRITER_UNSPECIFIED` and
`Field.writable_by` now say people, not agents. `GetType` cannot yet say
which fields are open to agents (Later).

**Readings.** "Who writes what" ends with one new sentence kind,
`field_agents`: "No agent writes its fields." or "An agent writes only
`f_a` and `f_b`.", with ", except one that names a role the agent holds" /
", and a field that names a role the agent holds", and "; an agent acting
for a customer sets only what the customer may" when the Type has customer
fields. A field only agents write reads "Only agents[ and holders of R]
write `f`." (`field_agents_only`); a field people and a role write no
longer reads as the role's alone. The three approved readings each gained
the line, approved by the owner. Its witness runs a story with agents.

**Agents in stories.** A person may be an agent (`bot: {agent: true,
roles: [...]}`, principal `agent:bot`, never a customer), and a step may
say an agent acts for a person: "bot for ana updates ...". The harness
starts one session per agent and user, over the whole namespace with
every power that user may grant (a customer's agent: read, create,
comment and every move). Test tooling only, as before.

**The bake-offs, re-run unchanged** (kernel side, their own stacks and
Types; their `results/` stay the earlier record):

| Run | Before | Now |
|---|---|---|
| Camunda bake-off, `test2-compromised.py kernel` | 31 of 36 refused | 32 of 32 refused |
| Temporal bake-off, purchase | 26 of 29 (2 succeeded, 1 allowed) | 27 of 27 |
| Temporal bake-off, access request | 15 of 19 (3 succeeded, 1 allowed) | 17 of 18 |
| The access request written blind | 16 of 19 | 18 of 18 |
| The staff-role Types | the agent refused | the same |

Both holes are refused on every Type, acting alone and acting for the
person. "File and submit a purchase in ana's name, acting for her" and
"ask for access in its own name" are now refused too (closed values on
create), so their follow-on attempts no longer run. Five attempts on
frozen fields now read `FIELD_NOT_WRITABLE` instead of `FIELD_FROZEN`. Still
allowed: ana's own request for no system (a person, the Type), and a
colleague changing ana's draft (track 1 item 4, "only the requester"), so
the bake-offs do not yet pass in full. (Item 4 has since closed the
colleague's edit where a Type says so: "Only the requester edits a draft,
as built", below.)

**Tests:** item service 11 new (the two attacks and the preparation; who
writes each kind of field, create, a transition's patch, clears, sets,
kernel writes, customers' agents, NOT_FOUND first); policy 1 (32 cases);
typedef 4; API 1; reading 2 and a witness; story 2; one scenario step.
About 40 existing tests moved to Types that open the field, or to a
person-written setup, assertions unchanged; the lists of every Type in
`types/` gained the two versions (52 to 54); w_leave@1's layout test gained
the line.

**Kernel changes (counted):** the field rule (`policy.MayWriteField`, the
item service's field rules and Create's order), the load checks, the
readings' sentence. No schema, migration or go.mod change; the proto change
is comments.

## Only the requester edits a draft, as built (2026-11-25)

Track 1, item 4 of `docs/roadmap.md`. After item 2 closed the agent's way
in, the bake-offs still showed one hole in a draft: a colleague (olaf, who
holds role staff like every person) changed ana's purchase before she
submitted it, and the manager then approved what olaf wrote
(`spikes/temporal-bakeoff/results/kernel-staff-role.md`). No Type could
say "only the requester". Now a Type can, state by state.

**The rule.** A state lists the fields only the Item's requester writes
while the Item is in it, shaped like `frozen`:

```yaml
states:
  - {id: draft, category: todo, requester_only: [f_amount, f_vendor, f_cost_centre, f_justification]}
```

Frozen means nobody changes a field in that state; `requester_only` means
only the requester does.

- **On top of `writable_by`, never instead of it.** The writer must pass
  `writable_by` (people, agents, roles) **and** be the requester. So an
  agent acting for ana writes her draft's field only where the field is
  also open to agents; a field `role:staff` writes stays the staff's and,
  in the draft, ana's alone among them.
- **The requester is `IsRequester`'s**, as for `allowed: [creator]`: the
  effective user (the user an agent acts for, else the actor) is the
  Item's effective creator (`created_on_behalf_of`, else `created_by`).
  Not `TookPartInCreation`: an agent that filed ana's draft does not
  write it later acting alone. An agent that filed a draft for itself is
  its requester. An Item a reaction created has a kernel actor as creator
  (or the user it is attributed to), so on the public door its listed
  fields are nobody's, or that user's: fail closed.
- **When it applies:** only while the Item is in a listed state, and again
  each time it comes back (a request sent back to its draft is the
  requester's again). A transition's patch is judged against the state it
  leaves, like frozen: ana submits with a patch; a colleague's patch on a
  move out of the draft is refused; a patch on the move into the draft is
  judged by the state it leaves. A move's `clears` is a write of the field
  it clears (as for `writable_by`); a move's `sets` is the Type's, untouched.
  A create always passes: whoever creates the Item is its requester.
- **The internal door is untouched:** hooks, timers and reactions write as
  before. A customer writes fields only on create, where they are the
  requester, so the customer rules and this one never meet.
- `policy.MayWriteInState(state, field, who, facts)` is the one pure
  answer; the item service asks it beside the field rules.

**The owner's decisions (Phase A):** all the proposed defaults.

- The syntax, `requester_only: [fields]` on a state, field by field; no
  "all fields" shorthand.
- The requester is `IsRequester`'s, and an agent for the requester needs
  the field open to agents as well.
- **An opt-in, by nature**: no Type changes meaning until it lists fields,
  so nothing in flight changes. No Type in `types/` uses it in this step.
- **"Re-run unchanged":** the bake-offs' scripts stay unchanged; the two
  staff-role spike Types (`spikes/temporal-bakeoff/kernel-staff-role/types/`,
  `purchase.v1.yaml` and `system_access.v1.yaml`) gain one
  `requester_only` line each, edited in place with a header comment (each
  run registers them fresh). A new version would not do: the scripts
  create `purchase@1`, and a create on the latest version is track 1
  item 6. A kernel default (an initial state's fields the requester's)
  would break every Type triaged by someone else (incident, ticket, PLAT).
- No proto change; the rule lives in `policy`, frozen stays in `lifecycle`.

**The refusal.** The existing `PERMISSION_DENIED` / `FIELD_NOT_WRITABLE`
with `fields` (no new code, no contract change), part of "not writable":
after who may write each field (`writable_by`), before frozen and held. A
field already refused by `writable_by` is named once, for that cause.
The item service's message: "only the requester writes these fields while
the Item is in draft: the request may not write f_amount". One refusal
names every field and says each cause, joined by "; "; an agent refused
only by this rule is not told about fields "open to agents". A refusal
without this cause reads as before, byte for byte.

**Load checks and schema.** Field by field, like frozen: an undeclared
field is `UNKNOWN_FIELD`, a field listed twice `DUPLICATE`, a field only the
kernel writes `CONFLICT` (no requester writes it), a field the same state
freezes `CONFLICT` (frozen means nobody). A field frozen in another state is
the normal case. The JSON Schema's state gains `requester_only`; the stored
definition carries it only when set, so every registered version is
byte-identical.

**Readings.** One sentence kind, `requester_only`, among what holds while
the Item is in the state, after what it freezes: "While it is in draft,
only the requester changes `f_amount` or `f_vendor`.", citing
`states[i].requester_only`. Its witness (`w_draft@1`): olaf refused with
`FIELD_NOT_WRITABLE`, olaf writing a field the draft does not list, ana
writing hers, and `FIELD_FROZEN` for ana once sent. No approved reading
changes: no Type in `types/` uses it.

**Not covered (Later):** a draft's links, readers and placement; a Type in
`types/` using it (`vacation_request@4`, `access_request@2`); `GetType`
showing it (the proto's `State` has `frozen` only); who last wrote a value,
in full, which is provenance (theme 2).

**The bake-offs, re-run with their scripts unchanged** (kernel side;
`results/` stay the earlier record):

| Run | After item 2 | Now |
|---|---|---|
| Camunda bake-off, `test2-compromised.py kernel` | 32 of 32 refused | 32 of 32 |
| Temporal bake-off, purchase | 27 of 27 | 27 of 27 |
| Temporal bake-off, access request | 17 of 18 | 17 of 18 |
| The access request written blind | 18 of 18 | 18 of 18 |
| **The staff-role Types** | 6 of 7: olaf changes ana's draft | **7 of 7: olaf refused** |
| The reviewer's list, kernel side | 2 allowed | the same 2 |

Still allowed, and outside this item: ana's own request for no system (the
Type, a person), and a colleague with no part in it reading ana's purchase
and its history (who sees what: these Types narrow nothing). They are the
roadmap's residues for the owner to decide.

**Tests:** item service 10 new (the attack first; only the listed fields;
agents and their messages; the effective creator; a transition's patch;
the rule again on return; clears and sets; frozen after submit; kernel
writes; a create); policy 1 (16 cases); typedef 5; reading 1 and a
witness. One existing test changed, agreed: `cite_test.go`'s list of what
a reading must cite gained `states[i].requester_only` (stricter, nothing
loosened). No API test: a test-only Type there would change
`TestListTypes_EveryVersionInOrder`; the refusal's mapping is the one
already tested.

**Kernel changes (counted):** the rule (`policy.MayWriteInState` and the
item service's field rules), the load checks, the reading's sentence. No
schema, migration, proto or go.mod change.

## Required means given by the mover, as built (2026-11-25)

Track 1, item 3 of `docs/roadmap.md`. Both bake-offs planted the reason for
a rejection before the decider acted, so the decider rejected without
giving one, though the Type requires it (`docs/research/camunda-bakeoff.md`,
test 2b; `docs/research/temporal-bakeoff.md`). Item 2 closed the agent's
way in; a colleague could still write `f_reason` first. A transition's
`required` was met by any value on the Item, whoever wrote it and
whenever. Now a Type can say that a required field must be given in the
move itself.

**The rule.** A transition lists, in `given`, fields of its `required`
that the move's own patch must set:

```yaml
- {from: finance_review, to: rejected, allowed: ['role:finance'], except: [creator],
   required: [f_reason], given: [f_reason]}
```

- **Both lists.** A `given` field is also in `required`, so `GetType`,
  the web form (which asks for the required fields) and every reader of
  `required` keep working without a contract change.
- **Given in the move** means the move's patch names the field and the
  write leaves it with a value. **The same value as before counts**: the
  decider re-sending the planted text is the decider's own choice. **An
  empty string counts**, as it counts as set for `required`; a Type that
  wants a non-empty reason says so in a guard (`size(f_reason) > 0`). A
  null in the patch unsets the field: not given.
- **The mover:** a person, through their patch. An agent acting for a
  person gives the field only where the Type opens it to agents
  (`writable_by`, item 2); a closed field is `FIELD_NOT_WRITABLE` first.
  `writable_by` says who may give a field; `given` says when. A customer
  moves with no patch, so never gives one.
- **The kernel's own moves carry no patch** (timers, reactions): they can
  never give a field. Such a Type is refused at load (below); at run time
  the move fails closed, as `MISSING_FIELDS`.
- **What it means is the lifecycle's.** `lifecycle.TransitionWith` judges
  required and given in one place (`checkRequired`): a `given` field counts
  only when `Input.Given` holds it. The item service passes in the fields
  the move's patch sets, and nothing else.

**The owner's decisions (Phase A):** the defaults, and on Q1 option B.

- **Q1, held moves: refused at load** (`HELD_TRANSITION`), as `clears` and
  `sets` are on a held move. Supporting them (judge the requester's patch
  when asked, freeze the given fields while the hold is open, let no check
  write them) is Later, for when a real Type needs it. Holds are unchanged.
- **Q2:** the API's one fixed message for `MISSING_FIELDS` now says both
  cases (below). Text only.
- **Q3:** the bake-offs' kernel Types gain `given: [f_reason]` on every
  rejection (15 lines in 9 files under `spikes/`), edited in place with a
  header note: the spike stacks register their Types fresh on every run.
  The scripts are unchanged.
- **Q4:** `""` counts as given. `""` as unset for every rule is Later.
- **Q5:** the kernel's own moves are refused at load, and fail closed at
  run time.
- **Q6:** `given` fields also in `required` (`MISSING_COUNTERPART`
  otherwise).
- **Q7:** an agent for the decider may give a field its Type opens to
  agents.
- **Q8:** no Type in `types/` uses it in this step.

**Order of checks and the refusal.** Unchanged: malformed -> not writable
-> frozen -> held -> **required fields (with given) and the guard** -> the
clears -> the sets -> invariants -> `allowed` and `except`. The refusal is
the existing `FAILED_PRECONDITION` / `MISSING_FIELDS`, with `fields`,
`from_state` and `to_state` (no new code, no proto change). Its `fields`
are the missing required and the missing given fields, in one sorted list.
The item service's reason has three wordings:

- only required fields missing: today's, word for word: `transition from
  "a" to "b" needs these fields set: f_amount`;
- only given fields missing: `... needs these fields given in the move:
  f_reason`;
- both: `... needs these fields set: f_amount; and given in the move:
  f_reason`.

The API's fixed message was "the transition needs fields that are not
set"; it is now **"the transition needs fields that are not set or not
given in the move"**.

**Load checks and schema.** Field by field, on `transitions[i].given[j]`:
an undeclared field is `UNKNOWN_FIELD`; a field listed twice
`DUPLICATE`; a field the move does not require `MISSING_COUNTERPART`; a
field no patch may name is refused: one only the kernel writes
(`WRITES_KERNEL_FIELD`), one the state the move leaves freezes
(`WRITES_FROZEN_FIELD`), one the move's own `allowed` names (`CONFLICT`),
and **one the state it leaves keeps for the requester (`requester_only`,
item 4) when the move has `except: [creator]`** (`CONFLICT`: only the
requester could give it, and the requester may not make the move; a move
`allowed` to a role still loads, since the requester may hold the role).
A field frozen in the state the move enters is the normal case: the move
entering a state may set what it freezes, so a reason given in a
rejection then stays. On the move, at `transitions[i].given`: a held
move (a check or a held reaction) is `HELD_TRANSITION`; a move a state's
timer or at-timer makes is `TIMER_NOT_ALLOWED`. On the reaction, at its
`to`, where a held target is refused: a move the Type's own
`created_inside` reaction makes, another Type's move reaction into it, or a
container move, is `CONFLICT`. (`clears` and `sets` of a given field were
already refused, as of a required one.) The JSON Schema's transition gains
`given`; the stored definition carries it only when set, so every
registered version is byte-identical.

**Readings.** A new part, `move.given`, read from `transitions[].given`.
The move sentence reads the required fields only to be set as before, then
the given ones: "Anyone with the role finance, but never anyone who took
part in creating it, moves it from finance_review to rejected, once the
mover gives `f_reason` in the move itself." With both: "..., once
`f_amount` is set and the mover gives `f_reason` in the move itself, only
if ...". Its witness (`w_reject@1`): a colleague plants a reason; the
decider's move without one, or with another field only, is refused
`MISSING_FIELDS`; with one, even the planted text, it is applied. No
approved reading changes: no Type in `types/` uses it.

**Not covered (Later):** held moves (above); a Type in `types/` using it
(`incident` and `ticket`'s `f_resolution`, `approval`'s `f_delegate`);
`GetType` saying which fields are given (the proto's `Transition` has
`required` only); and the web form, which sends only the required fields
that changed, so a decider confirming a pre-filled planted reason without
editing it is refused `MISSING_FIELDS` and the UI cannot say why. A
transition that declares every field it may set, and sets nothing else
(the roadmap's note from OpenNSW), was not taken up in this step. "Only
the decider writes the field" (`writable_by: ['field:f_x']`) would close the
write itself rather than the move: complementary, Later.

**The bake-offs, re-run with their scripts unchanged**, each compared row
by row with the same script run on `main` (kernel side; `results/` stay
the earlier record): every outcome is the same. No script plants a reason
as a person; the agent's plant is refused first, since item 2. Two rows
show the reworded message: the Camunda bake-off's `test0-paths.py`, "gus
rejects without a reason" (refused, then applied with one), and the
staff-role run's "fay can reject without giving a reason" (refused). The
Camunda bake-off's kernel list 32 of 32 refused; the Temporal bake-off's
measure 4 (purchase all refused, the access request's one row allowed
being ana's own request for no system), the blind-written access request
and the staff-role run (7 of 7), all unchanged. The proof of this item is
the item service's test of the attack (`TestGiven_Bakeoff_APlantedReasonDoesNotCount`).

**Tests:** item service 7 new (the attack first; the manager's rejection;
what counts as given; null or other fields; unchanged without `given`; an
agent for the decider; the order against `allowed`); lifecycle 5; typedef
7 (round trip, nothing new stored, what loads, ten load-check cases, held
moves, reactions' moves, the schema); reading 2 and a witness; API 1. No
existing test's code or assertion changed. Two fixture files, each a new
file: a schema corpus Type (`internal/typedef/testdata/schema/given.v1.yaml`)
and the witness Type (`internal/reading/testdata/types/w_reject.v1.yaml`).
One stub, `lifecycle.Input.Given`, landed with the tests.

**Kernel changes (counted):** the rule (`lifecycle.checkRequired`, the
item service passing the patch's fields), the load checks, the reading's
clause, the API's message. No schema, migration, proto or go.mod change.

## Waiting on me, step 1, as built: the view, derived at read time (2026-11-25)

Track 1, item 5 of `docs/roadmap.md`, the first of its five steps. Both
bake-offs' test 3 ("one inbox") found that the kernel could not say what
waits on a person: a 64-line script rebuilt it from the Types with one
search per move. Now one filter answers it, for every Type with no
per-Type code: `ItemFilter.waiting_on`, on `SearchItems` and `ListItems`.

A queue is a view, not a container (the owner's decision,
`docs/research/task-inboxes-taskana-polyflow.md`): a container is where an
Item belongs, a queue is who must act next. The view is read from the
Type: the moves out of each Item's current state, in the Item's own Type
version.

**The rule** (`internal/policy/waiting.go`: `WaitingMoves`, `NamedEntries`,
`WaitsOn`, `WaitsOnRole`; pure, beside `MayTransition`):

- **A move counts when its `allowed` names someone:** a role
  (`role:<name>`), a person (`principal:user:…`, `principal:agent:…`) or a
  field (`field:<id>`, the person or people the field holds).
- **An empty `allowed` is nobody's queue.** "Anyone may" is not "someone
  must": a Type that wants a queue names a role.
- **`creator` never counts.** A move for the creator only (withdraw a
  request, send a draft) is the requester's own, not work waiting on them.
  Beside a role (`allowed: [creator, 'role:x']`) the Item waits on x's
  holders, not on its requester.
- **`nobody` and the kernel's own actors** (the timer, a hook) do not count.
- **A state in category done or canceled never waits:** a reopen is not
  work.
- **`except` is applied to the person asking.** A finance member who asked
  for a purchase does not find it in their own view; their colleagues do.
- **Not evaluated: guards, `required` and `given` fields.** The view says
  "may act, as far as who they are goes"; the move may still be refused
  when asked. **An Item with an open hold is left out:** it waits on a
  check, not on a person (one indexed lookup).
- **Each Item follows its own version**, as its moves do (starting work,
  step 2b): a purchase made on version 1 stays in the queue version 1
  names when version 2 names another.

`WaitsOn` narrows a move's list to the entries that name someone and asks
`MayTransition`, so the rule for agents and the meaning of `except` are
the move's own, written once. A property test over 5,000 generated Types,
people and Items holds that whatever waits on someone is a move
`MayTransition` lets them make.

**The values of `waiting_on`:**

- **`me`:** the caller. For an agent, what its session may act on: the
  agent and the user it acts for both pass (a role must be the agent's
  too; a field that names a person never names their agent), within the
  session's scopes.
- **`role:<name>`:** that role's queue. Any member of staff may ask for
  it, and sees of it what they can already see. A queue is a role's:
  `except` is not applied (the purchase gus asked for is in the finance
  queue, and not in gus's own `me`). A role nobody holds, or that no Type
  names, is an empty queue.
- **Anything else is `INVALID_ARGUMENT` on `filter.waiting_on`,** another
  person's inbox (`user:fay`) included. So is **a customer asking**: a
  customer takes only the creator's moves, which do not count, until a
  Type can say that a state waits on its requester (step 5).

**How it is read.** Derived when it is asked; nothing is stored. The item
service (`internal/itemservice/waiting.go`) turns the rule into one more
SQL condition: for every registered Type version and state, each waiting
move becomes the condition under which the caller may make it ("true" for
a role they hold, read now, or their own ID; a test of the Item's field
for `field:<id>`; `except` as a test of who created the Item or its
container). States that share a condition are grouped into lists of
(type, version, state). The condition joins the visibility test and the
other filters **before the limit**, and every Item returned goes through
`policy.Project`, exactly as Search and List did. So the view composes
with types, states, categories, field and link filters, words,
`order_by`, paging and `min_seq`, and a page token is bound to
`waiting_on` like to the rest of its request. A test holds what the SQL
returns to `policy.WaitsOn` and `policy.WaitsOnRole` over a world with
every case.

Because nothing is stored, nothing can fall behind: a role granted shows
its queue at once and a role revoked hides it; a changed `field:` field
moves the Item to the person it now names; a new Type version changes
nothing for Items of earlier ones.

**The contract.** One new field, `string waiting_on = 6` on `ItemFilter`.
No new operation, error code, table or migration.

**pli.** `--waiting-on me|role:NAME` on `item search` and `item list`, and
`pli inbox [WORDS...] [--role NAME] [FILTERS] [PAGING]`, which is `item
search --waiting-on me` (with `--role NAME`, `role:NAME`) and prints the
same answer.

**The reading.** The first thing said of what holds while an Item is in a
state, before what the state freezes: "While it is in {s}, it waits on
{who}." (part `waits`), citing each move that names someone; the people
in the Type's order, each once, in the words a move's own sentence uses,
joined with ", or". It does not repeat `except`: the move sentences below
it do. A state whose moves name nobody, or in category done or canceled,
has no such sentence. `states[].category` stays among the keys no
sentence reads: it decides which views show the Item, as its reason
already said. The three approved readings each gained one sentence:
incident@3 and incident@4, "While it is in new, it waits on anyone with
the role desk_agent."; vacation_request@3, "While it is in submitted, it
waits on whoever `f_approver` names."

**The story grammar grew by one read step**, which the sentence's witness
needs (`internal/story`, test tooling: no binary links it):

```
time person finds waiting ITEM, ITEM, ...   the person's view holds exactly these Items
time person finds nothing waiting           the person's view is empty
```

It reads the item service's public Search with `waiting_on = me`, as the
person, every page. Like `sees`, it may not be refused, and `Check` holds
its Items to the ones the story knows.

**What the rule says of the Types in `types/`,** each written before it
existed (none was changed):

- **The trackers (story, task, bug, epic, sprint) name nobody,** so their
  Items wait on nobody. "Assigned to me" stays a field filter.
- **`incident@4` in `new` waits on `desk_agent`** only because its cancel
  move names the role; the move that picks an incident up is open to
  anyone. Right by luck, and now in an approved reading.
- **`ticket@7` in `in_progress` shows for every approver,** though the
  approval is for some tickets only: the guard that says which is not
  evaluated.
- **`leave_balance@1` in `open` waits on `hr_admin`:** the move that
  closes a balance names the role. True, and not what an HR admin would
  call work.

The first, third and fourth are what a `waiting: true|false` key on a
transition would let a Type say (step 5, Later).

**What it costs** (`go run ./cmd/bench -waiting`; Postgres 17; 400,000
Items in one namespace, 55,000 of them open and waiting; a page of 50,
30 runs each):

| Read | median ms | p90 ms |
| --- | --- | --- |
| A finance member, `me` (a role: 22,500 match) | 30.7 | 34.8 |
| The finance queue, asked by someone outside it | 29.4 | 40.2 |
| The CFO, `me` (2,500 match) | 36.2 | 50.7 |
| A manager with 500 waiting, `me` (a field) | 32.4 | 57.1 |
| A manager with nothing waiting | 27.9 | 33.4 |
| A manager who is also in finance | 32.5 | 37.2 |
| Someone nothing names | 40.6 | 47.8 |
| A finance member, purchases only | 57.8 | 74.7 |
| A finance member, ordered by amount | 42.5 | 49.8 |
| For comparison: a plain Type and state filter | 1.4 | 1.7 |

- **About 30 to 60 ms a page at this size,** twenty to forty times a plain
  filtered page. The cost follows the number of open Items in the states
  involved, not what is in the caller's view: someone with nothing
  waiting pays the same.
- **The condition names its Types and states twice.** The (type, version,
  state) lists alone made Postgres read every Item (50 to 83 ms); two
  `= any` tests that say nothing new let it walk
  `items_namespace_type_state`.
- **Step 3's question.** A throwaway prototype of rows stored per open
  Item (Phase A, Postgres 16) read a page in 0.3 to 0.5 ms whatever the
  size, for about 0.01 ms more on each move. Whether the pilot needs
  that is the owner's decision on these numbers.
- **The bench loads its Items with one INSERT,** not through the item
  service: an exception to "every write goes through the item service",
  **accepted by the owner for this measuring tool** (2026-11-25). Reads
  only are measured, and a Create each would take most of an hour. The
  loader can reach only the bench's own database: the mode starts a fresh
  Postgres container itself (`testdb.Start`, removed when it returns),
  runs the INSERT on that container's pool, and takes no database
  address, by flag or by environment. Nothing else may load Items so.

**The bake-offs.** The Temporal bake-off's `test_inbox.py`, unchanged,
gives the same rows as before: the script's own four for fay, and "Does a
claim hold?" still "allowed: the claim is only a field". No script row
tests the view; `pli --as fay inbox` now answers in one call what
`kernel-inbox.py` rebuilds. The claim is step 2.

**Tests.** New: policy 5 (31 cases of who a move names, and the property
test); item service 13 (the bake-offs' four of six first); API 3; pli 3;
story 4; reading 2 and a witness. No existing test's code changed. One
new fixture file, the witness Type
(`internal/reading/testdata/types/w_queue.v1.yaml`). Assertion changes,
approved by the owner at the tests gate: the three approved readings.

**Kernel changes (counted):** the rule (`policy`), the filter (the item
service and the API's one field), the reading's sentence, pli's command
and flag. No schema, migration or go.mod change; the proto grew by one
field.

**What is left** (the owner's split): step 2, a claim that binds; step 3,
who may act stored per open Item, if these numbers ask for it; step 4, a
container as a filter, capped counts, "who am I" with roles, and the web
Inbox on the view; step 5, a state that waits on its requester (and so a
customer's view).

## Out of scope for the spike

The real UI (the generic web UI and the PoC apps above are
proof-of-concept clients, not it), email intake, multi-tenancy (the
`namespace_id` column exists, with one default namespace), a dedicated search
engine. (Business-hours calendars and SLAs moved into Jira part 2.)
