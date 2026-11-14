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
- States: each has a category — `triage, backlog, todo, active, waiting, done, canceled, none`.
  `none` means "not work": work views leave it out. A Type that declares no
  states gets one implicit state, `default`, with category `none`.
- Transitions: from/to, guard expression, required fields, who may act, action references.
  - Who may act is the transition's `allowed` list. An entry is `role:<name>`
    or `principal:<id>`. Absent or empty means anyone who can see the Item.
  - A timer acts as `system:timer` and gets no silent exemption: if the
    transition it uses has an `allowed` list, the list must include
    `principal:system:timer`, or the Type is refused at load.
- Timers/SLAs: separate objects with start/pause/resume/stop conditions (later).
  Spike stand-in: a state may carry one timer,
  `{id: in_progress, category: active, timer: {after: 48h, to: open}}`. `to` must
  be a transition the Type declares from that state, so a timer can never make a
  move a person couldn't. The timer counts from the event that entered the state.
- Approvals: a waiting step completed by a token (later).
- Items stay on the Type version they were created with. Versioning rule:
  **pin the lifecycle, evolve the schema, float the policy.**

Guards are CEL expressions (`cel-go`), compiled when a Type loads. Every field
is a variable named by its ID, typed by the schema and nullable: unset is
`null`, so "is set" reads `f_x != null`. A guard that cannot be evaluated does
not hold.

Hooks (Step 6) are declared in the Type version, pinned with the lifecycle:
`{id, on: enter, state}` for an **enrich** hook (reads some fields, calls out,
writes others, never holds the Item, optional `fallback`), or `{id, on:
transition, from, to}` for a **check**. Each names an action (code in
`internal/hooks`, never in the Type) with `reads` and `writes`, matched to the
action by position; the action's arity is checked at load. A transition may
be an `escape` (cancel): it may leave a held state.

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

## Policy

- **Principals are typed IDs**, given by the caller: `user:<id>`, `agent:<id>`,
  `group:<id>`, and for the kernel's own actors `system:<id>` (`system:timer`)
  and `hook:<type>:<hook-id>` (a Type's hook).
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
  - It serves `Update`, `Transition`, `ResolveHold` (a check settles its hold)
    and `HookInput` (a hook reads its declared `reads`, nothing else).
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
  both must pass `allowed`.
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
    readers before vs after) needs `ConfirmWiden`; otherwise `ErrInvalid`.
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
  the root), moves (target: the destination's scope) and policy changes that
  widen readers, `Inherit` included. A plain transition puts no content anywhere.
  - Approval is a separate act by the human, not a flag the agent could set:
    `ApproveWiden(user, session, scope)` records an audited row in
    `agent_session_approvals` for that one scope. It counts only when the
    approver is the `on_behalf_of` user of the agent's request, and the approver
    must see the scope (else `ErrNotFound`). The agent's retry then succeeds.
  - An `agent:` actor without a session is `ErrInvalid`, on reads and writes.
  - **Sessions are minted by the server**, never chosen by the caller:
    `StartAgentSession(agent, on_behalf_of)` returns a session ID and stores
    `agent_sessions(session, agent, on_behalf_of, created_at)`. Every agent read
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
```

## API contract (first cut)

```protobuf
service ItemService {
  rpc Create(CreateRequest) returns (CreateResponse);
  rpc Update(UpdateRequest) returns (UpdateResponse);
  rpc Transition(TransitionRequest) returns (TransitionResponse);
  rpc Link(LinkRequest) returns (LinkResponse);
  rpc Archive(ArchiveRequest) returns (ArchiveResponse);
  rpc Get(GetRequest) returns (GetResponse);
  rpc Search(SearchRequest) returns (SearchResponse); // accepts min_seq
}
message WriteResult { string item_id = 1; int64 commit_seq = 2; int64 item_seq = 3; }
// Each RPC has its own response message, so it can grow without breaking
// clients. The five write responses each wrap a WriteResult.
// Every write request carries idempotency_key and principal (actor, on_behalf_of).
// UpdateRequest and TransitionRequest take an optional expected_item_seq;
// TransitionRequest also takes an optional fields patch and a bounded wait,
// and TransitionResponse adds outcome, hold_id and reason. Item carries its
// latest hold.
```

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

## Out of scope for the spike

Real UI, email intake, business-hours calendars, multi-tenancy (the
`namespace_id` column exists, with one default namespace), a dedicated search
engine, SLA objects beyond one timer, approvals (stretch only).
