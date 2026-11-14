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
- Timers/SLAs: separate objects with start/pause/resume/stop conditions (later).
  Spike stand-in: a state may carry one timer,
  `{id: in_progress, category: active, timer: {after: 48h, to: open}}`. `to` must
  be a transition the Type declares from that state, so a timer can never make a
  move a person couldn't. The timer counts from the event that entered the state.
- Approvals: a waiting step completed by a token (later).
- Items stay on the Type version they were created with. Versioning rule:
  **pin the lifecycle, evolve the schema, float the policy.**

For the spike, guards can be simple Go-evaluated expressions; we'll choose CEL
or similar later.

## Write path

```
client → item service (Go, stateless)
           check the request is well formed
           BEGIN
             claim idempotency key (namespace, actor, key)
               already used → return the stored result, write nothing
             lock Item row (SELECT … FOR UPDATE)
             validate schema + lifecycle + policy against the locked row
             write Item row (item_seq + 1)
             take next commit_seq (commit_counter row, locked)
             insert item_events (item_id, item_seq, commit_seq, attribution…, op, payload)
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
  checked under the row lock; zero means no check). Two different refusals:
  - `ErrRejected` — the rules say no; retrying is pointless (HTTP 422).
  - `ErrStale` — someone changed the Item first; re-read and retry (HTTP 409).
- `commit_seq` lets later reads/searches wait until the write is visible. For
  the spike it comes from a single locked counter row, so numbers become visible
  in commit order (a `bigserial` does not guarantee that). This serializes writes;
  revisit after the spike (snapshot-xmin watermark).

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
  make a wrong move. Stale, rejected and "Item not found" are final; any other
  failure retries with backoff, without limit.

## Policy

- **Scopes**: a scope node per container that narrows access. An Item's read
  access comes from its nearest ancestor scope that narrows it. Each Item stores
  one `scope_id`.
- A child can **narrow** its container's access but never widen it by inheritance.
- **Grants** widen explicitly (e.g. share one ticket with a customer). A grant
  attaches to a scope node. Effective readers = (parent readers narrowed by this
  node) + grants.
- API in `internal/policy`: `Authorize(principal, action, item)`,
  `VisibleScopes(principal)`, `Project(principal, item) → item with hidden fields redacted`.
- Policy data lives in Postgres; evaluation is in Go. No external authz service.
- **Boundary.** `Project` applies to every path that reaches a principal: people,
  agents, connectors, webhooks, search results. Internal infrastructure that
  only receives event shapes without field values (relay → our Temporal) is not
  a principal, so those payloads do not pass through `Project`.

## Agents (later in the spike)

- An agent is its own principal with a human sponsor.
- Acting for a user: rights = user's rights ∩ agent's grant.
- Every event records attribution:
  - `actor`: whose permissions were checked (user, agent, or `hook:<id>@<version>`)
  - `on_behalf_of`: the human at the root, if different
  - `agent_session`: set when an agent acts
  - `originating_hook`: set when a hook or connector caused the write
  - `causation_id`: the event that caused this write
  - `correlation_id`: groups a whole chain
  - `hop`: depth in the chain

  Build the columns in Step 2; callers fill them as they appear. Since Step 3,
  write requests carry them (`agent_session`, `originating_hook`, `causation_id`,
  `correlation_id`, `hop`) as a Go-only field, not yet in the API.

  A timer's write is recorded as: `actor` = `system:timer`, `originating_hook` =
  `timer:<type>@v<version>:<state>`, `causation_id` = the `item:seq` of the event
  that entered the state, `correlation_id` inherited from that event, `hop` = that
  event's hop + 1. Its idempotency key is `timer:<item>:<entered seq>:<expected seq>`.

## Schema (first cut)

```sql
create table types        (id text, version int, definition jsonb, primary key (id, version));
create table scopes       (id uuid primary key, parent_id uuid references scopes, narrows boolean not null);
create table scope_readers(scope_id uuid references scopes, principal text, primary key (scope_id, principal));
create table grants       (scope_id uuid references scopes, principal text, granted_by text,
                           created_at timestamptz not null default now(), primary key (scope_id, principal));
create table items        (id uuid primary key, namespace_id text not null default 'default',
                           type_id text not null, type_version int not null,
                           container_id uuid references items, scope_id uuid references scopes,
                           state text not null, fields jsonb not null default '{}',
                           item_seq bigint not null default 0, updated_at timestamptz not null default now());
create table item_events  (item_id uuid references items, item_seq bigint, commit_seq bigint not null unique,
                           actor text not null, on_behalf_of text, agent_session text,
                           originating_hook text, causation_id text, correlation_id text,
                           hop int not null default 0, op text not null, payload jsonb,
                           created_at timestamptz not null default now(), primary key (item_id, item_seq));
create table item_event_values (item_id uuid, item_seq bigint, field_values jsonb not null,
                           primary key (item_id, item_seq),
                           foreign key (item_id, item_seq) references item_events);
create table commit_counter (id boolean primary key default true check (id), last_seq bigint not null);
create table outbox       (item_id uuid, item_seq bigint, delivered_at timestamptz,
                           primary key (item_id, item_seq));
create index outbox_pending on outbox (item_id, item_seq) where delivered_at is null;
create index items_container on items (container_id);
create table idempotency  (namespace_id text not null default 'default', actor text not null, key text not null,
                           request_hash text not null, result jsonb not null,
                           created_at timestamptz not null default now(),
                           primary key (namespace_id, actor, key));
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
// TransitionRequest also takes an optional fields patch.
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

**Follow-up spike, Steps 6–7 (after Step 5):** holds and connectors. The
plan is in the "Work kernel: Spike plan" doc. In short: Temporal activities
run `on_enter`, `on_exit` and `on_complete` hooks and write their results back
through the normal write path. A blocking check holds a transition until it
allows, denies or corrects it. Write APIs take a bounded `wait`. The item
service makes no synchronous outside calls.

**Go checkpoint, after Step 3:** if most time went into fighting the language
rather than the design, stop and decide whether to rerun in Java.

## Out of scope for the spike

Real UI, email intake, business-hours calendars, multi-tenancy (the
`namespace_id` column exists, with one default namespace), a dedicated search
engine, SLA objects beyond one timer, approvals (stretch only).
