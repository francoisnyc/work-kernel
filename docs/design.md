# Work kernel — design summary for the spike

This is the settled design, condensed from the design docs ("Work kernel: …" in
my Claude artifacts). Treat it as the spec. Raise questions; don't silently
deviate.

## The idea

Every unit of work is an **Item** of a **Type**. Trackers (Jira-like), chat
(Slack-like) and a helpdesk will be apps built on the kernel. The kernel stays
small; everything product-specific is a Type or an app.

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

## Type

A Type is versioned YAML data that **names code but never contains it**.

- Fields: stable ID, type, options, defaults, invariants as expressions.
- States: each has a category — `triage, backlog, todo, active, waiting, done, canceled`.
- Transitions: from/to, guard expression, required fields, who may act, action references.
- Timers/SLAs: separate objects with start/pause/resume/stop conditions (later).
- Approvals: a waiting step completed by a token (later).
- Items stay on the Type version they were created with. Versioning rule:
  **pin the lifecycle, evolve the schema, float the policy.**

For the spike, guards can be simple Go-evaluated expressions; we'll choose CEL
or similar later.

## Write path

```
client → kernel service (Go, stateless)
           validate schema + lifecycle + policy
           BEGIN
             lock Item row (SELECT … FOR UPDATE)
             write Item row (item_seq + 1)
             insert item_events (item_id, item_seq, commit_seq, actor, on_behalf_of, op, payload)
             insert outbox (item_id, item_seq)
             store idempotency key → result
           COMMIT
         → returns { item_id, commit_seq, item_seq }
```

- Same idempotency key again → return the stored result, write nothing.
- `commit_seq` lets later reads/searches wait until the write is visible.

## Durable behavior (Temporal)

- Most Items never get a workflow. A workflow starts **lazily** when an Item
  enters a state that needs timers or waits.
- **Relay** (`cmd/relay`): reads undelivered outbox rows, delivers each Item's
  events **in item_seq order**, one at a time, using **Update-With-Start**
  (workflow ID = item ID, conflict policy USE_EXISTING, update ID = `item:seq`).
  Marks the row delivered once the update is accepted.
- **Workflow** (`internal/workflow`): keeps `lastAppliedSeq`. Duplicate → accept
  and ignore. Gap → reject so the relay retries from the lowest undelivered seq.
  Carries `lastAppliedSeq` across continue-as-new. If the workflow had closed, a
  new run reloads state from Postgres.
- The workflow is a thin scheduler of timers and waits. It calls the same pure
  transition function as the kernel; timer-driven transitions go back through the
  kernel's write path (as an activity), so history stays in one place.

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

## Agents (later in the spike)

- An agent is its own principal with a human sponsor.
- Acting for a user: rights = user's rights ∩ agent's grant.
- Every event records `actor` and `on_behalf_of`.

## Schema (first cut)

```sql
create table types        (id text, version int, definition jsonb, primary key (id, version));
create table scopes       (id uuid primary key, parent_id uuid references scopes, narrows boolean not null);
create table scope_readers(scope_id uuid references scopes, principal text, primary key (scope_id, principal));
create table grants       (scope_id uuid references scopes, principal text, granted_by text,
                           created_at timestamptz not null default now(), primary key (scope_id, principal));
create table items        (id uuid primary key, type_id text not null, type_version int not null,
                           container_id uuid references items, scope_id uuid references scopes,
                           state text not null, fields jsonb not null default '{}',
                           item_seq bigint not null default 0, updated_at timestamptz not null default now());
create table item_events  (item_id uuid references items, item_seq bigint, commit_seq bigserial,
                           actor text not null, on_behalf_of text, op text not null, payload jsonb,
                           created_at timestamptz not null default now(), primary key (item_id, item_seq));
create table outbox       (item_id uuid, item_seq bigint, delivered_at timestamptz,
                           primary key (item_id, item_seq));
create table idempotency  (key text primary key, result jsonb not null, created_at timestamptz not null default now());
```

## API contract (first cut)

```protobuf
service ItemService {
  rpc Create(CreateRequest) returns (WriteResult);
  rpc Update(UpdateRequest) returns (WriteResult);
  rpc Transition(TransitionRequest) returns (WriteResult);
  rpc Link(LinkRequest) returns (WriteResult);
  rpc Archive(ArchiveRequest) returns (WriteResult);
  rpc Get(GetRequest) returns (Item);
  rpc Search(SearchRequest) returns (SearchResult); // accepts min_seq
}
message WriteResult { string item_id = 1; int64 commit_seq = 2; int64 item_seq = 3; }
// Every write request carries idempotency_key and principal (actor, on_behalf_of).
```

## Spike build order

A step is a unit of work, not a calendar day: it is finished when its "done when"
passes, however long that takes.

| Step | Build | Done when |
| --- | --- | --- |
| 1 | Schema, `Create` and `Get`, idempotency keys | Same idempotency key twice → same item_id and commit_seq; exactly one event and one outbox row |
| 2 | Load a Type from YAML; `Update` and `Transition` via the pure transition function; guards | Illegal transitions rejected with a reason; legal ones recorded |
| 3 | Outbox relay with per-Item ordering; interpreter workflow started lazily with Update-With-Start; one durable timer | Killing the worker mid-timer still fires it; duplicates and gaps handled |
| 4 | Scopes and `VisibleScopes`; Postgres full-text search with `min_seq`; one narrowed Item | A second user never sees the narrowed Item, even right after a change |
| 5 | Type v2 while v1 Items run; measure actions, history bytes, write latency; write up | Both versions complete; numbers recorded in `docs/journal.md` |

**Go checkpoint, after Step 3:** if most time went into fighting the language
rather than the design, stop and decide whether to rerun in Java.

## Out of scope for the spike

Real UI, email intake, business-hours calendars, multi-tenancy, a dedicated
search engine, SLA objects beyond one timer, approvals (stretch only).
