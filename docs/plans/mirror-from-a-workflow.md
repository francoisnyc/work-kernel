# Plan: Items mirrored from a workflow

A brief for the orchestrator. Proposed 2026-11-28. The owner is trying the
kernel with another system whose Temporal workflows are clients of the
kernel. The target is **mixed**: the workflow owns the process (its stage
and its own fields), people and agents own the human part in the kernel
(who decides, the queue, the rules), each field with one owner. **Mirror
first**: the workflow owns the whole Item and the kernel mirrors it; the
mixed case follows on the same pieces.

## Why the kernel lacks it today

Every write is a patch judged by the Type (`UpdateItem` changes the fields
it names; `TransitionItem` moves by the Type's rules). There is no
external ID, no upsert, no full replace, and no order from the caller's
side: `etag` is an if-match the caller must read first, `request_id`
removes duplicates but not reordering, `item_seq` and `commit_seq` are the
kernel's own. A workflow that owns a record needs four things the kernel
does not have:

- **find or create by its own ID** (an upsert by external ID);
- **send its whole record** and have the kernel hold exactly that (a full
  replace of the fields it owns: absent means cleared);
- **its order kept:** activities retry and time out, several runs (and
  Continue-As-New) write the same record, so a late write can land after a
  newer one. The kernel must keep the newest by the workflow's own
  sequence number, without a read first;
- **an identity of its own,** neither a person nor an agent.

And one rule the kernel must state: a mirrored state is set by its source,
not reached by a transition, so the Type's transitions do not apply to it.

## Phase B: the mirror (this brief builds it)

### 1. A service principal

`service:<id>`, a program outside the kernel (an orchestrator, a mirror):
a new principal kind beside `user:`, `agent:`, `customer:` and `system:`.
Authenticated as people are today (a dev token; sign-on later). Closed by
default: it reads and writes only what a Type names it for, is never in a
namespace's general audience, holds no role unless granted one.

### 2. A mirrored Type

A Type declares its mirror:

```yaml
id: order_case
version: 1
mirror:
  by: service:orders        # the one principal that writes it
  state: true               # the state comes from the source
  owns: all                 # or a list of field IDs (the mixed case, later)
states:
  - {id: received, category: waiting, label: Received}
  - {id: picking,  category: active,  label: Being picked}
  - {id: shipped,  category: done,    label: Shipped}
```

Checked at load, failing closed:

- `by` is a `service:` principal;
- with `state: true`, the Type has no transitions, no timers and no hooks of
  its own (nothing moves it but its source); with `owns: all`, no field is
  `writable_by` anyone else;
- no invariant reads a mirrored field (the source is the truth: refusing
  its write would only make the copy drift; the kernel checks what it
  judges);
- other Types may link to it, contain it or be contained by it, and react
  to it (a case waiting until its order is `shipped`: `child_entered`,
  `linked_entered` and guards over `children.` and `links.`, as for any
  Item). The kernel's rules apply to what is built *on* the mirror.

The reading says it in words: "An order case is kept by service:orders,
which sets its state and every field; no one else changes it."

### 3. One new write: `MirrorItem`

An upsert, one Postgres transaction like every write (Item row, event,
outbox row; the row locked, so `item_seq` keeps no gaps):

- **in:** `parent`, `request_id`, `type` (a mirrored Type), `external_id`
  (the source's own ID, opaque, unique per namespace and service),
  `source_seq` (a positive integer, the source's order), `state`, `fields`
  (the whole record), and on create only `container` and readers;
- **find or create** by (namespace, service, `external_id`), under a lock
  so two first writes for one ID create one Item;
- **order:** if `source_seq` is not greater than the last one applied to
  this Item, nothing is written and the answer is `SUPERSEDED` with the
  Item as it stands. Not an error: the activity must not retry it;
- **replace:** the owned fields become exactly the record sent (a field it
  owns and leaves out is cleared); fields it does not own are untouched
  (none in this phase, all of them in the mixed case);
- **state:** set as sent; it must be one of the Type's states;
- **refused** unless the caller is the Type's `mirror.by`; refused for any
  other door: no person, agent or reaction updates or moves a mirrored
  Item (`UpdateItem`, `TransitionItem` refuse with `PERMISSION_DENIED`);
- **recorded:** an event of a new kind, `mirrored`, with the actor
  (`service:orders`), the field IDs changed and the state (opaque IDs only,
  kernel rule), and the `source_seq`; `request_id` makes a retried call
  return the first answer, as for every write;
- **out:** the Item, `item_seq`, `commit_seq`, and the outcome (`APPLIED`
  or `SUPERSEDED`).

The Item shows its mirror on every read: `mirror {service, external_id,
source_seq}`, through `policy.Project`. Search can filter by
`external_id`. The relay and the per-Item workflow see a `mirrored` event
as any other, so reactions on other Types fire.

### 4. The lifecycle stays in one place

Setting a mirrored state is a lifecycle decision like a transition, so it
lives in `internal/lifecycle`'s pure function (a `mirror` input beside
`transition`), used by the item service and the workflow alike (kernel
rule).

### 5. The workflow's side (for the other system; a sample, not kernel code)

- `source_seq`: a counter in the workflow's state, increased before each
  write and carried across Continue-As-New, so replay gives the same
  numbers (or the source's own record version, if it has one);
- `request_id`: the workflow ID and that number, so a retried activity is
  one write;
- `SUPERSEDED` is success; any other refusal is a bug to see, not to
  retry.

A short Go sample of that activity in `examples/`, with a test under
Temporal's `testsuite` where an early attempt lands after a later write.

### Acceptance (the negative tests first)

1. A write older than the last applied is `SUPERSEDED`, writes nothing, and
   the Item keeps the newer record.
2. The same `request_id` twice gives the first answer, one event.
3. A replace clears an owned field the record leaves out.
4. Any principal but the Type's mirror is refused, on every door.
5. A Type with `state: true` and a transition, timer or hook is refused at
   load; an invariant over a mirrored field is refused at load.
6. Two first writes for one `external_id` at once create one Item.
7. A case whose guard waits on a mirrored child moves when the mirror sets
   the child `shipped` (a reaction across the mirror).
8. History shows `service:orders` and each `source_seq`.

## Phase M: mixed (next, on the same pieces)

- `owns:` a list: the mirror replaces only those fields; the others belong
  to people and agents under the Type's rules as today.
- The state: either still the source's (`state: true`), with people's
  decisions in fields the workflow reads; or the Type's, the workflow then
  moving it with `TransitionItem` as a service principal, judged like
  anyone.
- **Events out to the workflow:** when a person decides, a signal to the
  workflow that follows the Item; a new outbound path, through
  `policy.Project` (shared with "Ask a person").
- One owner per field, which `writable_by` and `owns` together express;
  the load check refuses a field with two.

## The owner's decisions (2026-11-28)

All five agreed:

1. A new principal kind, `service:<id>`.
2. A new write operation, `MirrorItem`: the kernel rules' list of writes in
   `CLAUDE.md` gains it, and `docs/design.md` a section.
3. A mirrored state is set by its source, outside the Type's transitions,
   declared on the Type and checked at load.
4. In this phase, no invariant over mirrored fields.
5. Order: Phase B right after the Inbox's look, before track 2's authoring
   test.
