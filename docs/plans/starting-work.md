# Starting work: who may start a workflow, and what else starts one

Status: proposal, 2026-11-23. **Built:** step 1, the `create` power
(2026-11-23, PR #55), and step 2, the `start` block (2026-11-24; see
`docs/design.md`, "Starting work, step 2, as built", which wins where the
two differ). Nothing else is built. The Phase A answers and the review's answers (both
2026-11-23) are recorded as decisions below. A new table, a new door or a new principal kind needs a second OK
before it is built, even where this proposal names it.

Read with: `docs/roadmap.md` (themes 1, 2 and 4); `docs/design.md`
("Policy", "Who is calling", "Jira part 2, step 4a as built", "Hook
protocol (pli.hooks/v1), as built", "Ventoux V1, as built");
`docs/research/connectors-for-the-item-kernel.md` ("Inbound needs a stable
endpoint, a correlation key and a verdict for strays");
`docs/research/hook-protocol-versus-package-system.md` ("Auth and replay
protection", "Connector descriptors").

## In one page

Work starts in Ori in two ways that must fit together.

1. **Someone starts it.** A person or an agent calls `CreateItem`. Today
   any staff member who sees a container can create any Type in it. A
   Type gains an optional **`start` block**: in which container Types it
   may start (`in`) and who may start it (`allowed`). The block is judged
   by the same code in `policy` that judges a transition's `allowed`. A
   Type without a `start` block keeps today's behaviour exactly. Before
   that, agents are made to need the **`create` power** for every create.
   Today any write power is enough.
2. **Something starts it, or moves it.** An alert, a payment or a message
   from another system arrives through a **connector**. The connector
   checks the vendor's signature and sends a signed `pli.hooks/v1` event.
   The event's shape is a **source contract** in `contracts/`. A Type
   declares an **intake** that binds a source to one verdict: `create`,
   `correlate`, `create_or_correlate` or `signal`. An **inbox** keeps
   each delivery once. A kernel principal, `intake:<type>:<intake-id>`,
   performs the write. The Type must have a `start` block that names
   that principal, or it is refused at load. Inbound values fill fields; they never choose a
   move or a recipient. Timer starts reuse acting timers and create
   reactions. Signals are specified here, not built.

The smallest useful build is the create power, then part 1 alone. Both
are small, change nothing for existing Types, and close a real gap: for
security operations, "only the SOC creates cases, and only in a queue" is
part 1, before any alert arrives.

## Where things stand (as built)

- **`Create`** (`internal/itemservice/service.go`, `Service.Create`)
  checks, in order: the request's shape and its public principal; the
  Type version the caller names; the initial state; a `requester`, if any
  (`checkRequester`); the fields; the customer rule
  (`checkCustomerCreate`: a customer creates only Types its container's
  Type lists in `own` or `shared`); the kernel-written fields. Inside the
  transaction it checks that the container is visible (`checkVisible`),
  who may narrow (`checkPlacement`), the agent session's scopes
  (`checkInSession`), the write power (`checkWritePower`), the agent
  write rule (`checkAgentWrite`), uniqueness and invariants.
- **Nothing asks "may this principal create this Type here?"** for staff.
  Seeing the container is enough, for any Type, in any container.
- **Agents.** `checkWritePower` (`internal/itemservice/capabilities.go`)
  calls `item.Writes`: any power other than `read` allows every write.
  `design.md` says this on purpose ("v1 enforces read versus any write
  power"). So a session with only `edit_fields` can create Items today.
- **`policy.MayTransition`** judges a transition's `allowed` and `except`:
  `role:`, `principal:` and `field:` (judged for the actor and for the
  user it acts for; both must pass), `creator` (`IsRequester`, judged
  once), `nobody`, and in `except`, `creator` and `container_creator`
  (`TookPartInCreation`). `container_creator` exists only in `except`
  (`typedef.go` refuses it elsewhere).
- **The kernel creates Items** only through a `kind: create` reaction,
  `on: enter`. The target Type consents with `creates:`. The writer is
  `reaction:<type>:<id>`, with no human creator. `Service.Internal()`
  serves `Update` and `Transition`, never `Create`.
- **Nothing inbound exists.** The hook protocol goes outward (the worker
  calls a connector) and signs with Standard Webhooks. Async callbacks are
  specified, not built. Email intake is out of scope for the spike.

## Part 1: who may start a workflow

### 1.0 First commit: agents need the `create` power

**The change.** On `Create`, an agent session must hold `create`
(`item.PowerCreate`). Holding some other write power is not enough. Every
other write keeps the v1 rule (any write power), so this one commit
changes exactly one thing. The refusal stays what a read-only session
gets today: `ErrForbidden`, over the API `PERMISSION_DENIED` /
`NOT_ALLOWED`. The item service's error message names the missing
power (`this agent session may not create`); the API returns only
`NOT_ALLOWED`, with that reason's fixed message, so over the wire it
reads exactly as a read-only session's refusal (step 1, Phase A,
answer 4). No new code, no contract change.

It applies everywhere: to staff Types and customer Types, with or without
a `start` block. A customer's portal bot already holds `create` among its
fixed powers (`read`, `create`, `comment`, `transition:*`).

**The behaviour change.** A session started with write powers that do not
include `create` (for example `read,edit_fields`, or `comment`) could
create Items before. After this commit it cannot. Sessions with `create`,
and read-only sessions, see no change. `design.md`'s sentence "v1 enforces
read versus any write power" gains "except `Create`, which needs
`create`". That is a change to the settled design, approved as answer 1;
it goes into `design.md` with the code, not with this proposal.

**Every test it touches.** I read every test, script and config that
starts an agent session, then checked the reading by running it: in a
throwaway worktree (not committed), `Create` was made to require `create`
after `checkWritePower`, and `go test ./internal/itemservice/
./internal/api/ ./cmd/pli/ ./internal/workflow/` passed unchanged. The
scenario script and the browser tests were read, not run. What I found:

| Where | Session powers | Creates? | Effect |
| --- | --- | --- | --- |
| `internal/itemservice` helpers `wholeNamespace`, `withSession`, `agentFor` (`agent_scopes_test.go`, `agent_test.go`) | `allPowers`, which includes `create` | yes | none |
| `internal/itemservice/agent_scopes_test.go` `scoped(...)` calls (lines 95, 259, 372, 394, 433) | `read`; `read,comment,transition:approved`; `edit_fields` | no (reads, updates, a transition) | none |
| `internal/itemservice/links_agent_test.go` (lines 27, 34, 46, 147) | `read` or `edit_fields` | no (links) | none |
| `internal/itemservice/incoming_test.go`, `links_search_test.go`, `filter_types_test.go` | `read` | no | none |
| `internal/itemservice/customers_test.go` (`customerPowers`) | includes `create` | yes | none |
| `internal/workflow/approvals_e2e_test.go` | includes `create` | yes | none |
| `internal/api` `startSession(...)` calls | `read`; `read,edit_fields`; `comment` | no | none |
| `internal/api/main_test.go` grants (triage-bot for ana) | includes `create` | yes, where tests create as the bot | none |
| `internal/api/validate_types_test.go`, `customers_test.go`, `review_fixes_test.go` (portal bot) | include `create` | yes | none |
| `cmd/pli/part2_test.go`, `part3_test.go` | `read,create` where they create; `read,edit_fields` where they do not | | none |
| `scripts/scenario.sh` (triage-bot `read,edit_fields`, line 470) | | no (get, update, link) | none |
| `dev-tokens.example.yaml` grants | all include `create` | | none |
| `apps/poc`, `web/` | start no agent session | | none |

So **no existing test needs a change**, and no test asserts the refusal's
message text. Two comments become half true and need a word:
`TestAgentScopes_AnyWritePowerWrites` ("Any write power allows writes in
v1") and `TestLinkAgent_NeedsAWritePower` ("Any write power is enough in
v1"). Both test writes other than a create, and both stay as they are; only
the comment gains "except a create". If running the suite turns up
anything else, the commit stops and reports it; it does not change it.

**New tests (the spec for that commit):**

- An agent session with `edit_fields` only: `Create` is `ErrForbidden`,
  nothing written (no Item, no event, no idempotency row), and the same
  session still updates and transitions.
- The same with `comment` only, and with `transition:*` only.
- A session with `create` only: `Create` succeeds; `Update` succeeds too
  (any write power, unchanged).
- Over the API: `PERMISSION_DENIED` / `NOT_ALLOWED`, as for a read-only
  session.
- `pli`: an agent started with `--powers read,edit_fields` fails
  `item create` with `PERMISSION_DENIED NOT_ALLOWED`.

**Membership test.** Powers are already in the kernel (agents are a
property of every domain); this enforces one that was stored and not yet
enforced. No new concept.

### 1.1 The `start` block

```yaml
# on the Type being started
start:
  in: [desk, top]          # container Types it may start in; top = no container
  allowed: [role:desk_agent, field:f_owner, container_creator, principal:intake:incident:siem]
```

- **Optional.** No `start` block: anyone who may see the container may
  start the Type there, as today. A block with only `in`: anyone who sees
  the container, but only in those container Types. A block with only
  `allowed`: those principals, in any container.
- **`in`** lists container Type IDs (any version of each), plus the
  reserved word `top`: no container. Absent means anywhere, top level
  included. A Type named `top` is refused at load (`RESERVED_NAME`), so the
  word never means two things.
- **`allowed`** is a list of entries. Absent or empty means anyone who
  sees the container. The entries come next.

### 1.2 What each entry means in `start.allowed`

At create time the new Item does not exist, and its fields are the
caller's, so every entry reads **the container**, never the new Item.

| Entry | Matches | Judged | Notes |
| --- | --- | --- | --- |
| `role:<name>` | a principal holding the role in the namespace | for the actor and for its user; both must pass | as in a transition |
| `principal:<id>` | that principal: `user:`, or this Type's own `intake:` | for the actor and for its user | another Type's intake is refused at load |
| `field:<id>` | whoever the **container's** principal field `<id>` holds now (a set: any member) | for the actor and for its user; `field:` never matches an agent | every Type in `in` must declare `<id>` as a principal field, else refused at load; `in` must be present and must not hold `top` |
| `container_creator` | anyone who **took part in creating the container**: `TookPartInCreation` against the container's `created_by` and `created_on_behalf_of` | once, for the principal as a whole | **one word, one meaning**: the same function as in `except`. `in` must not hold `top` |
| `nobody` | no person, no agent, no customer | | only the kernel starts it, by a create reaction's consent (below) |
| `creator` | | | refused at load: the Item has no creator yet |

`nobody` and intakes: `nobody` means no person or agent starts the Type.
An intake starts it only if `allowed` names it with `principal:intake:…`,
so a Type that only the SOC's SIEM may start reads
`allowed: [principal:intake:incident:siem]`, and one that only a reaction
creates reads `allowed: [nobody]` (a reaction's consent is `creates:`, see
1.5). The two can be combined: `[nobody, principal:intake:…]` is
refused at load (`CONFLICT`), since naming an intake already shuts people
out.

**If the requester is needed** ("only the person this case is for may
open a follow-up inside it"), a separate word: `container_requester`,
matching `IsRequester` against the container (its effective creator: its
`created_on_behalf_of`, else its `created_by`). Proposed here, **not in
the first build**; nothing yet asks for it.

**Agents** are judged as in a transition: they need the `create` power
(1.0); `role:`, `principal:` and `field:` must pass for the agent and for
its user; `container_creator` is judged once. An agent that took part in
creating a container (it created it, or it acted for whoever did) matches.

**A person opening an Item for a customer** (`requester`) is judged as
the person, as every other check on that path already is.

### 1.3 Which version's rule applies

**The latest registered version's `start` block judges a create of any
version** of that Type. The Item's lifecycle stays pinned to the version
the caller names, as now. Without this, adding a block to `incident@4`
would do nothing against a client that still creates `incident@3`.

This follows the connectors report's rule that policy is always latest
("Compliance cannot wait for a drain"). It is the first place where one
version's declaration governs another's Items, so:

- **Deferred, not built in step 2** (this note and the next need a
  severity other than ERROR in `kernel.v1`; journal, Later, with Ventoux
  item 3): `pli type validate` says, on a draft that adds or changes a `start`
  block, which earlier versions it will govern. That is a note, not a
  problem: it stops nothing (a warning, once warnings exist, Ventoux
  item 3).
- Registering a later version that **drops** the block loosens every
  version at once. The semantic diff (Ventoux item 7) must report that as
  a loosening. Until it exists, `pli type validate` says so in its output
  (**deferred**, as the note above).
- A create reaction names a target version; its consent is checked on that
  version, as now; `in` is checked on the latest (1.5).

### 1.4 Customers

`own` and `shared` stay the customers' rule, unchanged. In addition:

- `in` applies to customers too. A customer creating an `incident` in a
  `desk` passes `own: [incident]` on the desk **and** `in` on the
  incident.
- `start.allowed` judges staff and agents only. A customer, or an agent
  acting for one, is never matched by `role:`, `principal:` or `field:`
  anyway (`design.md`, step 4a); applying `allowed` to them would shut
  every portal out of a Type with a block. Their gate is `own`/`shared`.
- **A load check** (stage 3, `START_NOT_IN`): an `own` or `shared` entry
  that names a Type whose latest `start.in` excludes the declaring
  container Type is refused, on the container Type's `own[i]` or
  `shared[i]`. Without it a portal would offer what the kernel then
  refuses.

### 1.5 Creates by the kernel

- **Create reactions**: the target's `creates:` consent is enough;
  `start.allowed` does not judge them. But the target's latest `start.in`
  must admit the source Type (the reaction's Item is created inside it).
  Otherwise the Type is refused at load: `START_NOT_IN`, stage 3, on the
  reaction. Since 1.3 makes the latest version govern, registering a new
  target version whose `in` drops a source Type is refused while a
  registered source Type still creates it.
- **Intakes** (part 2) are judged by `start.allowed` and `start.in` like
  any principal, through `principal:intake:…`.

### 1.6 Order of checks, and refusals

`start` is checked **after everything that answers `NOT_FOUND`** and
before the rest of the agent checks, in the same transaction that locks the
container, so a container moved or re-fielded between the check and the
commit cannot slip through. Order (**as built, corrected**: the agent
session's scopes come in step 1, not 4, since `design.md` puts `NOT_FOUND`
before any other check):

1. The container is visible to the caller (and to `on_behalf_of`), a
   customer's container is checked again under the lock, and an agent
   session's scopes cover it. Otherwise `NOT_FOUND`, as now, and first: the
   caller learns nothing about a container it cannot see.
2. `in` (latest version): the container's Type, or `top`. Otherwise
   `ErrForbidden`, over the API `PERMISSION_DENIED` / `NOT_ALLOWED`;
   the item service's message names the reason `START_NOT_IN`.
3. Customers: `own`/`shared` (as now). Staff and agents: `start.allowed`,
   judged by a new pure function `policy.MayStart(start, who,
   ContainerFacts)`, which shares its entry matching with `MayTransition`.
   Otherwise `PERMISSION_DENIED` / `NOT_ALLOWED`; the item service's
   message names the reason `START_NOT_ALLOWED`.
4. Then placement for a narrow (judged by the latest version's
   `placement_writable_by`, since step 2b), the write and `create` powers
   and the agent write rule, as now. (A customer's `own`/`shared` check still runs
   before the transaction too, so for a customer it refuses before `in`;
   over the API both read `NOT_ALLOWED`.)

No new error code, and no contract change: the two reasons go in the
item service's message only (decided at review, 2026-11-23). The API
returns only `NOT_ALLOWED`, with its fixed message: `apiError` never
sends the item service's text, so an API or `pli` caller cannot tell the
two apart, nor either from any other `NOT_ALLOWED` (corrected at step 1,
Phase A, answer 4). Saying why over the API is under Later.

Why `in` is checked before `allowed`: it is static (Type against Type),
and the caller sees the container, so saying "not here" leaks nothing.

### 1.7 Load checks (`pli type validate`)

All through `typedef.Check`, so `pli type validate` and `ValidateTypes`
need no change of their own.

| Check | Code | Stage | Path |
| --- | --- | --- | --- |
| `start` has a key other than `in` and `allowed` | `UNKNOWN_KEY` | 1 | `start.<key>` |
| a Type named `top` | `RESERVED_NAME` | 2 | `id` |
| an entry that is not `role:`, `principal:`, `field:`, `container_creator` or `nobody`; `creator` | `INVALID_VALUE` | 2 | `start.allowed[i]` |
| a `principal:` that is not `user:` or this Type's `intake:`; an `intake:` the Type does not declare | `INVALID_VALUE`; for an undeclared intake, `UNKNOWN_INTAKE` (new, with intakes) | 2 | `start.allowed[i]` |
| `field:` or `container_creator` with no `in`, or with `top` in `in` | `CONFLICT` | 2 | `start.allowed[i]` |
| `nobody` with any other entry | `CONFLICT` | 2 | `start.allowed[i]` |
| `in` names a Type that is not loaded | `UNKNOWN_TYPE` | 3 | `start.in[i]` |
| `field:<id>` missing on a Type in `in`, or not a principal field there | `UNKNOWN_FIELD` / `WRONG_FIELD_TYPE` | 3 | `start.allowed[i]` |
| `own`/`shared` names a Type whose `in` excludes this Type | `START_NOT_IN` (new) | 3 | `own[i]`, `shared[i]` |
| a create reaction whose source Type the target's `in` excludes | `START_NOT_IN` (new) | 3 | `reactions[i]` |
| an intake on a Type with no `start` block, or whose `start.allowed` does not name it | `INTAKE_NOT_ALLOWED` (new; the intake's twin of `TIMER_NOT_ALLOWED`) | 2 | `intakes[i]` |

Problem codes are strings documented in `design.md`, not a proto enum; a
new one is still something tools match on, so each is listed here and
added to the table in `design.md` with the code.

### 1.8 What changes for existing Types

**Nothing.** No Type in `types/` has a `start` block, so every create is
judged as today. The only behaviour change in part 1 is 1.0's `create`
power, which no existing test, script or dev-token grant relies on.

### 1.9 What a caller may start, and where (specified, not built)

A UI needs to know whether to show "New incident" in this desk, and an
agent needs the menu it may suggest from. This is the first half of
roadmap theme 1's "**work that may be added** to a case at each point (so
an agent suggests only from that list)": `start` says what may be added
to a container and by whom; the other half (what may be added *at this
point* of the case, by its state) is a later Type feature that would read
the same block.

Proposed shape, for later: `ListStartable {parent: <container or
namespace>}` returns the Types the caller may start there, each with its
version and the reason it is open (the matching entry), computed by the
same `policy.MayStart`, after `policy.Project` on the container. It is a
new operation in `kernel.v1` (a contract change: asked separately).

### Membership test for part 1

- **The `create` power**: already a kernel concept; every domain has
  agents. Passes.
- **The `start` block**: all three reference apps need it. Workflow: only
  employees file vacation requests, only at the top level. Case
  management: incidents only in a desk; a fulfilment task only inside an
  access request. Issue tracking: a story only inside a space or an epic.
  It must behave the same everywhere: who may create is a permission, and
  permissions are uniform (`policy`). Passes. Nothing in it is named
  desk, case or alert.

## Part 2: triggers and start events

### 2.1 The words

- **Connector**: an outside service that speaks `pli.hooks/v1` (as built,
  outbound). Inbound, it is the one that talks to the vendor (a SIEM,
  Stripe, a mail server), checks the vendor's own signature, and forwards
  a normalised event to the kernel.
- **Source**: a kind of event a connector emits, with a typed shape. A
  contract in `contracts/`, like an action's.
- **Intake**: a declaration on a Type: "events of this source do this to
  Items of this Type". It has a stable ID.
- **Inbox**: the table where each accepted delivery is kept once, before
  any Item is written.
- **Verdict**: what an intake does with a matched event: `create`,
  `correlate`, `create_or_correlate` or `signal`.

### 2.2 Source contracts

One JSON file per source, `contracts/<connector>.<source>@<major>.json`,
frozen in `contracts/contracts.lock` and checked by `contractsfrozen`, as
V1's actions are. The format reuses the operation's, with `kind: source`:

```json
{
  "connector": "siem", "operation": "alert", "major": 1, "kinds": ["source"],
  "description": "An alert raised by the SIEM.",
  "risk": "low", "sideEffects": "none", "async": "forbidden",
  "eventTypes": ["alert.raised", "alert.updated", "alert.closed"],
  "dedupe": "alertId", "correlation": "alertGroup",
  "outputs": ["alertId", "alertGroup", "severity", "hostId", "title", "detail"],
  "outputSchema": { "type": "object", "properties": {
    "alertId":    { "type": "string", "x-pli-trust": "trusted" },
    "alertGroup": { "type": "string", "x-pli-trust": "trusted" },
    "severity":   { "type": "string", "enum": ["low", "medium", "high", "critical"] },
    "hostId":     { "type": "string" },
    "title":      { "type": "string" },
    "detail":     { "type": "string" } } }
}
```

- `eventTypes`: a closed enum. The event type is the one thing an intake
  may branch on (2.7).
- `dedupe` and `correlation` name outputs that must be `trusted`.
- Every other output defaults to untrusted (`x-pli-trust` absent means
  `any`, as in V1). A source contract cannot mark free text trusted: a
  load rule refuses `x-pli-trust: trusted` on an output without an `enum`
  or a declared ID `format`, other than the dedupe and correlation keys.
- `x-pli-personal: true` marks an output that may hold personal data (an
  email address, a name, free text). It is part of the machine contract,
  so it is in the digest and frozen in the lock; erasure reads it (2.5).
  A dedupe or correlation key may not be personal (refused at start), since
  it is stored hashed and used for matching.
- `ListActions` serves sources too (they are contracts), with
  `kinds: [SOURCE]`: an `ActionKind`/`HookKind` enum value, so a contract
  change, asked with the build.

### 2.3 Intakes on the Type

```yaml
# incident@4 (sketch)
intakes:
  - id: siem
    source: siem.alert@1
    on:                                     # event type -> one of this intake's verdicts
      alert.raised:  create_or_correlate
      alert.updated: correlate
      alert.closed:  { correlate: { transition: auto_resolve } }
    fields:                                 # source output -> field; values only
      f_alert_id: alertId
      f_severity_reported: severity
      f_host: hostId
      f_title: title
      f_detail: detail
    when_closed: create                     # create (default) | correlate | park
    unmatched: park                         # park (default) | reject | drop
```

- **The endpoint is per intake ID**, never per Type version:
  `POST /intake/v1/namespaces/{ns}/types/{type}/intakes/{id}`. A `create`
  uses the latest version (with 1.3's rule); a `correlate` applies the
  intake as declared in **the matched Item's pinned version**, so several
  versions live on one URL without draining (the connectors report on
  Camunda's drain trap).
- **`on`** maps each event type the intake takes to one verdict. An event
  type not listed is unmatched. **An event type selects only among the
  verdicts its intake declares**: a `correlate` may name a transition, and
  that transition is fixed in the Type, never taken from the event.
- **`fields`** maps outputs to fields, by name. The types are checked at
  load, as V1 checks a hook's writes (`WRONG_FIELD_TYPE`). Fields an
  intake writes are **intake-written**, which part 2.7 uses.
- **Deployment configuration**, not the Type, says where a `create` lands:
  the intake's container Item, per namespace, in the connectors file:

  ```yaml
  intakes:
    incident/siem: {container: namespaces/acme/items/0190…, secret_env: SIEM_INTAKE_SECRET}
  ```

  The container must satisfy the Type's `start.in`; checked at start and
  on every create (a container moved since is a refusal, recorded on the
  inbox row).

### 2.4 The inbound protocol

The kernel accepts **only `pli.hooks/v1`**, sent by a connector, never a
vendor's own webhook. Vendor code (Stripe's signature, a SIEM's API, MIME)
stays in the connector, out of the kernel.

- **Envelope**, a new kind `event`, added within v1 (a minor addition:
  both sides already ignore what they do not know):

  | Field | What |
  | --- | --- |
  | `protocol` | `pli.hooks/v1` |
  | `kind` | `event` |
  | `source` | `siem.alert@1` |
  | `eventType` | one of the contract's `eventTypes` |
  | `eventId` | the connector's ID for this event, stable across its retries |
  | `occurredAt` | when it happened at the vendor (a claim, for display) |
  | `data` | the contract's outputs, by name; nothing else |
  | `connectorVersion` | the implementation that sent it |

- **Signing**: Standard Webhooks v1, as built for the outbound direction:
  `webhook-id` (the connector's `eventId`), `webhook-timestamp`,
  `webhook-signature`, five-minute tolerance, constant-time compare, any
  trusted secret during rotation. One secret per intake binding, from the
  environment variable the connectors file names.
- **Checks before the inbox**: the signature; the intake exists and is
  enabled; `source` is the intake's, of the major its Type names; the
  event type is in the contract; `data` fits the contract's schema
  (types, enums, no extra names). Any failure is a problem body
  (RFC 9457) and nothing is stored: 401 for a signature, 404 for an
  unknown intake, 400 for the rest.
- **Answers**: 202 once the event is committed to the inbox (fast, so the
  connector's retries stay rare); 200 with the stored outcome for a
  duplicate; 422 for a `reject` verdict. Processing failures after 202
  are the kernel's to handle (2.5), never the connector's retry.
- **Conformance**: `internal/hookproto/conformance` gains the inbound side,
  run against the kernel's endpoint: signatures (unsigned, tampered,
  stale, unknown scheme), duplicates, schema refusals.

### 2.5 The inbox: acknowledgement, deduplication, erasure

**A new table** (needs a second OK before it is built):

```sql
inbox (id uuid primary key, namespace_id, intake text,          -- 'incident/siem'
       dedupe_key bytea,                                         -- sha-256, never the text
       event_type text, data jsonb, received_at timestamptz,
       status text,      -- pending | applied | parked | rejected | dropped | failed
       item_id uuid, commit_seq bigint, reason text,
       unique (namespace_id, intake, dedupe_key))
```

- **Deduplication**: the dedupe key is the contract's `dedupe` output,
  else `webhook-id`. Its SHA-256 is stored, not the text. A second
  delivery hits the unique constraint and answers from the first row.
- **Applying**: the worker reads pending rows and writes the Item through
  a new internal door (2.6). The Item row, its event, its outbox row and
  the inbox row's `status = applied` commit **in one transaction**. The
  write's idempotency key is `intake:<intake>:<hex of dedupe hash>`: no
  personal data, stable across worker retries.
- **What is kept**: only the mapped, typed `data`, never the raw body (the
  connector keeps that, under its own rules).
- **Erasure**: the inbox is held to the **erasure rule**, not only to a
  retention period. `data` can hold personal data (an email address in an
  alert, a user's name). The source contract says which outputs may: each
  carries `x-pli-personal: true` or not, frozen in the lock (2.2).
  Erasing a person reaches the inbox: on every row whose Item is erased,
  and on every row whose personal outputs name the person, those outputs
  are cleared in the same erasure, keeping the row's shape (intake,
  status, dedupe hash, Item ID) for audit. A retention period applies on top
  (`applied` rows lose `data` after N days; parked rows keep it until
  resolved or expired). `item_events.payload` stays as it is: opaque IDs,
  field IDs, never values.
- **Parked and failed rows** are visible (a list for `ops`) and replayable;
  replay re-runs the verdict under the same idempotency key.

### 2.6 Which principal a trigger acts as

- **`intake:<type>:<intake-id>`**, a new kind of kernel principal, like
  `hook:` and `reaction:` (a new principal kind: second OK before it is
  built). Typed, so it cannot be claimed on the public door
  (`checkPublic` refuses it, as it refuses `hook:` today).
- **It holds no powers beyond its intake's verdicts**: it creates Items of
  its own Type, in its configured container, with its mapped fields; it
  correlates to Items of its own Type and applies the transition its
  verdict names, or writes its mapped fields. It never narrows, moves,
  links, changes policy or writes a field outside its mapping.
- **Part 1's rule applies to it, and it fails closed.** An intake on a
  Type with **no `start` block is refused at load**, as is one whose
  `start.allowed` does not name `principal:intake:<type>:<id>` (both
  `INTAKE_NOT_ALLOWED`). So every Type an outside system can create says
  so explicitly. `in` must admit its container.
  A `correlate` verdict's transition must list it in `allowed`, as a timer
  must list `principal:system:timer` (else refused at load).
- **No `on_behalf_of`** until external identities exist. The remote actor
  (an alert's reporter) is data, untrusted, and never a principal. When a
  remote actor is later mapped to a user (`external_identity`), part 1
  judges **both** the intake and that user, and both must pass, as for an
  agent and its user.
- **A kill switch per intake, required before inbound is built.** The
  settled direction's flag table (per Type version, hook or connection)
  gains the intake. Paused: the endpoint keeps accepting into the inbox
  (so nothing the vendor sends is lost), and the worker applies nothing
  until it is resumed.
- **A new internal door**: `Internal().Create`, accepting only `intake:`
  and (for timer creates) the reaction runner. Called only by the worker;
  the source test that holds `Internal()` to its callers covers it.
- **Attribution**: `originating_hook` = `intake:<type>/<id>@<source>`,
  `causation_id` = the inbox row, a new correlation root.

### 2.7 Inbound text is untrusted (Ventoux item 4)

The line, agreed in Phase A:

- **Trusted**: that a signed source sent an event of a declared type, and
  the outputs the contract marks trusted (the dedupe and correlation
  keys, enums, IDs). The event type may select one of its intake's
  declared verdicts. That is how "payment received" moves the invoice:
  the Type says what `payment.received` does; the event only says it
  happened.
- **Untrusted**: every other value. It may fill fields. It never decides
  a move, picks a recipient, or chooses the Item a write lands on.

Until Ventoux item 4 gives each value its provenance, part 2 needs no
per-value tracking for intakes, because the writer is known statically:

- **Intake-written fields are untrusted.** A field an intake's `fields`
  maps into is refused at load (`READ_NOT_ALLOWED`) wherever a value
  decides: a guard, a `field:` entry in `allowed` or `start.allowed`, a
  reaction's `when`, a `sets` expression, an at-timer, a `trusted` action
  input (V1's `x-pli-trust`, which item 4 then enforces at run time). It
  may be shown, searched and read by an enrich hook (`any` inputs).
- **The correlation key must be trusted** in the source contract; the
  correlation targets only Items of the intake's own Type, in the
  namespace; and a correlated write is still judged by lifecycle and
  `allowed`, as the intake principal. A forged key from a compromised
  connector can at worst move an Item of that Type the way that event type
  already may, which is the connector's blast radius and nothing more.
- **Severity is the classic trap.** The SIEM's `severity` is the
  attacker's to choose. It lands in `f_severity_reported`, untrusted; the
  case's own `f_priority`, which picks SLA targets, is set by a person or
  an enrich hook (`trusted` sources), never mapped from the alert. The
  roadmap's slice-1 attack ("set P1 and close this") fails the same way.

### 2.8 Inbound create (BPMN: message start event)

`create`: the intake creates an Item of its Type in its configured
container, with its mapped fields, as `intake:<type>:<id>`, through
`Internal().Create`, checked by part 1, in the inbox row's transaction.
It records an `external_keys` row (2.9) when the source has a correlation
key, so later events find the Item.

### 2.9 Correlation (BPMN: intermediate message catch event)

**A new table** (second OK before it is built):

```sql
external_keys (namespace_id, intake text, key bytea,    -- sha-256 of the correlation key
               item_id uuid references items, open boolean,
               primary key (namespace_id, intake, key, item_id))
create unique index external_keys_one_open on external_keys (namespace_id, intake, key) where open;
```

- `correlate`: find the open Item for (intake, key); apply the verdict's
  transition (fixed in the Type) or write the mapped fields, as the intake
  principal, judged by lifecycle and `allowed`. A refused move (a guard, a
  wrong state) marks the inbox row `failed` with the refusal's reason; it
  is not retried.
- **`create_or_correlate`, and races.** Two events with the same key
  arrive together. Each worker tries the create; the insert into
  `external_keys` with `open = true` runs **in the create's own
  transaction**, and the partial unique index decides: one commits, the
  other's transaction fails on the constraint, rolls back (no Item, no
  event), and is retried **as a correlate**, which now finds the winner.
  The loser's inbox row records `correlated`, never a second case.
- **A matching Item that is closed** (its state's category is `done` or
  `canceled`): per intake, `when_closed`:
  - `create` (the default): open a new case. The old row's `open` goes
    false when the Item enters a closed category (in the transition's own
    transaction), so the new create wins the index;
  - `correlate`: write to the closed Item anyway (a late payment on a
    closed invoice); its transitions decide what is allowed;
  - `park`: keep the event for a person.
- **A closed Item reopened.** Its key went `open = false` when it closed,
  and a later event may have opened a new case for the same key since.
  By default **the reopened Item's key stays closed**: reopening does not
  touch `external_keys`, so events for the key keep going to the newer
  case, and the unique index is never hit. (With no newer case, the key
  still stays closed: the next event follows `when_closed`.) Tests, when
  built: close a case, let an event open a second one, reopen the first;
  the reopen succeeds, and the next event correlates to the second case.
- **A correlate that writes a frozen field** (the Item waits on an
  approval) is refused like any write: the inbox row is `failed`, with the
  refusal's reason. It is not parked or retried.
- **Unmatched** events (no open Item for the key, on a `correlate`, or an
  event type the intake does not take): per intake, `unmatched`: `park`
  (the default: kept in the inbox, replayed when a later `create` writes
  the key, which fixes the "link not written yet" race), `reject` (422 to
  the connector) or `drop` (acknowledged and counted).

### 2.10 Timer starts (BPMN: timer start event)

Created on a schedule, **by a container**: a `kind: create` reaction with
`on: timer`, run by one of the container state's acting timers.

```yaml
# on the container's Type, in a state
acting: [{id: weekly, after: 168h, every: 168h, max: 52, reaction: make_review}]
reactions:
  - {id: make_review, kind: create, on: timer, state: active, type: review, version: 1,
     fields: {f_week: "cause.at"}}
```

- Reuses acting timers (their keys, their catch-up after a crash: "one at
  once, then the schedule", never a burst) and create reactions (consent
  by `creates:`, writer `reaction:<type>:<id>`, no human creator).
- Part 1: consent is enough; the target's `in` must admit the container
  Type (1.5).
- Leaving the state stops the schedule, as acting timers do.
- **Kernel change**: `on: timer` for a create reaction (today only
  `on: enter`), and an acting timer that runs a reaction instead of a
  hook. Small, inside existing concepts.
- **Top-level schedules** ("every Monday, a new report, in no container")
  are Later: they need a schedule owned by no Item.

### 2.11 Signals (BPMN: signal event): design only

One event reaching many Items: "this host is compromised" reaches every
open case about it; "the vendor is down" reaches every open ticket waiting
on it.

- **Declared** as an intake verdict `signal`, with its targets: the open
  Items of the intake's Type sharing the correlation key (several open
  rows: a separate, non-unique key table for signals), or the open Items
  inside the intake's container.
- **A declared bound** (`max: 500`): above it, the signal is parked, never
  applied in part.
- **One write and one key per Item**: `intake:<intake>:<dedupe>:<item>`,
  each its own transaction, so a crash half way resumes without doubling.
  The inbox row is `applied` once every target is.
- **Each write is judged** like a correlate: lifecycle, `allowed`, as the
  intake principal.
- Not built: no domain needs it yet; security operations will (a host
  isolated closes its alerts).

### 2.12 Conditional starts (BPMN: conditional start event)

"Start this work when the case changes so": the existing create reaction
with `when` (CEL over the source's fields) already is one, and it is
roadmap theme 1's "conditions that switch work on". No new feature. A
`when` reading an intake-written field is refused (2.7).

### 2.13 Email

A source like any other, behind a connector that turns a message into an
`event` (`mail.received@1`: sender, subject, body, all untrusted; a
message ID, trusted, as the dedupe key). Out of scope for the spike.

### Membership test for part 2

| Change | Needed by all three apps? | Must behave the same everywhere? | Verdict |
| --- | --- | --- | --- |
| Source contracts | Workflow: an HR system starts onboarding. Cases: alerts, emails. Issues: a CI failure opens a bug | Yes: checked and frozen like actions | Passes |
| Intakes on the Type, `create`/`correlate` | The same three | Yes: one inbound door, one dedupe, one principal | Passes |
| The inbox | The same three | Yes: audited, erased uniformly | Passes |
| `intake:` principal, `Internal().Create` | The same three | Yes: permissioned by part 1 and `allowed` | Passes |
| `external_keys` | Workflow: "payment received" moves an invoice. Cases: an alert update finds its case. Issues: a CI rerun finds its bug | Yes | Passes |
| Create reaction `on: timer` | Workflow: a monthly review. Cases: a weekly check on an open case. Issues: a sprint's retro | Yes | Passes |
| `signal` verdict | Not yet shown for all three | | Not built |

## How BPMN, Bonita and Camunda do it

**On these sources.** This session's network allowed no vendor docs
(docs.camunda.io, docs.camunda.org, documentation.bonitasoft.com, omg.org),
so the Bonita and Camunda points below that are not already in the
earlier reports are **UNVERIFIED**: they come from search summaries, not
from reading the pages. The Camunda inbound points (correlation key,
activation condition, message ID, the drain trap) were verified in
`docs/research/connectors-for-the-item-kernel.md`. Each UNVERIFIED point is
checked against the vendor docs before it is relied on (decided at
review).

| BPMN | Ori | Bonita (UNVERIFIED) | Camunda 7 (UNVERIFIED) | Camunda 8 |
| --- | --- | --- | --- | --- |
| Who may start (not BPMN) | `start.allowed` + `in` | the actor set as **initiator**, mapped to users, groups or roles; forum reports say the REST instantiation call did not always enforce the mapping | `CREATE_INSTANCE` on the process definition plus `CREATE` on process instances; authorization is off by default; `candidateStarterUsers/Groups` only filter "startable" queries | `CREATE_PROCESS_INSTANCE` on `PROCESS_DEFINITION` (in the 8.8 docs; UNVERIFIED which version added it) |
| None start | `CreateItem` | REST `POST /API/bpm/process/{id}/instantiation`, as the logged-in user | `startProcessInstanceByKey` | create process instance |
| Message start | intake `create` | message start event, correlation keys | message start event | message start event; inbound webhook connector with a message ID for dedupe (verified, connectors report); with a correlation key, a new instance only if none is active for the key (UNVERIFIED) |
| Intermediate message catch | intake `correlate` | catch message event, correlation keys | `correlateMessage`, business key | correlation key, activation condition (verified, connectors report) |
| Message start or catch for one key | `create_or_correlate`, decided by a unique index | | | message start's per-key uniqueness (UNVERIFIED) |
| Timer start | create reaction `on: timer` from a container's acting timer | timer start event (cycle, date, delay) | `timeDate`, `timeCycle` | `timeDate`, `timeCycle`; a new version cancels the old one's timers (UNVERIFIED) |
| Signal start/catch | intake `signal` (design only) | signal events | broadcast to every subscription | broadcast signal (from 8.4, UNVERIFIED) |
| Conditional start | create reaction with `when` | | condition evaluation API | conditional start events in current docs, version UNVERIFIED |

Where Ori differs on purpose:

- **Who may start is declared on the Type and enforced on every door**,
  REST included. Two of the three products make it optional or
  query-only, by the reports above.
- **The endpoint belongs to the intake, not the version**, so there is no
  drain trap (connectors report).
- **No expression over the payload decides anything.** Camunda's
  activation condition and correlation key are FEEL over the request; in
  Ori the event type picks a declared verdict and only trusted, declared
  outputs are keys.
- **A start never runs as a person it did not authenticate.** Bonita's
  REST start runs as the session user; an intake runs as its own
  principal, permissioned by the Type.

## How this serves security operations (theme 4)

The SOC domain, in these pieces:

- **Who opens a case.** `case@1` (sketch): `start: {in: [soc_queue],
  allowed: [role:soc_analyst, principal:intake:case:siem]}`. Analysts and
  the SIEM open cases; nobody else, nowhere else. An agent triaging for an
  analyst needs `create` and the analyst's role.
- **Work inside the case** (enrich, contain, ask the user): `start: {in:
  [case], allowed: [role:soc_analyst, container_creator]}` on each piece
  of work, which is part 1's half of "the work that may be added" (1.9).
  An agent suggests only from what `ListStartable` would return.
- **Alerts become cases.** The SIEM connector sends `siem.alert@1`.
  `create_or_correlate` on `alertGroup` groups a burst of alerts into one
  case; the unique index makes it race-free; `when_closed: create` opens a
  new case for a recurrence.
- **The attacker writes the alert.** Its title, detail, host and severity
  are untrusted: shown to the analyst, never deciding a move, a priority,
  a containment target or a recipient (2.7). An injected "close this
  case" in the alert text changes nothing: `alert.closed` is an event
  type the Type maps, and its text is never read for a decision.
- **Containment is an action** (`edr.isolate_host@1`, risk `high`) behind
  a person's approval; its `hostId` input is `trusted`, so it can never be
  mapped from the alert's `hostId` field: an analyst confirms the host.
- **Signals later**: a host isolated reaches every open case about it.

## Kernel changes, by build step

| Step | Kernel change | Schema | API (`kernel.v1`) | Needs a second OK |
| --- | --- | --- | --- | --- |
| 1 | `create` power on `Create` | none | none | no (answer 1) |
| 2 (**built** 2026-11-24) | `start` block; `policy.MayStart`; load checks; latest version governs; incident@4 with `in: [desk]` (option B) | none | none (reasons in the item service's message only; the API returns `NOT_ALLOWED`) | no |
| 2b (**built** 2026-11-24) | the same "latest version governs" rule for placement (`placement_writable_by`): a move, a narrow, an un-narrow and a narrowed create of any version; a kernel rule, no opt-in on the Type (`design.md`, "Starting work, step 2b, as built") | none | none | no (review answer 2) |
| 3 | first the intake kill switch (required before anything inbound); then source contracts (with `x-pli-personal`); intakes; the inbox; `intake:` principal; `Internal().Create`; the inbound endpoint and its conformance; the vendor precedents verified | `inbox` | a `SOURCE` kind in the actions' enum; the inbound endpoint is HTTP, outside `kernel.v1` | **yes**: a table, a door, a principal kind |
| 4 | correlation: `external_keys`, `create_or_correlate`, `when_closed`, `unmatched` | `external_keys` | none | **yes**: a table |
| 5 | create reaction `on: timer` | none | none | no |
| 6 | signals | a non-unique key table | none | **yes** |
| Later | `ListStartable` (1.9); `container_requester`; `except` on `start`; top-level schedules; email | | `ListStartable` | yes |

## What to build first

**Steps 1 and 2, as two pull requests.** Step 1 is one check and its
tests. Step 2 is the `start` block in `typedef` (parse, load checks),
`policy.MayStart` (pure, unit-tested against `MayTransition`'s cases), the
check in `Create` and its tests, and one Type that uses it: a new
`incident` version with `start: {in: [desk], allowed: [role:desk_agent]}`
(customers keep `own`), so the desk proves it end to end. Neither needs a
migration or a contract change.

## Open questions, decided at review (2026-11-23)

1. **Reason metadata**: no contract change; `START_NOT_IN` and
   `START_NOT_ALLOWED` stay in the item service's message; the API
   returns only `NOT_ALLOWED` (1.6, corrected at step 1).
2. **Latest version governs, for placement too**: yes, closing the same
   hole for `placement_writable_by`, as its own small step (2b in the
   table), not in step 2. **Built** (2026-11-24). It does not stop a
   create from naming an older version's lifecycle: that is its own step
   (`docs/roadmap.md`).
3. **An intake on a Type with no `start` block**: refused at load
   (`INTAKE_NOT_ALLOWED`), fail closed (2.6, 1.7).
4. **Erasure in the inbox**: an `x-pli-personal` flag on source outputs,
   frozen in the lock (2.2, 2.5).
5. **A correlate that writes a frozen field**: `failed`, with the
   refusal's reason (2.9).
6. **A kill switch per intake**: yes, and required before inbound is
   built: the first part of step 3 (2.6).
7. **A mapped external identity**: part 1 judges the intake and the user;
   both must pass (2.6).
8. **Verification**: the Bonita and Camunda points marked UNVERIFIED are
   checked against the vendor docs before any is relied on, at the latest
   as part of step 3.

Also decided at review: **a reopened Item's key stays closed** (2.9).

Each build step comes back for its own OK.

## Later (not proposed for building now)

- **Say why over the API.** Today every refusal of this kind is
  `NOT_ALLOWED` with one fixed message: a read-only session, a session
  without `create`, and later `START_NOT_IN` and `START_NOT_ALLOWED` all
  read the same. Agents need the reason to act sensibly (ask for a
  session with `create`, pick another container, hand back to a person).
  A contract change (a metadata key on `NOT_ALLOWED`, or new reasons):
  asked separately.

- `except` on `start` (answer 4); `except: [container_creator]` is the
  first use.
- `container_requester` (1.2).
- `ListStartable` (1.9), and its state-dependent half (theme 1).
- Top-level schedules (2.10).
- Signals (2.11).
- Email as a source (2.13).
- Polling sources (a connector that polls and emits events, with a cursor
  committed with the inbox row: the connectors report's Airbyte rule).
