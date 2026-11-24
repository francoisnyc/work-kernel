# Spike journal

One entry per session, newest at the bottom.

## Later

Ideas noted during the spike, not built yet.

- **Left open by bake-off hole 1** (design.md, "An effect re-checks what caused it, as built"; each
  also in docs/roadmap.md, "Holes found by the bake-off"; all need a Temporal anyone can reach):
  - **An early reminder.** A scheduled acting timer's run leaves no event until its hook finishes, so
    a forged start can send it early, once per run. Closing it in the item service means the item
    service knows when a run is due; the dev clock's scale lives only in the worker today.
  - **A timer at an instant with a hook and no `sets`** writes nothing when it acts: no proof to ask
    for. `ActAt` could leave a record for it.
  - **State timers trust the workflow's clock.** A forged start can make a duration timer's move
    early (expire a request now). Timers at an instant are already judged by the item service's clock.
  - **A forged `hook_failed` naming the true entry** makes a real action count as done. Only the
    workflow knows that a budget ran out, so the item service cannot tell it from a real failure.
  - **Enrich hooks' causes are not confirmed**, and no activity checks that the hook it is given is
    of its own kind (`RunEnrich` runs a check hook's action and writes its fields). No outside effect:
    the values are the declared action's own answer for the Item's real fields.
  - **Authenticating Temporal** (sign-on; who may start, signal or poll which task queue) closes all
    five at the root. Not designed.
  - **A hook that already completed for a cause is called again on a replayed request.** The
    connector's own idempotency then decides; the kernel could answer from its own record instead.
    Not done: `TestActivities_RunAction_SecondDifferentResult_IsAKeyConflict` pins that a second run
    reaches the connector.
  - **The web UI and the PoC apps show `cause_not_confirmed` as its raw code**, like every reason.

- **Found by the Temporal bake-off** (docs/research/temporal-bakeoff.md; nothing built):
  - **Fields are open to the agent by default.** Both bake-offs' Types left
    what is asked, and the decision's reason, writable by anyone who may
    edit fields, the agent included. `writable_by: ['role:staff']` (a role
    every person holds) closed both holes for the agent, tested. Consider a
    default, or a `writable_by: [people]`, so an author need not know it.
    "Only the requester edits a draft" still has no way to be said.
  - **Readings did not catch a request nobody can decide** (an owner asking
    for their own system, `except: [creator]` on the only decider). A check
    that proves every waiting state has someone who may move it.

- **Found by the Camunda bake-off** (docs/research/camunda-bakeoff.md, "Gaps found"; nothing built):
  - **A creator can pick an old Type version.** After `purchase@2` (the CFO
    step) was live, `item create --type purchase@1` still worked and the
    CFO was skipped. Starting work, steps 1 and 2b ("the latest version's
    rules govern creates") would close it; it matters more than its place
    in that plan suggests, because the failure is an approval.
  - **The worker acts on workflow input without reading the Item.** Closed
    for action hooks on 2026-11-24 (the entry "Bake-off hole 1" below):
    the worker confirms the cause before any side effect. What a
    production Temporal must enforce is still to decide; the list is in
    the item below.
  - **No "only the requester" or "only the decider" rule for a field.**
    Anyone who sees a draft edits it before its owner submits (an agent
    with `edit_fields` changed the amount; submit then froze the agent's
    values). A decision's reason can be planted before the decider acts,
    which defeats `required` (the Requests area already lists "a decision
    field only the approver may write"). `writable_by: [creator]` and
    `writable_by: ['field:f_x']` would cover both.
  - **Queues.** No "what waits on me" search, no way to ask the caller's
    roles, no claim tied to the caller (`sets` has no `caller`): the
    bake-off's inbox rebuilt it from `GetType` with one search per move.
  - **Stories for Types outside `types/`.** The story runner reads only
    `../../types` and `../../stories`; a spike's Types cannot use it.
  - **A Type goes live only at start; a request in flight cannot move to
    a new version.** Camunda migrated one with one call.
- **Starting work, step 2's Later items** (docs/plans/starting-work.md, 1.3):
  - The "governs earlier versions" and "loosened" notes, with the WARNING
    severity (Ventoux item 3): `pli type validate` should say which earlier
    versions a draft's start block will govern, and that a draft dropping
    the block in force loosens them. It needs a severity other than ERROR
    in `kernel.v1` (a contract change), so step 2 builds neither.
  - `ListStartable` (plan 1.9), and `start` in `kernel.v1.Type`, so that
    `GetType`/`ListTypes` show the block and a UI or an agent knows what it
    may offer. Both are contract changes.
  - The web UI's "New" form offers the latest version of every Type to
    anyone, so staff can pick a Type and a place the start block then
    refuses (`NOT_ALLOWED`). It needs the item above.
  - apps/poc knows incident versions [2, 3] only (`INCIDENT_VERSIONS`): an
    incident@4, which the web UI's customer form now creates, is not in its
    queue.
  - Roles are not locked during a create's start check, as for
    transitions: a role revoked while a create commits does not stop it.
  - `RegisterType` runs no checks of the Types together, the start block's
    included: plan 1.5's refusal at registration holds for
    `ValidateTypes`, `pli type validate` and the Types folder at start
    only. `Create` refuses at run time anyway.
  - Who may open an incident is a product choice left open: incident@4 says
    only where (`in: [desk]`); a `role:employee` beside `role:desk_agent`,
    say, once a domain asks (owner's decision 1, option B).
  - A story that walks incident@4's rule (who opens an incident, where):
    the readings' `start` witness covers the kernel's side; a story was
    not asked for in this step.
  - **A -race timing margin.** `TestMinSeq_DoesNotWaitForYoungerTransactions`
    and `TestSearch_MinSeqAlreadyReached_AnswersAtOnce` allow 1 s for a
    Search with `min_seq`; under `-race` they take 0.70 to 0.89 s, on main
    too. With the workflow package running beside them (`-p 2`) they took
    1.24 to 1.31 s twice in this step's runs, and passed in 3 runs on main
    and 5 further runs on the branch; a poller of open transactions found
    only tests that hold locks on purpose. A test change for later (a
    bound relative to an unloaded Search, or no bound under -race).
    **Fixed 2026-11-24** (found in step 2b's Phase C, where both failed on
    main too): the second was not the wait. Each test makes its own
    Service, and a Service's first Search loads every registered Type
    version (`ListTypes`), 441 of them by the end of the package's run:
    0.85 to 1.03 s under `-race`, growing with every step that registers
    test Types. Each test now makes one Search before the clock starts;
    the timed Search then takes 3 to 5 ms, and the 1 s bound and the
    assertions are unchanged (the owner agreed). Still open, for the day
    Search is measured: resolving a filter loads every Type; a long-lived
    Service pays it once.

- **Starting work, step 1's Later items** (docs/plans/starting-work.md, 1.0):
  - **Reaction creates by an agent without `create`.** An agent session
    holding `transition:*` (or any transition power) but not `create` may
    not create, yet its transition into a state with an on-enter create
    reaction makes an Item: the target Type's `creates:` consent governs,
    and no reaction reads a session's powers. Pinned by
    `TestCreatePower_AReactionCreatesForAnAgentWithoutCreate`. Revisit if
    a Type author needs "only creators may cause creates"; it would mean
    reactions reading the causing session, a new concept.
  - **Say why over the API.** A read-only session, a session without
    `create`, and later `START_NOT_IN` / `START_NOT_ALLOWED` all answer
    `PERMISSION_DENIED NOT_ALLOWED` with one fixed message: `apiError`
    never sends the item service's text. Agents need the reason to act
    sensibly (ask for `create`, pick another container, hand back to a
    person). A contract change (a metadata key or new reasons), asked
    separately; also under Later in the plan.

- **The hook protocol's Later items** (design.md, "Hook protocol
  (pli.hooks/v1), as built"):
  - `actor` in the envelope (who caused the event: principal,
    `onBehalfOf`, `agentSession`), the day a connector needs it, with the
    `HookInput` change it takes, behind `policy.Project`. The name is
    reserved; v1 only grows, so adding it breaks nobody.
  - **Before real use:** connector configuration as operator data, in a
    table, audited, instead of a file the worker follows.
  - Record on the Item which connector version answered (the answer's
    `connectorVersion`), for audit.
  - Named connector reasons for action hooks: today an unconfigured,
    unauthorized or mismatched connector is recorded as `action_failed`,
    because the item service accepts four `hook_failed` reasons.
  - **Before open-sourcing:** move the ERP, CRM and email connectors out of
    the worker, as HR now is; the kernel then ships only generic ones.
  - A `pli hooks call` command (signed fixture envelopes to a local
    connector) and `pli hooks serve-mock` (the kernel's side, for callback
    tests); a delivery log in the kernel, per attempt, with manual replay;
    connector URLs through an SSRF-filtering egress proxy, and mTLS as an
    option; `Retry-After` honoured on a 429 (Temporal's next retry delay);
    `v2a` (ed25519) signing and signed answers.
  - Failure policy and timeouts per hook binding, in the Type (the report's
    table); namespaced connector names through a local alias (`hr` to
    `acme.hr-directory@1`), with the bundle format.
  (Hook protocol spike)

- **Action contracts' Later items** (Ventoux V1; design.md, "Ventoux V1,
  as built"):
  - `GetType` shows a Type's hooks (each with the action it pins): today
    `kernel.v1.Type` has none, so a tool cannot see which actions a
    registered Type uses.
  - Connector availability in `ListActions`: whether this deployment
    configured each action's connector (today only `connector_unconfigured`
    at run time says so).
  - `UNKNOWN_REASON` (a load-time problem) when a Type can first name a
    reason, with a `business_error` routed to a transition.
  - Nullability at load: prove a field set from the `required` of every
    transition into a hook's state, then refuse a nullable field into an
    input that takes no null.
  - A description per reason code, so a UI can say what a denial means
    (apps/poc translates them by hand today).
  - Draft contracts in `ValidateTypes`, so an authoring tool can propose an
    action that does not exist yet and see the Type checked against it.
  - Contracts per namespace, with bundles (today the catalogue is the
    kernel build's: a new remote connector still needs a release).
  (Ventoux V1)

- **Stories' Later items** (Ventoux V5; design.md, "Ventoux V5, as
  built"):
  - **contracts: principal kinds on pli-principal inputs.** A contract
    cannot say which kinds of principal an input takes (`email.notify`'s
    compiled-in code refuses customers, its contract does not say so), so
    a story's fake can accept a recipient production refuses.
  - Draft Types in a story (files beside `types/`), for slice 1: a story
    that tests a new incident version before it is registered.
  - `pli story run` against a server needs a simulation API; tests only
    until then.
  - A story's own reaction to a delivery the relay cannot make is a
    failure after 300 refused passes (about 3 s): enough for a gap, which
    never heals; revisit if a story needs a delivery that heals later.
  (Ventoux V5)

- **Readings' Later items** (Ventoux V6; design.md, "Ventoux V6, as
  built"):
  - **Found by readings:** incident@3 lets any staff member who sees an
    incident start it, wait for the customer, resolve it or cancel it (four
    moves with an empty `allowed`); only placement is kept for
    `desk_agent`. Decide whether those moves belong to desk agents.
  - **Found by readings:** vacation_request lets anyone who sees it narrow
    it (already above: "vacation_request and ticket have no placement
    rules"), so a requester can narrow a request away from its approver.
  - A kind of sentence for the `start` block when starting work, step 2,
    lands (who may create an Item of the Type), with its witness. The
    `create` power (#55) is a session's, not a Type's, so no reading
    changed for it.
  - A move kept for a hook (`principal:hook:...` in `allowed`): no hook
    makes a move today, so "Only the hook `h` may move it" is witnessed on
    its refusal side only.
  - The reading on the web UI's Type page; labels instead of field IDs once
    presentation hints exist.
  - Long `sets` lists read heavily (sla_goal's moves); patterns (item 8)
    would shorten them, and the semantic diff (item 7) can compare two
    readings' sentences by kind and path.

- Serve the Type format's JSON Schema through the API (V2c checks it in as
  a file only), so an authoring tool fetches the schema of the server it
  talks to. (Ventoux 2a, answer 17)
- Type problems: a reaction, and a consent across Types, still stop at
  their first problem (every other one is checked); a reaction cycle is
  reported one at a time. Finer when a tool needs it. (Ventoux 2a)
- ~~V2c (the JSON Schema): it must validate a Type file after its YAML merge
  keys (`<<: *anchor`) are expanded.~~ Done in V2c: the tests decode the
  YAML with yaml.v3 (merges expanded) before validating; the sla package's
  files validate.
- **Tighten typedef's stage (a), then the schema, in one step.** typedef
  reads any scalar where a string is meant (`id: 5` loads) and lets null
  through for lists and maps (`fields: ~` loads), so the schema, which may
  never be stricter, does too. Refuse a non-string scalar where a string is
  meant and null for a list or a map in stage (a) (`WRONG_SHAPE`), then
  tighten `schema/type.schema.json` to match; its tests then prove the two
  agree. (Ventoux V2c, Phase B review)
- `RegisterType`'s own refusal at start (a registered version with another
  definition) as `VERSION_CHANGED` too; today it is a plain error. (Ventoux
  V2b, Phase A)
- `ValidateTypes` could return the drafts as parsed Types (as `GetType`
  shows them), for an authoring tool's preview. (Ventoux V2b, Phase A)
- A `from: [list]` form for transitions, so "the owner may drop it from
  anywhere" is one line, not six (build_step@1 has six X->dropped lines; a
  `from:` list is `WRONG_SHAPE` today). Next to Ventoux item 8 (patterns
  that expand into rules, `docs/plans/ideas-from-ventoux.md`); decide with
  the next Type-format change. (build_step@1)
- epic@2: count build_step in an epic's progress (`incoming: issues` lists
  only story, bug and task today) and in its close guard, so an epic of
  steps is not done while a step is neither done nor dropped. (build_step@1)
- Action hooks on build_step through a pli.hooks/v1 connector: start a
  builder on entering `questions`, a reviewer on entering
  `tests_proposed`. (build_step@1)
- A `sprint` link on a future build_step@2, so a step can join a sprint
  like story, bug and task (build_step@1 has only `epic` and `blocks`; the
  loop picks by `f_rank` and what nothing blocks). (build_step@1)
- **High, before real use: move Items to a new Type version.** The kernel
  has no such operation, so every Item created before a hardening keeps its
  version's rules: an incident@2 is still narrowed by its requester, its
  sla_goal@1 goals still completed by hand, a reply@1 still moved. A kernel
  change. (Hardening after Jira 2.4)
- A `plat_admin` role to place PLAT's work Items (story, bug, task, epic,
  sprint, space), which anyone who sees them may move or narrow today; with
  the open question of a narrow at creation (sam's private task) against
  moves and narrowing afterwards: placement_writable_by refuses both today.
  (Hardening after Jira 2.4)
- vacation_request and ticket have no placement rules either: a requester
  could narrow a request away from its approver. (Hardening after Jira 2.4)
- ~~**High: any principal who sees an Item may move it or narrow it**~~, and
  ~~**a goal under sla_goal@1 can be completed by hand**~~: done in the
  hardening step (incident@3, reply@2, note@2, comment@3, sla_goal@3;
  design.md, "Hardening after Jira 2.4, as built"), for Items of the new
  versions.
- A reaction leaves the workflow's queue with only a log line when
  `ActivityAfterReactionFailed` itself fails (or the Item is gone): no
  record on the Item, the reaction vanishes. Keep it queued and retry the
  record with backoff; a test makes the activity fail and wants the
  reaction recorded once recording works again. (Walkthrough of the reply
  path, 2026-11-21)
- Groups as readers, or a named approver who joins the readers: today the
  whole namespace sees who asked for what access (PR 2's requests are not
  narrowed). (Jira 2.4b, Q8a)
- `excludes:` on a principal field (Jira 2.4b's #8, not built): an
  approval handed over to the requester cannot be decided until
  `access_admin` reassigns it (design.md, the weakness). (Jira 2.4b)

- **API candidates from the PoC apps** (design.md, "PoC apps as built:
  what the API lacked", says what each cost the apps): search within a
  container; links in search and list results; `update_time` on an Item,
  with a sort by it; totals (or at least "more than N") on lists; a
  has-no-link filter ("issues in no sprint"); a who-am-I call with roles and
  display names. (PoC apps)
- A description field on the incident, in a new Type version: the portal
  writes a request's description as its first reply (two writes, and two
  `reaction_skipped` records staff see). (PoC apps)
- Issue keys from the kernel: the PoC apps show the space's key and the last
  8 characters of the Item's ID as a stand-in. (PoC apps)
- **Needed for Bonita: hooks in `GetType`** (id, trigger, kind, action,
  state). A process map shows a check unnamed and no emails at all: an
  action on entering a state is invisible until it has run. (PoC apps,
  Requests: design.md, its gap 1)
- **Needed for Bonita: a balance check at submit, and a reservation for
  pending days.** The vacation request checks the balance only when an
  approval deducts it: the form can only warn, and two requests that do not
  both fit can both wait for approval. (PoC apps, Requests: gap 2)
- **Needed for Bonita: readers derived from a field** (an approver joins the
  readers of the request and of the requester's balance), with the 2.4b
  Later item on groups as readers. Today an approver sees a balance only if
  HR adds them, and every staff member reads every request. (PoC apps,
  Requests: gaps 6 and 7)
- **Needed for Bonita: a decision field only the approver may write.** The
  app writes the approver's comment as a `comment@1` inside the request, a
  second write after the decision. (PoC apps, Requests: gap 3)
- A filter on the creator ("my requests"), and an Item's time in its state
  (or `update_time`): the Requests app reads everyone's requests to find a
  person's, and one history per inbox row to order it. (PoC apps, Requests:
  gaps 4 and 5)
- The vacation request in a new version: no deduction for unpaid leave, and
  `f_days` checked against the dates (a working-day calendar). (PoC apps,
  Requests: gaps 9 and 10)

- ~~**Next, before Jira 2.4: the etag changes only when something a caller
  could overwrite changes.**~~ Done in the etag step (2026-11-20): see its
  entry, and design.md, "The etag".
- `group:` readers, so a narrowed portal can admit a whole set of
  customers (an organization) without naming each one. Until then a
  narrowed portal admits only the customers among its readers. (Jira 2.4a)
- A customer's word search reads their own rows and recomputes the text of
  the fields they see on each (`policy.CustomerWords`): fine for a few
  requests each, linear for a customer with thousands. A stored tsvector of
  the customer fields, or a search index, when one appears. (Jira 2.4a)
- A customer approving their own portal bot: today a bot's write that would
  need an approval is `APPROVAL_UNAVAILABLE` at once. A customer could
  approve within what they see. (Review of Jira 2.4a)
- The GitHub Actions `actions/checkout@v4`, `setup-go@v5` and
  `setup-node@v4` target Node 20, which is deprecated (CI's warning on
  PR #35). Move to versions on Node 24. (Jira 2.4a)
- An agent filing for a customer: `requester` is refused from an agent
  session, because the row has one `created_on_behalf_of` and the agent's
  user would be lost from `except: [creator]`. (Jira 2.4a, Q6)
- A customer's history pages by `item_seq` with the hidden events left out
  in SQL; reading deep histories may want an index on `(item_id, op)`.
  (Jira 2.4a)
- Mark the ticket's hook-written fields `writable_by: kernel` in a new
  ticket version (v7): `f_plan` (hook `get_plan`), `f_on_hold`
  (`get_credit_status`), `f_cost_center` (`erp_check`), all in v3 to v6,
  and `f_invoice_id` (`create_invoice`) in v4 to v6. Until then a hook's
  write of one of them changes the ticket's etag, and a caller may
  overwrite them. They are the only fields the kernel writes that are not
  marked. (The etag step)
- One function writes the Item row for every write path: `appendEvent`
  would take the row's change (state, fields, scope, container) and do the
  single UPDATE with both counters. Today 13 UPDATEs and 2 INSERTs each
  write `item_seq` and `content_seq`, held together by a source-reading
  test. A refactor of every write path, with no change of behaviour. (The
  etag step, Phase C)
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
  It also changes the etag: the etag rule reads the event's shape, not its
  values. (The etag step)
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
- **A GIN index on `items.fields` if containers grow past this size.** The
  contains and empty filters on multi-value fields scan the candidates
  (25 ms for one label among 35,000 Items; table in the Jira 2.2 entry).
  Past a few hundred thousand Items in a namespace, measure a
  `jsonb_path_ops` GIN index against its write cost. (Jira 2.2)
- Reaction outcome reasons are lower case in the item service and upper case
  through the API, like every event reason. Fine, but the two spellings met
  in three tests; one spelling in test helpers would have caught it. (Jira 2.2)
- A source-declared `on: create` reaction (the created Item moves another)
  was decided against in Jira 2.2 in favour of the container's
  `on: created_inside`. Revisit only if a non-contained case appears.
- Elapsed working time is kept in whole seconds, truncated
  (`working_between`): a clock that is paused and resumed loses up to a second
  per pause, in the agent's favour. Keep finer time if that ever matters.
  (Jira 2.3a)
- A Type that copies a calendar onto each Item it creates (pinned at creation)
  carries the holiday list on every such Item. Measured with the real
  `sla_goal` (Jira 2.3b): a goal's row is 783 bytes with no days off, 1,111
  with 10 and 4,279 with 366 (its fields alone: 293, 453, 1,526), and its
  create event's values as much again. Share or reference the calendar if
  that hurts.
- ~~A kernel-written field set by a reaction bumps its Item's etag, so a
  person's save made in that window is `ABORTED STALE`. Measure the count in
  scenario B (Jira 2.3b).~~ Measured in 2.3b: see the first entry above,
  which replaces this one.
- `CreateItem` with links in the same write (an Item that needs a link from
  its first moment). (Jira 2.3 Phase A)
- ~~A reaction's outcome record does not show its target through the API.~~
  Done in Jira 2.3b (`ItemEvent.reaction_target`).
- ~~A hook on an Item a reaction created has no recipient to read.~~ Done in
  Jira 2.3b (offers: `container:<name>`).
- From Jira 2.3b: closing a resolved incident by hand (today the timer's
  only); recomputing a running goal when the priority, the policy or the
  calendar changes; a third metric (`sla_goal@2`); a fallback recipient (a
  role) when a breach has nobody to tell; anyone can create a bare
  `sla_goal` with no fields outside a host (harmless: every field is the
  kernel's, but it should be refused); the queue and the breached search
  sort every match after a sequential scan (fine at 5,000 incidents; an
  index on a sorted field is the lever at 100 times that).

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

## 2026-11-19 — Jira proof of concept, part 2, step 1: date-time fields, a timer at a field's instant, computed effects (finished)

**Built:** primitives 1, 2 and 4 of "Decisions for Jira part 2". A `datetime` field holds an instant
(sent as RFC 3339 with an offset, stored and returned as UTC with six fractional digits), with
equality, ranges and `order_by`. A state may declare `at: {field, to}`: a `sprint@2` closes itself
when `f_end_at` passes, and moving the end re-arms the timer in the same run. A transition may
declare `sets`: `story`, `bug` and `task` `@2` stamp `f_resolved_at` with their done event's own time
and unset it on reopening. Through the API, pli, the web UI's generic form and the scenario
(steps J1 to J5). design.md: "Jira part 2, step 1 as built".

**Kernel changes, counted** (six; proto v1 only grew, `buf breaking` green; no migration):
(1) The `datetime` field type: one stored form, filters and ranges with bounds in any offset,
`order_by`, CEL timestamp. (2) The timer at a field's instant: `at` on a state, its load rules,
`NeedsWorkflow`, the relay naming the at-fields an event touched (`Event.FieldIDs`), the workflow's
branch. (3) Computed effects: `sets` on a transition, evaluated by `lifecycle.TransitionWith` on the
guard's view plus `cause.at` and `today`; exempt from `writable_by`; `effect_failed`. (4) **The
write's time**: an event's `created_at` is `clock_timestamp()` read after the row locks, not
`now()`; along `item_seq` an Item's event times never go backwards. (5) **Not yet**: the item
service refuses an `at` timer's move while the write's time is before the instant (`ErrNotYet`
with the remainder; unset is `ErrStale`). (6) `Service.AtInstant`, the one read that gives the
workflow a field's value, and the `LoadInstant` activity. Proto: `FIELD_TYPE_DATETIME`,
`State.timer`, `State.at`, `Transition.sets`. New refusal: `EFFECT_FAILED`. Not the kernel: the
four version 2 Types, the web input, the scenario.

**The declared exception.** "The workflow never sees a field value" now has one exception,
written next to the rule in design.md ("Durable behavior", and the Policy boundary): the instant of
a datetime field that the Item's current state names in `at`. `AtInstant` takes no field name, so
the workflow cannot ask for another. It is on the exemption list of
`TestEveryExportedReadCallsProject`, with its reason. The recorded histories hold the instant and
nothing else of the Item (checked: no field text in either file).

**Determinism, and how each point was proven:**
- *The wait is the instant minus `workflow.Now(ctx)`.* By grep: `internal/workflow/item.go` and
  `hooks.go` have no `time.Now`, `time.Until`, `time.Since` or `rand.`.
- *Re-arming cancels the old timer through its own context.* Each wait is a `NewTimer` in a
  `WithCancel` context, in a selector with a buffered SDK channel that the update handler feeds
  (`SendAsync`). An applied event always wins, whatever fired with it, so nothing depends on which
  coroutine wakes first. Proven by `TestAt_MovedLater_Rearms` and its neighbours (no firing at the
  old instant), and in the recorded history: three TimerStarted, three TimerCanceled in run 1.
- *Continue-as-new keeps a pending timer.* The instant travels in the input (`Snapshot.AtInstant`,
  `AtLoaded`). Proven twice: `TestAt_ContinueAsNew_CarriesTheInstant` (test environment), and
  `TestE2E_At_ContinueAsNew_KeepsTheTimer` on a second Temporal dev server started with
  `limit.historyCount.suggestContinueAsNew=40`: the first run ends ContinuedAsNew with its timer
  pending, the second closes the lap at its instant, once.
- *`LoadInstant` runs only on entering an `at` state and on events naming its field.* Counted from
  the real histories in that test: run 1 has 1 LoadItem and 2 LoadInstant (the entry, the moved
  end; none for the renames), run 2 has neither and 1 FireTimer. Also
  `TestAt_OtherEventsDoNotReadTheField`.
- *Old histories.* No `GetVersion`: only a run whose Snapshot has an `At` table enters the new
  branch. The four histories recorded from main replay unchanged. Two more are recorded from this
  code (`lap_at_rearm_then_continue_as_new.json`, 46 events; `lap_at_carried_timer_fires.json`, 21)
  and replayed by `TestReplay_AtTimerHistories`: the fifth history, with its continue-as-new, is
  two files because it is two runs.

**Never early.** With the item service's clock 800 ms behind the worker's
(`Options.ClockSkew`, test-only), Temporal fires at its own instant, the item service answers
NotYet, the workflow arms again for the remainder, and the closing event is stamped at or after
the instant (`TestE2E_At_ServiceClockBehind_NeverClosesEarly`: one to three NotYet, one closing).
`ClockSkew` cannot be set by a flag, an environment variable or a file:
`TestClockSkew_NoProductionCodeSetsIt` reads the module's source.

**One write, one now.** `cause.at` is the event's `created_at` exactly, and `today` inside `sets`
is its UTC date (tested one second either side of midnight, with the service's Go clock on the
other day). Guards still read `today` from the Go clock (Later).

**Tests:** 95 new Go test functions in 12 files, 21 new vitest tests (409 in all), 1 new Playwright test (4
in all). Changed existing tests, each agreed in review: `TestPli_Types` (19 to 23 Types),
`TestParseDir_ReadsTheRepositorysTypes` and `TestListTypes_EveryVersionInOrder` (the four version 2
Types), `TestHasListeners_PLATTypesHaveNone` (sprint@2 listens by design), and one entry added to
the Project test's exemption list. Six of my own Phase B tests had fixture mistakes, found in
Phase C and fixed after review; one more changed its expectation when `today` in an effect became
the stamp's date.

**Numbers:** `go test -race -count=1 ./...` green, no data race (workflow package 162 s, item
service 63 s). Scenario, two runs in a row on a fresh stack: **43.5 s and 44.0 s** (30 to 41 s
before; steps J3 and J4 wait about 12 s for real instants, since the dev clock scales durations
only). A sprint ending "5 s from now" closed itself 4.8 s after it was started, in both runs.
Playwright: 4 tests in 4.5 s.

**Surprises:** `now()` is the transaction's start, so events could already go backwards in time
along `item_seq`; the test reproduced it on the first try (a write held before its lock, another
committing meanwhile). A `datetime-local` input drops zero seconds from its value, in jsdom as in
browsers. A run in a state without behavior completes at once, so a workflow test cannot start
there and wait for an event. The existing rule "a kernel-written field needs a writer" met `sets`
immediately: `sets` now counts as that writer, which is also how such a field is unset (null).
Every event of an Item cancels and restarts its state's timer, even a rename (Later).

**Not verified here:** the images' build stages. This sandbox cannot download modules inside a
Docker build, so the stack ran from binaries and a site built on the host, packaged with a
throwaway override; CI's `scenario` job runs the repository's own Dockerfiles.

**Open:** the acting timer at an instant (the SLA breach records `breached_at` and the goal keeps
running) is Jira 2.3 scope, not Later: step 1's `at` only moves. A sprint@2 has a workflow for as
long as it is active, even without an end.
**Next session:** Jira 2.2: reactions that move or create, and multi-value fields.

## 2026-11-20 — Jira proof of concept, part 2, step 2: reactions that move or create, multi-value fields (finished)

**Built:** primitive 3 of "Decisions for Jira part 2" and the multi-value fields. A reaction
may **move**: declared by the source on entering a state (`kind: move, on: enter`), or by a
container about Items created inside it (`on: created_inside, of: [...]`): `ticket@6` goes from
`waiting_for_customer` back to `in_progress` when its own requester adds a public `comment@2`.
A reaction may **create**: entering `in_progress` from `open` puts a `checklist@1` inside the
ticket. A field declared `multiple: true` holds a set: `story`, `bug` and `task` `@3` have
`f_labels` and `f_reviewers`, with set operations and contains / empty filters. Through the API,
pli (`--add`, `--remove`, `--empty`), the web UI's generic form (chips) and the scenario (K1 to
K7). design.md: "Jira part 2, step 2 as built".

**Kernel changes, counted** (nine; proto v1 only grew, `buf breaking` green; no migration):
(1) **Move on enter**: a source's reaction transitions the Item one link away, with the target
Type's consent (`accepts_reactions: {type:id: {moves: [state]}}`), which stands in for `allowed`
and `except` only: guards, `required` and frozen fields still apply. (2) **Move on
created_inside**: declared on the container, `when` over `child.*` only, `by: creator`; needs no
consent, since the Type moves itself. With it, `Create` writes one outbox row for a child whose
container's Type reacts to its Type, and the relay delivers it to the *container's* workflow as
update `child`, starting the workflow if it has none. (3) **Create on enter**: a contained Item
of a Type that consents (`creates: [fields]`), fields computed by CEL from the source's fields and
`cause.at`; created by `reaction:<type>:<id>`, with no human creator. (4) **One outcome per
(reaction, cause)**, field-less, on the declaring Item: `reaction_applied`, `reaction_failed`,
or the new `reaction_skipped` (`condition_false`, `not_by`, `not_in_from`, `no_target`). A refused
target is an outcome, never a retry. (5) **The workflow's reaction queue**: pending reactions,
deduplicated, run one after another by one coroutine inside the hook budget; giving up records
`budget_exhausted`. (6) **Cycles refused at load** across move and create reactions. (7)
**Multi-value fields**: `multiple`, `max`, stored sorted and unique, empty is unset, set
operations `{add, remove}` recorded as asked (`set_ops`), `too_many_values`. (8) **Filters**:
equality on a set is contains, two are all-of, `empty: true` for any field; ranges and `order_by`
on a set are refused. (9) **Guards and policy see a list**; `allowed: [field:x]` matches any
member. Proto: `Reaction.trigger`, `of_types`, `when`, `by`, `to_state`, `target_from_states`,
`create_type`, `create_fields`; kinds MOVE and CREATE; `AcceptedReaction.moves`, `creates`,
`creates_fields`; `Field.multiple`, `max`; `FieldFilter.empty`; `ItemEvent.set_ops`;
`ITEM_EVENT_KIND_REACTION_SKIPPED`. New refusal: `TOO_MANY_VALUES`. Not the kernel: the six
new Type versions, pli's flags, the chips input, the scenario.

**Two times in one reaction** (decision 4a). The write a reaction makes carries two instants: its
event's own `created_at`, stamped inside that write like any other, and `cause.at`, the time of
the event that triggered it. `sets` and `fields` read `cause.at`, so a worker stopped for ten
seconds delays the bookkeeping and not the recorded instant
(`TestE2E_AReactionDelayedByAStoppedWorker_KeepsTheCausesTime`).

**Determinism.** No `GetVersion`: only a run whose Snapshot has the new reaction tables, or that
receives the new `child` update, takes a new branch. The six histories recorded earlier replay
unchanged. A seventh, `desk_woken_cold_by_a_child.json`, is the run a child's create started on
a container with no workflow (LoadItem, RunAfterReaction, completed);
`TestReplay_ReactionHistories` replays it. The queue is a slice run in order by one coroutine;
nothing iterates a map.

**EXPLAIN** (`go run ./cmd/bench --explain-sets`; 30,000 issues on version 3 in one space, 2%
hidden, 5,000 tickets beside them; each issue has one of 20 area labels and one of 300 team
labels, a tenth have none):

| Read | Fresh (ms) | Analyzed (ms) | Analyzed plan |
| --- | --- | --- | --- |
| The space's issues labelled area-3 (1,500), newest first, page 1 | 30.9 | 1.2 | no sort |
| The same, page 3 | 24.7 | 1.2 | no sort |
| Search: labelled rare (30), three Types | 26.8 | 25.6 | seq scan of 35,000, sort of 30 |
| Search: a label nobody has (0) | 27.4 | 25.5 | seq scan of 35,000 |
| Search: two labels, all of them (100) | 15.3 | 16.3 | sort of 50 |
| Search: I am a reviewer (600), open, by rank | 28.3 | 23.4 | top-N heapsort |
| Search: no labels (3,000), page 1 | 12.5 | 8.0 | sort of 1,425 |
| Words and a label: "alpha" (35,000 matches), team-63 (100) | 39.5 | 33.2 | sort of 50 |

A contains filter is `fields @> {"f": ["v"]}`, evaluated on the candidates the other conditions
leave: the same plans and cost as the existing field filters (assignee = me was 10 to 17 ms in
Jira part 1). **No GIN index**: nothing here demands one. Later has the size at which to
measure again.

**The etag rule in the web UI.** The Item page sends the etag with a save, except a save made
only of set operations: those commute with anyone else's, which is why they exist. The smoke
test has sam add a label while lea's page is open; with the etag her save was `ABORTED STALE`.
A save that also changes a plain field keeps the etag.

**Tests:** 67 new Go test functions in 10 new files, 20 new vitest tests (429 in all), 1 new
Playwright test (5 in all). Changed existing tests, agreed in the Phase B review: the Type-list
tests (23 to 29 Types). **Four fixes to my own Phase B tests, found in Phase C and applied
after review:** `TestAcceptsReactions_ListOrMap` used a string field where accepted `fields`
must be ints (a reaction adds); `TestReactions_API_ShowInHistory` and scenario K5 and K6
expected lower-case reasons, and the API gives every reason in upper case (`NOT_BY`,
`CONDITION_FALSE`, `GUARD_FAILED`; the convention stays); scenario K4's ticket was titled "VPN
drops" and, back in progress, showed up in D1's search for active VPN work.

**Numbers:** `go test -race -count=1 ./...` green, no data race (workflow package 167 s, item
service 62 s). Scenario, two runs in a row on a fresh stack: both passed. A ticket was back in
progress **279 ms** after its requester's comment (create, outbox, relay, a cold workflow start,
the reaction's write). Playwright: 5 tests in 4.6 s.

**Surprises:** the desk's own move, made by the reaction, is delivered to the desk's workflow
like any event, and by then the run that made it may have completed: recording "the last run"
saved the wrong history. A CEL list type has no `!= null`; sets are declared nullable lists. The
field decoder had a key whitelist that rejected `multiple`. My load rule "every version of an
`of` Type declares the child field" refused the real Types (`comment@1` has no `f_public`); it is
now "some version does", and a missing field reads as null. A form that saves with an etag
defeats set operations, which no unit test could see; the browser test did.

**Not verified here:** the images' build stages, as before (the stack ran from host-built
binaries with a throwaway override); CI's `scenario` job runs the repository's own Dockerfiles.

**Open:** a transition fired by a field change on the same Item (N-of-M reviewers) is still
open from the decisions. A container that reacts to children keeps getting one outbox row per
matching child, whatever its state; the skip is recorded (`not_in_from`).
**Next session:** Jira 2.3: the acting timer at an instant, working-time arithmetic, the
`sla_goal` Type.

## 2026-11-20 — Jira proof of concept, part 2, step 3a: working time, children, set, an acting timer at an instant (finished)

**Built:** the kernel pieces of Jira 2.3, proved with test Types (a job in a yard with hours
contains meters); the `sla` package, the incident and scenario B are step 3b, a second PR. A job
starts its meters when it is created, on its container's hours; pauses, resumes and stops them as
it moves; another person's public remark stops the reply meter; each meter shows its due instant
on its job and is marked late at that instant, never before, and goes on. design.md: "Jira part
2, step 3a as built".

**Kernel changes, counted** (ten; proto v1 only grew, `buf breaking` green; no migration; no new
direct dependency: `time/tzdata` is the standard library's):
(1) **Working time**: package `worktime` and the CEL functions `calendar`, `working_after`,
`working_between`, in every expression. (2) **Creation enters the initial state**: a from-less
enter reaction runs at creation. (3) **A reaction that moves children** (`children: {of,
when}`), on enter and on `created_inside`: at most 8, one write and one record per child, `more`
on all but the last. (4) **`by: not_creator`**. (5) **A reaction that sets** fields on the
container (`kind: set`), with consent `sets`. (6) **Consent by reaction ID** (`*:<id>`), through
containment only. (7) **A create reaction reads linked Items** (`linked.`, `container.linked.`),
with `readable_by_reactions`, FOR SHARE; and may set multi-value fields. (8) **An acting timer at
an instant** (`at`, `when`, `sets`, `hook`): `ActingInstants`, `ActAt`, the relay naming its
fields, the workflow's coroutines. (9) **`not_empty`** in filters. (10) **Types in subfolders**.
Proto: `Reaction.children_of_types`, `children_when`, `target`, `set_fields`, kind SET;
`AcceptedReaction.sets`; `ActingTimer.at_field`, `when`, `sets`, `hook`;
`Type.readable_by_reactions`; `FieldFilter.not_empty`. New reasons: `too_many_targets`,
`no_container`, `read_not_accepted`. Not the kernel: nothing; no Type in `types/` changed.

**Ten choices the tests pin** (all confirmed in the Phase C review): (1) "empty: false" is
`not_empty: true`; (2) per-child records carry `more` except the last, which finishes the
reaction; (3) create reactions may set multi-value fields, never principal ones; (4) a linked
read ignores the requester's visibility, `readable_by_reactions` is the consent and refuses
principal fields; (5) `working_between` returns whole seconds, truncated (Later); (6) a set with
no container is `skipped no_target`, without consent `failed reaction_not_accepted`; (7) a Type
two folders down is a load error, dot-folders are skipped; (8) a wildcard consent for added
fields is refused at load; (9) the timer's hook is keyed `timer:<item>:<timer>:<entry>:<instant>:hook`;
(10) `working_after` refuses a non-positive duration. Decided while implementing, also confirmed:
a child that left its state gets its own `skipped not_in_from`; a closing record when no record
lacks `more`; the bound of 8 counts the children that would move; a hook-only timer writes
nothing and uses no key; `hook_ran` run 0; the calendar functions in guards too.

**The crash test** (asked for in the Phase B review, written before the item service's code):
`TestReactMove_Children_ACrashPartway_FinishesExactlyOnce`. Three children; the call is cut short
while the row it needs next is locked by another transaction: the job's (after the first child's
write, before its record) or the second child's (after the first record). Back again, each child
is moved exactly once (its key), each has exactly one record, in the order of their IDs, and the
reaction stays pending until the record without `more`.

**Met or missed is decided on instants** (decision in the Phase C review, replacing G4's "from
the elapsed time", for 2.3b): stopped while running, missed if `cause.at > f_due_at`; stopped
while paused, missed if the breach mark is set after the pause's own correction. Found by a test:
with whole seconds, a meter stopped 50 ms after a 3 s due instant had "3 s elapsed" and said met
while marked late.

**Determinism.** No `GetVersion`: only a run whose Snapshot has an `ActingAt` table starts the new
coroutines, and a set reaction goes through the existing queue. The seven histories recorded
earlier replay unchanged; an eighth, `meter_late_at_an_instant.json` (LoadItem, a set reaction,
LoadActingInstants, a timer, ActAt, four events, a second set reaction, completed), is replayed
by `TestReplay_ActingAtHistories`. Waits are on `workflow.Now`; leaving the state cancels the
entry's coroutines; instants and what already acted are in the Snapshot; a cold start rebuilds
from `StateEntry` and `ActAt`'s key.

**Tests:** 102 new Go test functions in 11 new files, no web change (429 vitest, 5 Playwright,
as before). Changed existing tests, agreed: `TestEveryExportedReadCallsProject` gains the
exemption of `ActingInstants`, with `AtInstant`'s justification. **Five fixes to my own Phase B
tests, found in Phase C and applied after review:** a fixture timer without its hook
(typedef); a "quiet" fixture that left a reaction on the initial state (lifecycle); the test
meter's outcome decided on truncated seconds (e2e; now on instants); the three API tests that
register test Types moved to their own package, `internal/api/apistep3`, because Types are global
and `TestListTypes_EveryVersionInOrder` lists exactly the repository's; an assertion on a second
workflow's record without waiting for it (e2e, seen only under `-race`).

**Numbers:** `go test -race -count=1 ./...` green, no data race (workflow package about 200 s,
item service about 80 s). Scenario, two runs in a row on a fresh stack: both passed (47 s, 46 s).
Playwright: 5 tests in 9.9 s.

**Surprises:** a coroutine may only block on a context of its own, so the timer's write, which
must not be cut short by leaving the state, runs on `workflow.NewDisconnectedContext`, not on
the workflow's root context. CEL's type guards refuse a null argument before the function sees
it; they are off for the three functions, which say themselves what they lack. `cond ? null : x`
types as null in CEL; written `cond ? x : null` it keeps x's type. A fixture mistake in a test
that only asserts refusals hides itself: three Phase B tests passed before anything was built.

**Not verified here:** the images' build stages (Docker Hub answered 429: the stack ran from
host-built binaries on the real distroless base, with a throwaway override), and the pinned
Playwright's own browser build (the smoke test ran on this container's Chromium). CI's `scenario`
job verifies both.

**Open, for 2.3b's Phase A:** who a breach notification goes to (the goal has no human creator;
probably the host's assignee, declared by the host); the target of per-child outcome records
through the visibility filter. From the decisions, still open: a transition fired by a field
change on the same Item.
**Next session:** Jira 2.3b: `types/sla/` (`sla_goal@1`, `calendar@1`, README), `incident@1`,
scenario B, pli and the web UI; EXPLAIN for the queue sort and the breached search.

## 2026-11-20 — Jira proof of concept, part 2, step 3b: the sla package, incident@1, scenario B (finished)

**Built:** `types/sla/` (`sla_goal@1`, `calendar@1`, `sla_policy@1`, a README that is the contract with a
host), `desk@1` and `incident@1`, scenario B (B1 to B10) through pli, and the web UI's three generic
pieces. An incident starts a first-response and a resolution goal when it is created, with the target its
desk's policy gives its priority; the goals pause, resume, breach (a mark at the instant and one email to
the assignee of that moment), complete as met or missed, or are canceled; each shows its due instant on
the incident, which the queue sorts by. design.md: scenario B and "Jira part 2, step 3b as built".

**Kernel changes, counted** (three; proto v1 only grew, `buf breaking` green; no migration; no new
dependency; no workflow code changed): (1) **Offers**: a Type offers a principal field to the hooks of
contained Items; an action hook reads `container:<name>`, resolved when it runs, as the offered principal
sees the container; `ErrNoRecipient`, recorded once as `hook_failed no_recipient`. (2) **A link only a
role writes** (`writable_by: ['role:…']` on a link). (3) **A reaction record names its target**
(`ItemEvent.reaction_target`, through visibility). Proto: `ItemEvent.reaction_target`, `Type.offers`
(`Offer`), `LinkType.writable_by_roles`. Not the kernel: pli's `--not-empty`, the Types, the web UI.

**Two visible behaviour changes** (approved in the Phase C review): a reaction's outcome record no longer
fills `link_target` in a history (it did, with an empty link type); it fills `reaction_target` only. The
fixed message of `LINK_NOT_WRITABLE` is now "the caller may not set links of this type" (it said "only
the kernel sets links of this type").

**Decided in the reviews:** the goal's end state is `completed`, with `f_outcome` met or missed (design.md
amended; it had two end states); a met goal never carries a breach mark (a stop or a pause caused at or
before the due instant clears it); resolving requires `f_resolution`, and reopening clears it; resolved ->
closed is the timer's only; only action hooks read an offer, and no multi-value principal is offered; a
goal resumed with no time left is due at the resume instant (`working_after` refuses zero seconds).

**FOUND, NOT FIXED: the etag changes too often.** The etag is `item_seq`, and every event bumps it: a
reaction's outcome record and a hook's record as much as a kernel-written field. A save made with the
etag read right after an incident's create: 0 of 10 STALE with no delay, **10 of 10 at 100 ms, at 500 ms
and at 2 s**. Both due instants are on the incident 322 ms after its create (p50; p90 387 ms, max 401 ms).
One cycle (create, answer, wait, customer reply, resolve) is 20 events on the incident: 1 create, 4
transitions, 6 due-instant writes by its goals, 9 reaction records. So exempting kernel-written fields
alone (the fix noted in step 3a) would not be enough. Top of Later, the next step before Jira 2.4; the
README's web UI section warns of it.

**Tests that failed on their own logic once the Types existed** (most Phase B tests failed at the
fixture: the Type files did not exist): three, fixed after review. Two item service tests counted a
goal's events from 1, forgetting the goal's own outcome records (now `item_seq` before and after); the API
test's `UnlinkItems` sent no `request_id`. A fourth was met in the code instead: pli's test expected the
server to refuse `--empty f --not-empty f`, which as two filters is legal, so pli sends them as one
filter, which the server does refuse. Every other new test passed on first contact with the real Types,
the eight end-to-end tests and scenario B included. Existing tests changed, agreed: the three Type-list
tests gain the five Types (34).

**Determinism.** No workflow code changed: nobody to tell is answered inside the `RunAction` activity
(the item service judges, the activity records and succeeds). Two histories recorded from this code as
baselines of the first real host: `incident_cycle.json` (63 events: both starts, the pause, the
requester's comment, the resume, the stop, the scaled close timer) and `sla_goal_breach_and_mail.json`
(76 events: the set reactions, LoadActingInstants, ActAt at the instant, the breach hook). With the eight
earlier ones they replay (`TestReplay_*`).

**Numbers.** `go run ./cmd/bench -sla`, 5,000 incidents and 10,000 goals, analyzed: the queue by
`f_due_resolution` (3,003 of 5,000 with a due instant) 8.9 ms; open incidents by due instant 5.3 ms;
overdue now 4.3 ms; ever breached (`f_breached_at` not empty, latest first) 4.0 ms (a first run gave
9.3, 6.3, 6.4 and 5.5). All four are one
shape: a sequential scan of `items` filtered by Type and by the field's presence, a hash join to the
readable scopes, a top-N heapsort (52 to 128 kB); no index serves the field. A goal's row: 783 bytes
with no days off, 1,111 with 10, 4,279 with 366. Scenario: twice in a row on a fresh stack, 84 s each
(both goals exist 293 ms after an incident). Playwright: 6 tests in 10.9 s. vitest: 438.

**Surprises:** YAML's merge key (`<<: *pause`) lets the stop reuse the pause's three effects, so the
correction is written once. Two existing source-reading guards caught the new bench mode: it
borrowed a skewed clock to breach goals (`TestClockSkew_NoProductionCodeSetsIt`; it now uses a
one-second policy and waits) and called the internal door itself
(`TestInternalDoor_OnlyTheWorkerAndHookRunnerCallIt`; it now runs the reactions through the worker's
activities). The second was found only by the full rerun, after the PR was open. A count of an Item's
events is a brittle assertion wherever reactions record their outcomes on it.

**A flaky step 3a test, found by CI on this PR and fixed in it.**
`TestE2E_Step3_AStoppedWorker_ComputesTheSameElapsedAndDue` failed once in CI ("not within 15s: the same
instant on the job") and passed on the re-run. *Cause, from the failed run's log:* the test stopped the
worker the moment the meter was paused. At 17:23:56 the meter's workflow scheduled its own `hide`
reaction (`RunAfterReaction`, activity 34) and the worker stopped in that same second. The log has no
"Activity error" line for it (a reported failure logs one, as another test's did at 17:23:52) and no
completion: the attempt was handed out and never answered. The worker came back at 17:23:58, the meter
resumed (LoadActingInstants, a new timer), and its `show` reaction, queued behind the unanswered `hide`,
never ran before the test gave up at 17:24:13. An attempt nobody answers is retried only when its
**StartToClose timeout (30 s)** runs out, plus the retry policy's first interval (1 s): 31 s, against a
wait of 15 s. It is a test race, not a reaction that never resumes: nothing in the log contradicts it,
and the new test below reproduces exactly that and sees the reaction complete at 31 s. *Fix (approved):*
the test waits for the job's `f_due` to clear (the `hide` done) before it stops the worker again.
*New test,* for what the old timing probed by accident:
`TestE2E_Step3_AWorkerStoppedMidReaction_TheReactionCompletesOnce_WithinItsTimeout`. A worker with a
connection of its own holds a reaction's activity in flight (the row it needs is locked); the connection
is cut, then the worker stopped, as a crash would; a new worker starts. The reaction completes **exactly
once** (one move, one record) with the times computed from the cause, and within a bound derived from
the activity's own options: **StartToCloseTimeout 30 s + InitialInterval 1 s (+ 5 s to hand the retry
over) = 36 s**; measured 31 s, three runs in three. (A worker that is merely stopped in-process reports
the cancelled attempt and is retried after 1 s: the first version of the test measured 2.1 s and proved
nothing, hence the cut connection.) So after a worker crash, whatever a reaction or a hook was doing
comes about half a minute late: a goal's pause, the due instant on its host, a breach notice. Never the
computed times. Under a minute, so not in Later.

**Not verified here:** the images' build stages (module download fails inside the build container: the
stack ran from host-built binaries on the real distroless and nginx bases, with a throwaway override)
and the pinned Playwright's own browser (the tests ran on this container's Chromium). CI's `scenario` job
verifies both.

**Open:** a transition fired by a field change on the same Item (N-of-M approvers), from the decisions.
**Next session:** the etag rule (top of Later), then Jira 2.4.

## 2026-11-20 — The etag step: the etag changes only when something a caller could overwrite changes (finished)

**Built:** `items.content_seq`, a second counter per Item, in the same row and written by the same
statement as `item_seq`; the public etag is derived from it and written `c7`. It moves on a state change,
on a field a caller may write (role-written fields and set operations included, whoever writes), on a
link of the source, on a readers change and on a move; never on a field-less record (`hook_ran`,
`hook_failed`, the three reaction outcomes, a denied or failed hold) nor on a write that names only
`writable_by: kernel` fields. design.md: "The etag", under "Public API (v1)". The README's known limit
is gone.

**The measurement of Jira 2.3b, again:** the save made with the etag read right after an incident's
create is accepted **10 times in 10** (`TestE2E_Incident_TheSaveWithWhatWasReadAtTheCreate_IsAccepted_10In10`,
a real worker; it was refused 10 times in 10). One whole cycle of an incident leaves `content_seq` at 5
(the create and the four moves people made) whatever the kernel wrote. A real conflict is still `STALE`:
two people on one field, a transition between the read and the save, at every level (item service, API,
pli in scenario B11, the browser).

**Kernel changes, counted** (one; migration 00012; **proto unchanged**; no new dependency; **no workflow
code changed**, the replay tests pass untouched): the counter and its rule. The rule is one pure function
(`changesContent`, `internal/itemservice/content.go`) over the event's kind, its payload and the Type,
with **no default**: an event kind nobody decided fails its write. A table test covers the 13 kinds in 41
rows; `internal/api` holds its list of kinds to the decided ones, both ways. `ExpectedContentSeq` is the
new if-match on update, transition, link and unlink; `ExpectedItemSeq` is untouched on both doors, and
both may be sent. A request without the new field hashes as before (omitempty; three hashes computed on
main are pinned in a test). Existing Items start at `content_seq = item_seq`. Not the kernel: the API's
etag text, the web UI.

**Visible behaviour change:** the etag's text. A decimal etag from before this step is `STALE`, like any
etag the server never made (`c01`, `C1`, `c1 ` too: the exact text only).

**FOUND, AND THE RULE IT LEFT: never UPDATE an Item row twice in one transaction.** Phase A decided that
`appendEvent` would bump `content_seq` itself, with a second UPDATE of the row, so that no write path
could forget. `TestReactMove_Children_ACrashPartway_FinishesExactlyOnce` then failed: with the job's row
locked by someone else, the reaction no longer moved its first child. *Cause:* when a transaction updates
a row it has already written, Postgres re-checks the row's foreign keys and takes `FOR KEY SHARE` on the
container's row and on the scope's; one UPDATE does not. Every content-changing write on a contained Item
would have waited for any transaction holding its container's row lock: new contention, and a new way to
deadlock where a reaction writes a parent and its children. Reproduced in plain Postgres with a two-row
table (design.md, "The etag", has the SQL): one UPDATE of the child returns at once while the parent is
locked `FOR UPDATE`; two UPDATEs of the child in one transaction wait on
`SELECT 1 FROM ONLY items x WHERE id = $1 FOR KEY SHARE OF x`. *Fix (approved, option A):* each of the 13
UPDATEs and both INSERTs that write `item_seq` writes `content_seq` in the same statement, from the
rule's decision (`decideContent`), which `appendEvent` now requires in place of the event's kind. A
source-reading test (`TestContentSeq_EveryStatementThatWritesItemSeqWritesItToo`) holds every statement
to it; it was seen failing on a deliberately broken statement (`links.go:206`: "this statement writes
items.item_seq and not content_seq"), and a second test feeds it four broken shapes. Option B (one
function writes the row for every path) is in Later.

**Numbers.** A field update, 3,000 calls after 100 warm-up, p50 / p95 in ms. With the second UPDATE:
2.03 / 2.5 against 1.84 / 2.2 without, about +10%, the same for a top-level and a contained Item. With
one statement, three runs each, interleaved: top-level 1.85 to 1.99 / 2.40 to 2.47 against a baseline of
1.91 to 2.03 / 2.32 to 2.37; contained 1.92 to 1.99 / 2.29 to 2.43 against 1.93 to 1.99 / 2.29 to 2.41.
Back at the baseline, within the noise. Scenario: twice in a row on a fresh stack, 84 s each, B11
included. Playwright: 7 tests in 12.6 s. vitest: 453. `go test -race ./...` green (workflow suite 306 s).

**The web UI.** The Item page keyed its form on the etag and the state; a kernel-written field no longer
changes the etag, so a refreshed page would have kept an old due instant in its form and sent it with
the next save (`FIELD_NOT_WRITABLE`). The form's key is now the etag, the state and the fields, and a
save never includes a field the user cannot write: a kernel-written one, or one reserved to roles the
acting user lacks. Such a role-written field is read-only for them, with a hint naming the role (when
the roles are not known it is sent and the server decides). The four tests of that read-only input were
written together with its code, not before it (agreed in the Phase C review).

**Fields the kernel writes that are not marked `writable_by: kernel`:** the ticket's hook-written fields
and nothing else: `f_plan`, `f_on_hold`, `f_cost_center` (ticket v3 to v6) and `f_invoice_id` (v4 to
v6). A hook's write of one of them changes the etag (a test pins it). Every field a reaction, a
transition's `sets` or an acting timer writes is marked. No Type was changed; Later has the new ticket
version.

**Existing tests changed, each agreed:** 21 literals in 7 files spell the etag `c1` where they said `1`
(one commit of their own); scenario K7 compared a story's etag with the length of its history and now
compares it with itself; `TestMeasure_Incident_StaleSaves` sends `ExpectedContentSeq`;
`TestGet_ReturnsCreatedItem` expects `ContentSeq: 1` in the whole Item it compares.

**Surprises:** the foreign-key re-check above, which no test of this step was written to find: a test of
step 3a about a crash mid-reaction found it. The scenario already held one confusion of the etag with
`item_seq` (K7), the very thing the new text form is for. And "one place, so no path can forget" turned
out to be cheaper to hold with a test that reads the source than with a second statement.

**Not verified here:** the images' build stages (module download fails inside the build container: the
stack ran from host-built binaries on the real distroless and nginx bases, with a throwaway override)
and the pinned Playwright's own browser (the tests ran on this container's Chromium). `buf lint`,
`buf breaking` and the generate checks were not run here either: the proto is untouched. CI verifies all
of them.

**Open:** nothing from this step.
**Next session:** Jira 2.4.

## 2026-11-21 — Jira proof of concept, part 2, step 4a: customers (finished)

**Built:** `customer:<id>` principals with fixed permissions; `own`, `shared` and `customer_fields` on Types; the
customer rule in SQL in two forms (a predicate and a relation) beside `VisibleCondition`, applied on Get, Search,
List, links, rollups, history and writes; `policy.Project` with the Item's Type (customer fields only, no etag);
`requester` on create; customers and a portal bot in the dev-token file; desk@2, incident@2, reply@1, note@1;
scenario C1; pli `--requester`; the web UI's customers. design.md: "Jira part 2, step 4a as built", the Public API's
Writes (no etag for a customer, `requester`, the idempotency key), the principals bullet, the Web UI.

**Kernel changes, counted** (six; migration 00013; proto v1 only grew, `buf breaking` green against main: 
`CreateItemRequest.requester`, `Type.own`, `Type.shared`, `Type.customer_fields`; no new dependency; no workflow
code changed, the replay tests pass untouched):
1. **The `customer:` kind and its fixed permissions**: a valid actor, and `on_behalf_of` of an agent; no role is
   ever read for one; a customer creates only what a container admits, takes only `creator` moves as the
   requester (also through an empty `allowed`, which the authorize shortcut used to let through), never updates,
   moves, narrows or links; a session for one holds a customer's powers only.
2. **`own`, `shared`, `customer_fields`** and their load rules (per version, and across the set in `ParseDir`).
3. **The read rule with field projection**: `policy.CustomerCondition`, `policy.CustomerRelation`,
   `policy.CustomerWords` and `Project`'s Type; Search and List build their query in one function each
   (`pageSQL`), which the plan test explains.
4. **`requester`** on create.
5. **The index** `items_requester` (partial: Items made by or for a customer).
6. **The idempotency key** is `(namespace, actor, effective user, key)`. Existing rows keep their answers: an
   agent's row takes the person its stored result's event was written for, any other row its actor. It also
   fixes an agent acting for two staff users (the second write was refused as a reuse).

**Not the kernel:** the four Types, pli's flag, the dev-token file's checks, the API's mapping, the web UI.

**Postgres 17 (postgres:17-alpine), the queries Search and List actually build** (`pageSQL`), for one customer
among 10,000 with 5 incidents each, on 500,000 Items (2 portals, 50,000 incidents, 100,000 goals, 100,000 notes,
150,000 replies, 100,000 stories, 500 incidents narrowed to staff), analyzed. EXPLAIN (ANALYZE, BUFFERS):

| Read | Rows | Time | Buffers |
| --- | --- | --- | --- |
| Search incidents | 5 | 0.39 ms | 74 |
| Search in_progress | 0 | 0.27 ms | 54 |
| Search words "vpn" | 20 | 0.91 ms | 139 |
| Search every Type | 22 | 0.36 ms | 147 |
| List the portal | 5 | 0.27 ms | 58 |
| List the top level | 2 | 0.27 ms | 46 |
| List her incident | 3 | 0.31 ms | 55 |
| Get a reply (the predicate) | 1 | 0.15 ms | 16 |
| The predicate as the driver (not used) | 22 | 1,086 ms | ~2,015,000 |

No plan of a real read scans `items`: `items_requester`, then `items_container`, `items_namespace_type_state`
for the portals, and primary keys. The plan test holds it on a 25,000-Item set.

**Visible behaviour changes:** a reader may be `customer:`; `acting_for` may be a customer; `#/items` alone is
the top level in the web UI; a create with a bad `requester` is refused for it before its fields are checked.

**Existing tests changed, each agreed:** the three Type-list tests gain the four Types (38); the web's dev-user
list gains kim and lee; the API test server gains customer tokens and a portal bot in lists of their own;
`TestContentSeq_MigrationBackfillsFromItemSeq` steps down to version 11 by number (one step down undid 00013).

**Tests that failed on their own logic** (Phase B tests, fixed after review, one commit): six mistakes in seven
tests. `anyType` named two test Types registered only by a later test (a filter on an unknown Type is refused);
`bug@3`'s field is `f_summary`; `sla_resume` was run for the reaction's outcome record instead of its transition;
the API's Type filter wants the full name; the end-to-end customer had no namespace, which List refuses. Every
other new test passed on first contact with the code, the property test (3 seeds × 60 steps, every principal ×
every Item, Get, Search and List) and the plan test included.

**Surprises:** the kernel's own actors carry `on_behalf_of` for attribution, now possibly a customer: the rule
asks only who the actor is or which customer an agent acts for, or a customer's reply would have stopped the
reactions it triggers. `authorize` skipped `allowed` when empty, which would have let a customer start work on
their own incident. A Type version never changes once registered, so a changed Type file needs a fresh database
(the stack refused to start). The scenario's variables are global: C1's loop reused `$hidden`, which D3 reads.

**Not verified here:** the images' build stages (`go mod download` fails inside the build container: the stack ran
from host-built binaries and a host-built site on the real distroless and nginx bases, with a throwaway override)
and the pinned Playwright's own browser (the tests ran on this container's Chromium). CI builds the images.

**Numbers.** Scenario: twice in a row on a fresh stack, 95 s each, C1 included. Playwright: 8 tests in 7.2 s.
vitest: 458. `go test -race ./...` green, no race found (workflow suite 317 s). One end-to-end test of this step expected three replies where it writes four; fixed after review, in a commit of its own.

**Open:** nothing from this step. **Next session:** Jira 2.4b, the access request with N-of-M approvers.

## 2026-11-21 — Jira part 2, step 4a: the review's fixes (finished)

**Built:** the fixes of docs/reviews/2026-11-21-customers.md (five findings, four unproven points) and one
more finding of the same kind met while fixing them. design.md: "The per-row rule", the Writes section
(`commit_seq` 0 and `min_seq` refused for a customer), `APPROVAL_UNAVAILABLE` in the error table, the Agents
and Web UI notes. The review report marks each finding fixed.

**Kernel changes, counted** (seven; no migration; proto unchanged, `buf breaking` green; no new dependency; no
workflow code changed, the replay tests pass untouched):
1. **The per-row rule**: for a customer, a field or link name counts on a row only when that row's own Type
   version lists it: field filters and link conditions match nothing else, `order_by` sorts the rest as empty,
   after; a name no version lists is refused in the same bytes as an unknown one (`typesFor` drops unlisted link
   names too; `rowListing`). Found while fixing finding 1: field filters and `order_by` read the field on every
   row, whatever its version, so a field a later version shows a customer would have revealed the earlier
   versions' values. No Type in types/ triggered it.
2. **A customer's links are chosen in the SQL**, so the 200-link limit and `links_truncated` count only theirs.
3. **`commit_seq` 0 for a customer** (and an agent acting for one) in every write's answer, replays too, and in
   history; **`min_seq` refused** from them.
4. **The requester must see the Item as written**, checked in the create's transaction.
5. **A listed rollup needs its link listed** (load rule).
6. **The customer rule again under the lock** on a customer's create (the container) and transition: the
   check before the transaction left a window in which a move made the write land outside what admits them.
7. **`ErrApprovalUnavailable`** (`PERMISSION_DENIED / APPROVAL_UNAVAILABLE`, no metadata) for a write by an agent
   acting for a customer that would need an approval: refused at once, never pending.

**Not the kernel:** the web's `customerView` drops `field:X` entries a customer cannot see (the Inbox), scenario
C12.

**Word search was already per version** (`policy.CustomerWords` reads each row's own customer fields):
`TestFix_Words_HoldPerVersion` passed before the fixes, and stays.

**The property test** (`TestProperty_CustomerRule_AgentsLinksAndWords`, new): portal bots as actors and
readers, linked tickets (a listed and an unlisted link name), words in visible and hidden fields, and vtickets
at v1 and v2 with `f_code` and `rel` set on both. 3 seeds × 70 steps, 2.9 s (fails above a minute). Its Search
count over every Item uses the step's `anyType` filter, which names the step's Types and not the test's
vtickets (a filter naming an unregistered Type is refused, and the vtickets are registered by this test only);
so that count covers anyType's Types, and the vtickets are covered by the per-version checks instead (field
filters, `order_by`, link conditions and words, for every principal). I corrected that count in this test
before Phase C, where I had first compared it with every Item.

**Unproven points of the review, settled:** the check-then-write window was real for a move (fixed, 6); a
narrowing in that window was already refused by the scope check in the transaction
(`TestFix_CheckThenWrite_ThePortalIsNarrowedAway` passed before the fix, kept as a guard); the portal bot's
approval (fixed, 7); the Inbox, real for a move that names both `creator` and `field:X` with X hidden (no Type in
types/ has one; fixed in the web, vitest and Playwright). **Migration 00013's down step** restores the key
`(namespace_id, actor, key)` and fails if an agent used one key for two people after the migration: dev only, no
change.

**Tests:** 9 new item service tests, the property test, 3 API tests, 3 vitest, 1 Playwright; the five review tests pass
unchanged. No approved test changed.

**Numbers.** `go test -race ./...` green, no race (workflow suite 314 s). Scenario: twice on a fresh stack, 96 s
and 95 s, C12 included. Playwright: 9 tests in 8.3 s. vitest: 461. The stack ran from host-built binaries as
before (the image build cannot download modules here); CI builds the images.

**Open:** nothing. **Next session:** Jira 2.4b.

## 2026-11-21 — PoC apps: a product-shaped client (finished)

**Built:** `apps/poc`, a third client of the public API: a service desk (a desk's queue in five views with
wall-clock countdowns and Breached from the goals; an incident's page with assign, moves, service levels and
their timeline, a conversation where a reply and an internal note cannot be confused, causes, history), a
customer portal (pick a desk, open a request, my requests, a request's public conversation and the requester's
moves) and a tracker (the active sprint's board with pointer and keyboard moves, an issue's page, the backlog
with quick filters, search and quick create, epics with progress). Shared: dev sign-in, a person menu (switch
user, theme), light and dark held to WCAG AA, refusals in plain words, polling every 30 s on visible pages. The
`poc` compose service on 127.0.0.1:8091; CI builds, unit-tests and runs the three journeys. No kernel, proto or
Type change. design.md: "PoC apps as built", with the twelve gaps and their cost.

**Tests:** 260 vitest (the logic and four components); three Playwright journeys with axe on every main screen:
lea on the board, sam working an incident (a real first-response breach in 3 s), kim in the portal (every API
response she receives scanned: no note, goal, assignee or due instant, nor their values).

**Decided during the step, each agreed:** the queue's Paused comes from the incident's state, the incident page's
from the goal; Type definitions are left out of kim's key and Type scan (Types are readable by anyone), values
are checked everywhere; the portal's description is required; history lines name the actor, messages say "You".

**Test changes, each agreed, each in a commit of its own:** kim's assignee is bob, not lea: lea created the desk,
and an Item's creator is part of what a customer sees, so the scan could not tell lea the assignee from lea the
creator; the visible-text checks match whole words and the values exact JSON strings. Issue keys use the LAST 8
characters of the ID: UUIDv7 starts with a timestamp, the same on every card of a run.

**Tooling:** the lock file starts from web/'s tree (npm 10's resolver crashed, `Cannot read properties of null
(reading 'edgesOut')`, on a fresh install), so both apps pin the same versions. `axe-core` is pinned to 4.14.0 by
an `overrides` entry (4.14.1 was five days old). vitest runs the logic (`.ts`) in Node and the components
(`.tsx`) in jsdom, as two projects: `devusers.test.ts` reads the repository's dev-token file, which needs a file
URL (web/ marks such a test `@vitest-environment node`); the test file is unchanged. The apps import
`web/src/gen` and `web/src/rank` read-only (path aliases; the image gets them as two more build contexts).

**Fixed on the way:** axe's `label-content-name-mismatch` on the person menu: its button had an `aria-label`
that did not contain its visible text; it now shows the name, with "Signed in as" for screen readers only.

**Surprises:** customers see an Item's creator, so a value check on a principal must pick someone who created
nothing the customer sees. Item IDs are UUIDv7: their first 8 characters are no short ID. lea's journey passed
on its first run against the stack. Rebased on main after PR #36 (customers' `commit_seq` 0, `min_seq`
refused, unlisted names refused): nothing in the apps changed; the portal sends none of them.

**Numbers.** On the rebased tree: scenario passed on a fresh stack; the journeys twice in a row, 21 to 39 s for
the three; vitest 260; `go test ./...` green. The stack ran from host-built binaries and the poc image from a
build that trusts this container's proxy CA for `npm ci` only (as for Go, the image build cannot download here);
the journeys ran on this container's Chromium, against the poc container. CI builds the real images and runs
the pinned Playwright's browser.

**Open:** the twelve API gaps (Later). **Next session:** Jira 2.4b.

## 2026-11-22 — Jira 2.4b, PR 1: the building blocks (Phase D)

**Built:** what the access request needs, on test Types (rack, jar, bean; crate, pea; duo; errand), and three
Type versions. **Eleven kernel changes** (design.md, "Jira 2.4b, PR 1 as built"):
1. `container.<field>` in `sets` and create reactions (with `readable_by_reactions`' consent);
2. who, by plain reference only, in three parts: `sets` may write a principal field and a multi-value field;
   `readable_by_reactions` may list a principal field; a create reaction may set a principal field. Each only
   as a plain reference (`container.<field>`, a field of its shape, `each`, one linked read), never computed;
3. `for_each` on create reactions (`each`; a key and a record per member);
4. guards read `container.{state,category}` and `children.<type>`, with no visibility filter;
5. a move reaction with `target: container`;
6. `on_guard: skip` (`reaction_skipped guard_false`), opt-in;
7. `except: [container_creator]`;
9. `unique_among_siblings` (`NOT_UNIQUE`);
10. `placement_writable_by` (`PLACEMENT_NOT_WRITABLE`);
11. a hand-over: `sets` writes a field its own `allowed` names, by plain reference;
12. `allowed: [nobody]`.
Not built: #8 (`excludes`, dropped: a documented weakness with Q4c) and #13 (two levels up). The lock order:
the container (FOR SHARE, or FOR NO KEY UPDATE for a unique field), then the Item with its linked rows in one
UUID pass, then children in UUID order; a set reaction takes the container first. `write()` counts the
deadlocks it retries. desk@3, sla_goal@2, sla_policy@2. `kernel.v1` grew four fields; no migration.

**Tests:** 13 typedef tests, 16 item service tests (three of them concurrency tests that want no deadlock
retried), 2 API tests, 1 vitest. **Test changes, each agreed, in a commit of their own:** two cases of
TestJira24b_2 judged across Types (one Type alone cannot know whether the container's field holds a set);
`rack4b("")`; two step-3 cases turned into the boundaries of changes 1 and 2; four lists of the repository's
Types grew (typedef twice, the API's ListTypes, pli's type list); the API's 2.4b test Types registered once in
TestMain; TestJira24b_10's agent asked before the rack is narrowed (and `NOT_FOUND` after); the 2.4b test jar's
Type ID is `urn` (another test registers a `jar`).

**Surprises:** a guard's children got a CEL type of their own (`map(string, dyn)`): giving them a linked
Item's type would have refused, at load, existing link guards that read a field (they fail when they run). The
two Phase B test Type IDs that collided only showed once the fixtures loaded. A set reaction's old UUID order was
exactly the deadlock the new order removes.

**Numbers.** `go test -race ./...` green (item service 122 s, workflow suite 323 s, with the replay test and the
property tests). Scenario: twice on a fresh stack, about 95 s each. Playwright: 9 tests in 8.4 s. vitest: 463.
The stack ran from host-built binaries as before, **without the `poc` service**: its image cannot be built in
this container (`npm ci` fails), and the scenario does not use it; CI builds every image and runs the PoC
journeys.

**From the walkthrough of the reply path:** a dropped reaction (Later); a children move's unlocked
`target_from` read is safe, since the transition checks again after its lock (design.md, Locks).

**Open:** the two high Later items (placement for the other Types, sla_goal@1). **Next session:** PR 2, the
access request (resource, access_request, approval, fulfilment_task, scenario E), after PR 1 merges.

## 2026-11-22 — PoC apps: Requests, a process app on the vacation request (finished)

**Built:** a fourth area of `apps/poc`, Requests, on `vacation_request@3` and `leave_balance@1`, to see how a
BPM-style app feels on the kernel before the Bonita processes: My leave (the balance, my requests and where each
stands), Request time off (a draft, then the move to submitted; refusals in plain words, the draft kept), Waiting
for you (oldest first, how long each has waited), a decision screen (the request, the requester's balance, their
other requests in the period; approve or reject with a comment, under the etag read), a case page with a process
map drawn from `GetType` and a timeline, and Balances for `hr_admin`. Every staff member sees the area (the Types
name no role for asking or deciding). `compose.demo.yaml`: a dev day of 2 minutes for exploring by hand. No kernel,
proto or Type change. design.md: "PoC apps as built", the Requests area, with its twelve gaps and their cost.

**The process map** is generic: one column per state category (the end states share the last), right-angled
arrows with their own ports, lanes and tracks, system steps numbered from what `GetType` declares (checks, timers,
acting timers, kernel-set links, reactions). A geometry test holds five Types to it: no arrow crosses a box, two
arrows that share no end never run along each other, labels apart, everything inside the map.

**Tests:** 148 new vitest (the map, the inbox, the form and its refusals, deciding, the moves offered, the
timeline, `nobody`), 408 in all; a fourth journey with axe on six screens: ana requests, a refusal for dates that
make no sense, bob approves from his inbox within the request's 10 s, the balance drops, a request past the
balance is refused when bob approves and he rejects it, the case page at Approved, hana's balances.

**Decided during the step, each agreed:** the decision carries the etag (a request recalled, edited and sent again
meanwhile is `STALE`); the days follow the dates only until typed, then the count is a hint; the decision screen's
axe check runs on the first request, so the second's 10 s hold only the refused approval and the rejection.
**Test changes, each agreed, each in a commit of its own:** those three (Phase B), and the journey waits for My
leave's heading before reading its table (`cellOf` reads the headers at once; the case page was still showing).
`devusers.test.ts`: Requests appended to two expectations (agreed in Phase A).

**Verified against the stack:** two reminders and a refused approval leave the request's etag as it was (no kernel
bug: the etag step holds); on `compose.demo.yaml`, a request sent 4.3 minutes earlier still waited, with exactly one
reminder. **Rebased on main after PR #38** (Jira 2.4b): `allowed: [nobody]` is now a move no person makes in every
reader of `allowed` in the apps (one predicate, `isSystemOnly`, as web/ does), with a test of its own.

**Surprises:** `GetType` marks Approve as checked: the held deduction holds it, not a check hook. An email's
`hook_ran` carries the root human as `on_behalf_of`. `order_by` refuses a principal field. A balance write's
`causation_id` names the request's event, so the case ties the deduction (the approval's ask) and the refund (the
withdrawal) to itself. The whole journey runs in about 6 s, well inside each request's 10 s.

**Two local workarounds, outside the repository (nothing committed):** Docker builds in this container cannot use
the HTTPS proxy's CA, so a compose override in the scratchpad served Go modules from the host's module cache
(`GOPROXY=http://127.0.0.1:8899`, `build.network: host`) and built the two nginx images from host-built sites
(`dockerfile_inline`); and the pinned Playwright (1.65.1) wants a newer Chromium than this container's, so a local
config, excluded from git, pointed `executablePath` at `/opt/pw-browsers/chromium`. CI builds the real images and
runs the pinned browser.

**Numbers.** On the rebased tree: scenario passed on the stack; vitest 408; the four journeys twice in a row, 27
and 28 s; `go vet` clean; `go test ./...` green (no Go changed).

**Open:** the four Later items needed for Bonita, and the API gaps. **Next session:** Jira 2.4b, PR 2, or the
Bonita processes. Heads-up: 2.4b's next PR gives lea `access_admin` in the dev-token file, which
`devusers.test.ts` reads; whichever PR merges second rebases and updates that expectation.

## 2026-11-22 — Jira 2.4b, PR 2: the access request (Phase D)

**Built:** four Types on PR 1's building blocks, with **no kernel change**: `resource@1` (approvers, quorum and
contact, `access_admin`'s; calendar and policy, `sla_admin`'s; `quorum_invalid`), `access_request@1` (submit
copies the three and marks `f_submitted_at`; `submit_without_quorum`; one approval per approver; the approval
goal on sla_goal@2; the two counting guards; leftover approvals closed; a fulfilment task on approval),
`approval@1` (decided by its approver, never the requester, while the request is pending; the move at the request
with `on_guard: skip`; the hand-over; unique while pending or approved; canceled by nobody but the request) and
`fulfilment_task@1`. lea holds `access_admin` in the dev-token file. Scenario E (E1 to E8). design.md: "Jira
2.4b, PR 2 as built"; Jira 2.4 is done in the build order.

**Decided in Phase A, each agreed:** the submit with no quorum is refused by an invariant with a kernel-written
marker, not a guard (a guard is judged before the transition's sets, so it never sees the copies); the goal
measures the time to a decision, so `sla_answered` completes it on a rejection (sla_goal@2 consents by reaction
ID; no sla_goal@3); a late decision is `GUARD_FAILED` in the window, `NO_TRANSITION` once closed.

**Tests:** 8 item service tests, 1 typedef test, 2 end-to-end tests with a real worker (two approvals at once,
three rounds; an approval and a rejection at once, three rounds), 1 vitest (the generic Inbox over approval@1).
**Test changes, each agreed, each in a commit of its own:** apps/poc's devusers test expects lea's
`access_admin`; scenario E's access desk is narrowed to its people (unnarrowed, a desk@3 is a portal every
customer lists: C1 failed on a second run, and so did the customer Playwright test) and its resource is named
Wiki (D1 searches VPN in the active category and found E's fulfilment task). The four Type lists grew (typedef,
API, pli 41 → 45). The end-to-end tests accept `guard_false` or `not_in_from` for the second move's skip
(design.md): which one is timing.

**Surprises:** none in the kernel: every Phase B test of the item service passed on the first run of the Types.
The scenario's surprises were all about sharing one namespace across steps and runs.

**Numbers.** `go test -race ./...` green (item service 132 s, workflow suite 342 s). Rebased on main after PR #39
(apps/poc, Requests): apps/poc's vitest 408 and typecheck green. Scenario: twice on a fresh stack, 102 s and
103 s. Playwright: 9 tests in 7.3 s. vitest: 464. The stack ran without the `poc` service, as before (its image
cannot be built here); CI builds it and runs its journeys.

**Open:** the Later list (placement for the other Types, sla_goal@1; groups as readers; excludes; the dropped
reaction). **Next session:** Bonita, or the high Later items.

## 2026-11-22 — Hardening after Jira 2.4: who places Items, and goals no person moves (Phase D)

**Built:** five Type versions, **no kernel change**: incident@3 (placed by desk agents; its goals on sla_goal@3),
reply@2 and note@2 (placed by the kernel: they stay in their incident), comment@3 (likewise), sla_goal@3 (sla_goal@2
placed by the kernel). apps/poc creates incident@3, reply@2 and note@2, and its queue lists incident@2 and @3
together; scenario C, its new step C13 and the web customer test run on the new versions (B keeps @1).
design.md: "Hardening after Jira 2.4, as built" (which versions, why; existing Items keep their version's rules;
the generic web UI creates the latest version).

**Tests:** 1 typedef test (the five files, and every goal reaction the incident uses accepted by sla_goal@3), 4
item service tests (who places each, no hand move of a goal, the older versions unchanged); the five Type lists
grew (pli 45 → 50); apps/poc's queue and request tests. **Test change, agreed, in a commit of its own:** the web
customer test's goals are `sla_goal@3` (kim's incident is made in the web UI, which creates the latest version).

**Surprises:** the web UI moved to the new versions by itself (it creates the latest one), which is what found
the stale expectation. Nothing in apps/poc pinned a goal version.

**Numbers.** `go test -race ./...` green (item service 145 s, workflow suite 330 s). Scenario: twice on a fresh
stack, 104 s each. Web Playwright 9 in 8.3 s. **apps/poc's four journeys passed locally for the first time** (32.5
s), against a poc image served from a bundle built on the host (the real image's `npm ci` cannot run in this
container); vitest 408 and typecheck. CI builds the real images.

**Open:** the Later list: version migration before real use, plat_admin and narrow-at-creation, vacation_request
and ticket placement. **Next session:** Bonita (an exported process, then the packages design pass).

## 2026-11-22 — Hook protocol spike: pli.hooks/v1, and the HR directory out of the worker (Phase D)

**Built:** `pli.hooks/v1`, from the research report's recommendation (protocol now, no package system), trimmed to
what the spike exercises: the request envelope, check and enrich outcomes, error classes as RFC 9457 problems,
Standard Webhooks signing with replay protection, a descriptor at `/.well-known/pli-connector`, and a conformance
test any connector runs (`internal/hookproto`). The HR directory (`hr.manager_of`) left the worker: connstub's HR
server speaks the protocol and decides what the worker's code decided; the worker reaches it as `hr@1`, by
configuration (`--connectors`, `deploy/connectors.yaml`, read again when it changes). CRM, ERP and email stay
compiled in, behind the same `hooks.Connector` interface. **No Type file changed**; the scenario is unchanged.
design.md: "Hook protocol (pli.hooks/v1), as built", with the specified-only parts (action hooks over the wire,
async completion, failure policy per binding), the rerun rule, the alias idea for namespaces, "Packages: bundles,
later" with the five triggers, and "Next step: incidents".

**Kernel changes (three):** (1) a Type's action may name a major, `<connector>.<operation>[@<major>]`, none
meaning 1, and a major the catalog does not declare is refused at load; (2) the worker runs every hook through one
`Connector` interface, compiled in or remote by configuration; (3) the workflow records `connector_unconfigured`,
`connector_unauthorized` or `connector_mismatch` on a check's hold, at once. No item service, schema or proto change.

**Decided in Phase A, each agreed:** plain Go structs and JSON, no proto; inputs by the operation's names, mapped
by position (so connectors never see field IDs); the catalog keeps a declaration of each remote operation, so Types
load with no network; check outcomes keep `correct`; 409 in flight, 422 key conflict; `webhook-id` from the key,
replay answered from the success store; extend connstub rather than a new service; a rerun reuses the key
(the report mints a new one); incidents only for action hooks and enrich hooks without a fallback, specified, not
built; a read-only call without a key uses its request ID. **Actor:** first included, then left out before Phase C
(its own commit): the name is reserved, and Later.

**Tests:** 49 new (signing on the reference library's known answer, the envelopes and the frozen v1.0 fixtures,
the conformance test and its six broken connectors, the HR connector, the remote client's envelope, failure classes,
descriptor checks and enrich, the connectors file and its reload, `@N` in typedef, the connector reasons in the
workflow, and end to end: retries within the budget, the three misconfigurations, HR 1.4 switched to 1.5 while a
request is held). **Test change, agreed, in a commit of its own:** the existing HR tests' setup reaches HR as a
remote connector (`newHR`, `newHRStack`, the mail and leave stacks, the correction-refused test); no assertion
changed.

**Surprises:** the spec has deprecated `v1a` (which the report names) for `v2a`. Go's own resend of a dropped
request stopped being a question once `webhook-id` was stable and replays are answered from the store. Every
existing HR test passed against the remote connector on the first full run. `go mod download all` rewrites
`go.sum` (restored; not committed).

**Numbers.** `go test -race ./...` green in 6m45s (workflow suite with the replay test 335 s, item service 137 s).
Scenario twice on a fresh stack: 103 s each; the HR check over the protocol answered in 224 and 174 ms. Web
Playwright 9 in 7.8 s; apps/poc's journeys 4 in 26.6 s.

**Local workarounds, outside the repository (nothing committed):** Docker Hub answered 429 for the golang and
nginx base images, so they were pulled from `mirror.gcr.io` and tagged with their usual names; Docker builds here
cannot use the HTTPS proxy's CA, so a compose override in the scratchpad served Go modules from the host's cache
and built the web and poc images from sites built on the host; Playwright ran with a local config (excluded from
git) pointing at this container's Chromium. CI builds the real images and runs the pinned browser.

**Open:** the hook protocol's Later items (top of this file). **Next session:** incidents (design.md, "Next step:
incidents"), or Bonita, whose connectors would be written against `pli.hooks/v1` from the start.

## 2026-11-22 — Ventoux 2a: every problem of a Type as data (Phase C)

**Built:** `typedef` reports every problem of a Type, or of a folder, as `Problems` (an `error`):
`{source, path, line, column, severity, code, message}`, 36 codes in design.md's new "Type problems".
Three stages: (a) the YAML, walked against the Type's structs by reflection (every unknown key and wrong
shape, merge keys expanded); (b) each Type, each declaration checked on its own; (c) the Types together,
only once each is sound. ~400 `fail` sites now carry a code, a path and what to do; the old message is
the start of each. design.md: "Type problems" and the Ventoux build order (V1, V2a–c). **No test
written before 2a changed.**

**Surprises:** yaml.v3 stops at the first custom decoder that fails, so it never reported more than one
unknown key: stage (a) needed its own walk. Expression environments declared every field, so one bad
field broke every guard (a cascade): they now declare each ID once and skip missing or reserved ones.
`sla_goal` uses YAML merge keys (`<<: *anchor`), which a naive walk reads as a key. CEL errors span
lines; a message is kept on one line.

**Numbers.** typedef: 55 new tests, all green with `-race`, and every package that runs without
Docker. **Not run here:** the Postgres packages (item service, API, pli, relay, workflow, cmd/itemservice)
need Docker, which this container lacks. Read by hand: their only dependency on typedef's wording is
`TestRegisterType_LinkInverseAgreesAcrossVersions`, whose four names are all still in the new message.
CI decides.

**Open:** the Later entries above (the custom YAML decoders' checks, unreachable once stage (a) runs, were removed
after review). **Next session:** CI on the PR, then V2b (`ValidateTypes`).

## 2026-11-22 — Ventoux V2b and V2c: ValidateTypes and the Type format as JSON Schema (Phase D)

**Built:** V2b, `ValidateTypes`: draft Types checked against every registered version, nothing written,
through the API (`kernel.v1` grew one operation and three messages; `buf breaking` green) and
`pli type validate` (files or folders; one line per problem, `--json`, exit 1 on an ERROR). V2c,
`schema/type.schema.json`, by hand, checked in tests only. design.md: "Ventoux V2b and V2c, as built", the
`ValidateTypes` row in the API table, the V2b row's wording (`pli type validate`), both rows done.

**Kernel changes (two, counted):** (1) `typedef.Check(sources, registered)`: every stage on drafts with the
registered versions' stored JSON beside them; stage (c) run on the registered alone, then with the drafts,
and only new problems reported; a new code, `VERSION_CHANGED`. `ParseDir` now reads its folder and calls it:
one code path. (2) The item service's `ValidateTypes`, a read that reads no Items (exempt from
`policy.Project`, like `ListTypes`). No schema change, no write path touched.

**Decided in Phase A, each agreed:** the API shape (`code` a string, no `valid` bool, `content` a string,
not a write); limits on `sources`, in the handler; customers and agents acting for one refused, no agent
power needed; identical as `RegisterType` decides it (jsonb); old problems of the registered set not
reported, a registered version's named in the caller's namespace; `pli type validate`, singular; the schema
at `schema/type.schema.json`, keys and shapes only plus enums and `required` where typedef has the same
closed rule; the jsonschema library in go.mod, imported by tests only.

**Tests:** 44 new Go tests (typedef Check 13, the schema 8, item service 7, API 9, pli 7) and two scenario
steps. **Test changes, agreed, each in its own commit:** four additive ones in Phase B (`EachHasACase` counts
Check's cases; `ValidateTypes` exempt from `policy.Project`; the API's operation list and `calls()`), and
three fixes to my own Phase B tests in Phase C: an unreadable `{` cannot name its Type; a property named
`required` is not the keyword; the scenario's first run prints one line, not two.

**Surprises:** typedef is more lenient than it looks: `id: 5` and `fields: ~` load, so a schema that is never
stricter takes any scalar for text and null almost everywhere (Later: tighten both together). The stage (a)
walker made a good oracle: the schema test places eleven kinds of value at every position of the format and
asks the walker, then the schema (500+ checks). Only three keys were used by no file in `types/`
(`inverse_max`, the map form of a consent with `fields`): the corpus is one file. A draft can break a
registered version (`leave_balance@2` without the deduction's consent breaks `vacation_request` v2 and v3):
that case drove the "problem on a registered version" design.

**Numbers.** `go test -race ./...` green: workflow suite with the replay test 348 s, item service 162 s.
Scenario twice on a fresh stack: 106 and 103 s. Web Playwright 9 in 9.1 s; apps/poc's journeys 4 in 28.0 s.
Web 464 and apps/poc 408 unit tests.

**Local workarounds, outside the repository (nothing committed):** dockerd started by hand in this
container (it stopped once mid-session and was started again). Docker builds here cannot use the HTTPS
proxy's CA, so a compose override in the scratchpad served Go modules from the host's module cache
(`python3 -m http.server` as a GOPROXY, builds on the host network) and built the web and poc images from
sites built on the host with `VITE_DEV_USERS=1`. Playwright ran with a local config (excluded from git)
pointing at this container's Chromium. CI builds the real images and runs the pinned browser.

**Open:** the three new Later entries above. **Next session:** V1 (action contracts), or Ventoux item 3 (checks
that prove properties), which would add warnings to the same `{path, code, message}` problems.

## 2026-11-23 — Ventoux V1: action contracts (Phase D)

**Built:** every action a hook may name has a contract, one JSON file per operation in `contracts/`,
compiled into the item service and the worker: typed inputs and outputs in hook order, kinds, side effects,
risk, the reasons a check may deny with, a description everywhere, and each input's trust (declared, not
enforced until Ventoux item 4). `contracts/contracts.lock` freezes each published contract's machine digest
(descriptions left out, as agreed); CI refuses a changed or removed line against the base branch
(`contractsfrozen`). A hook of the wrong kind, or reading or writing a field of the wrong type, is refused at
load; every connector's answers are held to its contract at run time; `ListActions` and `pli action list`
serve the catalogue. design.md: "Ventoux V1, as built: action contracts", the V1 row (done), the
`ACTION_KIND` and `ListActions` rows, pointers from the Type and hook protocol sections. No Type file changed.

**Kernel changes (four, counted):** (1) contracts as frozen data: `contracts/` and `internal/contracts`
(format rules, digest, lock checked both ways at start), and the CI freeze step; (2) typedef checks hooks
against the catalogue: `ACTION_KIND` (new code), `WRONG_FIELD_TYPE` on reads and writes, with no cascades;
typedef no longer links the worker's connector code; (3) the worker's catalog is the contracts, with code
attached: compiled-in answers' reasons and every output's type are checked (`connector_mismatch`), and a
remote descriptor must fit the contract; connstub's HR descriptor is the contract; (4) `ListActions` in
`kernel.v1` (one operation, three messages, three enums; `buf breaking` green). No schema change, no item
service write path touched, no new direct dependency.

**Decided in Phase A, each agreed:** per-operation JSON contracts embedded in both binaries, the single
source of truth; the descriptor's format reused (`hookproto.Operation` plus `inputs`/`outputs` order,
`description`, `risk`, `format`, `enum`, `x-pli-trust`); majors as built; the lock with a Go test and the
CI step; the digest covers the machine contract only (the owner's change); the type table and rules,
`WRONG_FIELD_TYPE` reused, `ACTION_KIND` added, no reason code until a Type can name one; run-time checks
for compiled-in reasons and output types; risk as data (low, medium, high); `ListActions` only, users and
agents in a session, exempt from Project; trust declarable now with kind-based defaults; strict rules, with
the fixtures fixed in a commit of their own.

**Tests:** 38 new Go tests (contracts 19, typedef 5, hooks 5, API 5, pli 3, contractsfrozen 1), many
table-driven (44 format rules, 8 single-problem cases with PyYAML positions), and four scenario steps.
**Test changes, agreed, each in its own commit:** (a) Phase B's fixture commit: fixtures that used real
actions as stand-ins now fit their contracts (7 files, no assertion changed); checked by putting the strict
checks in temporarily before the commit. The `duty` fixture's tests still exercise what they were written for:
`stamp` still writes `f_owner` (now an `hr.manager_of` check on a new `open -> review`, the only kind of
action that writes a principal) and is refused on the internal door, and `gate` still holds `open ->
checked` (now `erp.check_credit`, reading `f_note` and a new `f_amount`). (b) `EachHasACase` counts the
contract cases; (c) `TestHR_Descriptor` wants the contract's operation; (d) the API's operation list and
`calls()` gain `ListActions`, whose name is exempt from the "looks like the internal door" check by name
only; (e) Phase C: HR answers `group:hr` instead of `bob` in the two `correction_refused` tests, since the
worker now stops `bob` (not a principal) as `connector_mismatch` before ResolveHold sees it; `group:hr` is a
principal no principal field holds, so ResolveHold still refuses the correction.

**Surprises:** typedef never checked a hook's kind against its action's, and tests used `crm.get_plan` as a
check in hundreds of parses: the strict rules broke 37 tests in 7 files, all fixtures, none of `types/`.
`TestContract_OnlyPublicOperations` refuses any method named like the internal door, "Action" included. The
run-time output check moved a correction test's path, as above. Load's own error message ("publish it with
<action> <digest>") turned out to be the easiest way to write the lock.

**Seen once, not reproduced:** `cmd/connstub`'s tests failed once in Phase C, in a run where several
packages tested in parallel; the output was not kept. Not reproduced in 16 runs on its own (with
`internal/connstub` and `internal/hookproto`), nor in the two full `-race` runs after. If CI shows it, stop.

**Numbers.** `go test -race ./...` green: workflow suite with the replay tests 328 s, item service 150 s.
Scenario twice on a fresh stack: 104 and 104 s. Web Playwright 9 in 8.4 s; apps/poc's journeys 4 in 37.4 s.
Web 464 and apps/poc 408 unit tests.

**Local workarounds, outside the repository (nothing committed):** dockerd run by hand as a background
process (started from an ordinary shell call it stopped twice); Go modules for the image builds served from
this machine's module cache (`go mod download` first; go.sum unchanged; `python3 -m http.server` as the
GOPROXY, builds on the host network); the web and poc images built from sites built on this machine with
`VITE_DEV_USERS=1`, through a compose override in the scratchpad; Playwright through a local config
(excluded from git) pointing at this container's Chromium. Docker Hub did not rate-limit this time. CI
builds the real images and runs the pinned browser.

**Open:** the action contracts' Later items (top of this file). **Next session:** Ventoux item 6
(plain-language readings with witnesses) or item 3 (checks that prove properties, which can read risk).

## 2026-11-23 — Starting work: a proposal (docs only)

**Built:** no code. `docs/plans/starting-work.md`, a proposal in two parts. (1) Who may start a workflow: agents
need the `create` power for every create (today any write power is enough), then an optional `start` block on a
Type, `{in: [container Types, top], allowed: [...]}`, judged by `policy` like a transition's `allowed`, every
entry reading the container; the latest version's block governs creates of every version; no block, no change.
(2) Triggers: source contracts in `contracts/`, intakes on the Type with one verdict per event type (`create`,
`correlate`, `create_or_correlate`, `signal`), inbound `pli.hooks/v1` only through a connector, an inbox under
the erasure rule, an `intake:` principal named in `start.allowed`, intake-written fields untrusted at load,
`external_keys` deciding create-or-correlate races, timer starts as create reactions on a container's acting
timer, signals specified only. Mapped to BPMN start and catch events, Bonita and Camunda. Phase A's 22 answers
are recorded in it.

**Kernel changes:** none (proposed: six steps; three need a second OK: the inbox, `external_keys`, the signal
key table, with the `intake:` principal and `Internal().Create`).

**Surprises:** `item.Writes` lets any write power create, though the brief assumed `create` was needed.
`container_creator` existed only in `except`. A caller picking an old Type version dodges any rule added in a
later one: the same hole placement has (to close as its own step). The vendor docs were unreachable from this session,
so the Bonita and Camunda points not already in the connectors report are marked UNVERIFIED.

**Checked:** the claim that the `create` power touches no existing test, by running it: in a throwaway worktree,
`Create` required `create`, and `internal/itemservice`, `internal/api`, `cmd/pli` and `internal/workflow` passed
unchanged (dockerd started by hand, as before). Not committed; the worktree was removed.

**Review (2026-11-23):** one gap filled (a reopened Item's correlation key stays closed; events keep going to
the newer case), and the eight open questions decided: reasons stay in the message; placement gets the same
latest-version rule as its own step; an intake on a Type without a `start` block is refused at load; erasure
reads an `x-pli-personal` flag on source outputs; a correlate on a frozen field fails; an intake kill switch is
required before inbound; a mapped external user must pass part 1 too; the UNVERIFIED precedents are checked
before any is relied on.

**Open:** none in the proposal; each build step comes back for its own OK. **Next session:** step 1 (the
`create` power), Phase A.

## 2026-11-23 — Ventoux V5: stories in simulated time (Phase D)

**Built:** `internal/story`: a story is a YAML walkthrough of one or more Types (the people and their roles,
a fake for every action from its contract, a timeline of one-line steps, and the **whole** expected timeline),
run as an ordinary Go test in simulated time. `Parse`, `Check`, `Run`, `Compare` (and `Report`, `Renderer`)
are the four exports item 6's witnesses will reuse. Six stories in `stories/`: vacation (approve, expiry,
refund, HR outage and denial) and incident (reopen on a reply with a weekend pause, breach). `make stories`
(`STORY=vacation/approve` runs one). design.md: "Ventoux V5, as built" (the grammar, how a run differs from
production, the two harness bugs) and the V5 row. Each story's `author:` says "approval pending" until the
owner reads it.

**Kernel changes (four seams, counted; no production behaviour changed):** (1) `itemservice.Options.Clock`,
tests only: a write is stamped with it, and `today` follows it; a guard test keeps it out of production
code, another keeps `internal/story` out of every binary; (2) `hooks.Connectors.WithConnector`, the Go
binding for any connector, ahead of remote and compiled-in ones, held to the contracts like every connector;
(3) `workflow.Register`, pulled out of `NewWorker`, which uses it; (4) `api.Refusal`, the API's refusal
reason for an item service error, so a story names a refusal as pli does.

**Decided, each agreed:** Phase A's seven answers (YAML with one-line strings, `stories/` at the top level,
Temporal's in-process test environment with a real item service, fakes from the contracts checked at load
and at run time, the whole timeline with a multiset per instant, reads as a person, the first six stories,
tests only). Phase B: the fourth seam; `reaction_skipped` lines hidden unless `show_skipped: true`
(nothing is lost: a reaction that should have applied leaves its effect's line missing); a window form
(`between TIME and TIME`) for timing the kernel does not promise, used only by the HR outage.

**Tests:** 59 new Go test functions (story package 45, the four seams 14), with 94 table cases (parse 24
problems, check 40, the renderer 30 events) and the six stories as subtests of `TestStories`; the two risks
and the gap are tests of their own. **Test changes, agreed, each
in its own commit:** in Phase B, the window form (parse, compare, the outage story); in Phase C, two of my
Phase B tests: the continue-as-new test runs refund.yaml without its read at the withdrawal (the injected
failures delay the refund past it, by design), and the name-clash case reads `inc`, so the clash is its
only problem.

**Surprises:** the dump of a real run showed every goal move writing two or three `reaction_skipped`
records, hence `show_skipped`. Temporal's test environment never reports a child's continue-as-new as a
completion: it reruns the child, so a start while running is the count. An update's answer can arrive
before the workflow task that handled it has scheduled its activity: settle looked too early once in a full
`-race` run (a read saw the balance before its deduction); it now round-trips the main loop and looks
again. An update to a workflow that completes first is never answered there (seen once as a 30 s stall);
a delivery now notices the completion. The harness's own correctness is a matter of counting the
kernel's activities exactly: a workflow interceptor counts them when scheduled, a wrapper while an
attempt runs, the cancel listener when one is canceled unstarted.

**Numbers.** `go test -race ./...` green: workflow suite with the replay tests 331 s, item service 155 s,
stories 17 s. Each story 0.3 to 0.9 s under `-race`; the expiry story's nine simulated days 0.26 s.
Stability: the story package 30 times under `-race`, no failure and no race (180 story runs; the slowest,
incident/reopen_on_reply, 0.84 s at most). Scenario twice on a fresh stack: 104 and
104 s. Web Playwright 9 in 15.0 s; apps/poc's journeys 4 in 40.1 s.

**Local workarounds, outside the repository (nothing committed):** dockerd started by hand, three times (it
stopped mid-session and with each worker restart); Docker Hub answered 429, so the golang and nginx base
images came from `mirror.gcr.io`, tagged with their usual names; Go modules for the image builds served
from this machine's module cache (`go mod download` first; go.sum unchanged; `python3 -m http.server` as
the GOPROXY, builds on the host network); the web and poc images built from sites built on this machine
with `VITE_DEV_USERS=1`, through a compose override in the scratchpad; Playwright through local configs
(excluded in `.git/info/exclude`) pointing at this container's Chromium. CI builds the real images and runs
the pinned browser.

**Open:** the stories' Later items (top of this file). **Next session:** the owner approves the six
stories; then Ventoux item 6 (plain-language readings with witnesses), which reuses `Parse`, `Check`,
`Run` and `Compare`.

## 2026-11-23 — Starting work, step 1: the `create` power (finished)

**Built:** an agent session needs `create` for every `Create`; another write power is no longer enough, and every
other write keeps the v1 rule. `checkCreatePower` runs right after `checkWritePower`: scope (`NOT_FOUND`), then the
power, then the agent write rule (`NEEDS_APPROVAL`). Refusal: `ErrForbidden`, "this agent session may not create";
over the API `PERMISSION_DENIED NOT_ALLOWED`, byte for byte a read-only session's. Create reactions are unchanged.
design.md, powers.go and starting-work.md updated. PR #55.

**Decided (Phase A):** only `Create` needs `create`; reaction creates follow `creates:` (now a test); no test for
replaying a pre-change key; the two half-true test comments gain "except a create" (own commit); the plan corrected:
the item service's message names the reason, the API returns only `NOT_ALLOWED` (step 2's reasons too).

**Tests:** 8 new (item service 6, API 1, pli 1); 6 failed for the right reason before the check, 2 guards passed. No
existing test changed, confirmed by running the whole suite with the check in a throwaway worktree in Phase A.

**Surprises:** the plan promised the refusal's message over the API, but `apiError` sends only the reason's fixed
text. The stories (added after the plan's check) start no agent sessions.

**Numbers.** `go test ./...` green: workflow 322 s, item service 70 s.

**Local workarounds (nothing committed):** dockerd started by hand three times (it stops between runs);
`postgres:17-alpine` from `mirror.gcr.io` after Docker Hub answered 429.

**Open:** the two Later items above. **Next session:** step 2 (the `start` block), Phase A.

## 2026-11-24 — Ventoux V6: plain-language readings, each with a witness (Phase D)

**Built:** `internal/reading` renders a Type as numbered sentences under sections a person reads in order, from
the Type and the contracts it pins: 35 kinds of sentence and their clauses (83 parts, a closed list), a key
defining only the terms used, the field list, the actions from their contracts, and what no reading covers.
Conditions read in words in a closed subset, anything else as what it reads. `ExplainType` (a registered version
or a draft) and `pli type explain`. Two readings approved by the owner, `readings/vacation_request@3.txt` and
`readings/incident@3.txt`, compared exactly. design.md: "Ventoux V6, as built" (the wording of every kind), the V6
row, and the empty-`allowed` sentence corrected ("any staff member who can see the Item, never a customer").
Roadmap: V5 and V6 under Done.

**Kernel changes (three, counted):** (1) `internal/reading`; (2) `ExplainType` in `kernel.v1` (one operation, six
messages; `buf breaking` green); (3) `pli type explain`. No schema, write path or item service change; the API's
draft checks for `ValidateTypes` became shared helpers, with no change in behaviour.

**The story harness grew (counted apart, not kernel):** two steps, `places ITEM in ITEM | at the top level` and
`narrows ITEM to person, ...`, and a read of a rollup's count, `sees ITEM with ROLLUP.KEY = N`. **And a fix the
witnesses found:** naming an Item the kernel created filled only the harness's map from Item to name, so a step
naming one went to no Item at all: a read failed `NOT_FOUND`, a write was refused `NOT_FOUND`, and `cannot see`
passed whatever the person could see. **`kim cannot see first` in `stories/incident/reopen_on_reply.yaml` was
vacuous before; it is a real check now, and still passes.**

**Decided, each agreed:** Phase A's defaults (all kinds now; field IDs in code font; expressions as a closed
subset, held to the kernel's own guard evaluation, plus one witness through `Run` of a false guard; the approved
readings checked in, no `-update`; the two story steps; `ExplainType` with drafts, users and agents, customers
refused, exempt from Project; the empty-`allowed` fix; the web UI later). Phase B: the operation lists gain
`ExplainType` (additive, own commit); the rollup read as a third grammar addition; "Only the hook `h` may move it"
kept. After the rebase onto #55 (the `create` power): no reading changes, since the power is a session's and no
Type says anything of it; the key's agent entry stays true.

**Tests:** 32 new test functions (reading 15, story 5, API 6, pli 6), with the eleven witnesses as subtests (21
fixture Types), 28 condition cases and 3 cases of what a rule reads, and the cite and well-formed tests over the 50 Types of
`types/` and the fixtures. **Test changes, agreed:** the two operation lists (`TestContract_OnlyPublicOperations`
and `calls()`) gain `ExplainType`. No other test changed.

**Surprises:** the witnesses caught two errors in my Phase A wording: an acting timer at an instant sets the
field's instant, not the time it runs, and runs at once when the Item enters after it ("When it is in S and the
instant in F has passed"); and guards come before `allowed`, so `nobody` reads "Only a reaction moves it". The
harness bug above. A check's 15-minute budget ran out before a fake's outage ended, the first time I wrote the
check witness. Capitalising every sentence turned "sla_goal's" into "Sla_goal's", which the approved incident
reading caught: only my own phrases are capitalised now.

**Numbers.** `go test -race ./...` green: workflow suite with the replay test 335 s, item service 158 s, reading
12 s, stories 25 s. The story and reading packages 10 times under `-race`: no failure, no race (116 s and 44 s).
After the rebase onto #55: reading, story, API and pli green under `-race`. Scenario twice on a fresh stack: 105 and
104 s. Web Playwright 9 in 9.4 s; apps/poc's journeys 4 in 38.3 s; web 464 and apps/poc 408 unit tests.

**Local workarounds, outside the repository (nothing committed):** dockerd started by hand (it stopped between
phases, again); Go modules for the image builds served from this machine's module cache (`python3 -m
http.server` as the GOPROXY, builds on the host network; go.sum unchanged); the web and poc images built from sites
built on this machine with `VITE_DEV_USERS=1`, copied to scratch contexts (their `.dockerignore` leaves `dist`
out), through a compose override in the scratchpad; Playwright through local configs (excluded in
`.git/info/exclude`) pointing at this container's Chromium. Docker Hub did not rate-limit. CI builds the real
images and runs the pinned browser.

**Open:** the readings' Later items (top of this file), the first two found by readings. **Next session:** the
owner decides on the incident's moves; then starting work, step 2 (the `start` block), which will need a kind of
sentence of its own.

## 2026-11-24 — ValidateTypes' limits raised to 200 sources and 1 MiB (an ORI story)

**Built:** one `ValidateTypes` or `ExplainType` call takes 1 to 200 draft sources and 1 MiB of content in all (was
50 and 256 KiB): types/ was at exactly 50 files, so build_step@1 could not be validated. Over either limit, one
refusal (`INVALID_ARGUMENT` / `INVALID_REQUEST`, field violation on `sources`) names both limits and the request's
own count and size, in MiB rounded up to one decimal: "give 1 to 200 sources, 1 MiB in all; this request has 201
sources, 0.1 MiB", so a tool can split a request without guessing. The proto comment changed with it (comment only:
`buf breaking` green); gen/ and web/src/gen regenerated. design.md (its own commit, once the owner approves the
wording): the two V2b places, with the owner's note, and a clause at "Messages are fixed".

**Decided (Phase A and B):** the limits are the owner's (200, 1 MiB); the tests that pin them changed as agreed,
their values and messages only; `TestValidateTypes_TheRepositoryTypes` is now a guard (types/ fits in one call)
instead of pinning 50 files; ExplainType's argument test pins the same limits (PR #56 shared `draftSources`), so it
changed too; the refusal's text is a promise, so it has a test (`TestValidateTypes_LimitsRefusalSaysTheSizes`, for
both operations). No server-wide read limit: the check stays in the handler. No batching in pli.

**Tests:** `go test ./...` green (workflow 367 s, item service 369 s). 3 changed (ValidateTypes limits and repository guard, ExplainType arguments, pli over-the-limit), 1 new.
The new allowed edges (200 sources, 1 MiB, one source of 1 MiB) and every text assertion failed against main's code;
the refused edges and the guard could not fail before the change (the old limits refuse more).

**Surprises:** PR #56 landed mid-step and moved the limit checks into `draftSources`, shared with ExplainType,
whose test pinned 51 sources. "Never a value" in a field violation (design.md) was written against leaking what
someone typed; a request's own count and size are not that, and the new clause says so. Counting the bytes now comes
before the name check, so an oversized request with a nameless source gets the size refusal (same code and field).

**Open:** none. **Next session:** build_step@1 rebased onto this (its `TestPli_Types` count is its own).

## 2026-11-24 — Starting work, step 2: the `start` block (Phase D)

**Built:** an optional `start: {in, allowed}` block on a Type (plan part 1, 1.1 to 1.8, without intakes). `in`
names the container Types (any version) or `top`; `allowed` takes `role:`, `principal:user:`, `field:` (the
container's), `container_creator` or `nobody`. The **latest registered version's** block judges a create of every
version. `typedef` parses and checks it (stage 2 and 3, new code `START_NOT_IN`; a Type named `top` is
`RESERVED_NAME`); `policy.MayStart` judges `allowed`, sharing one matcher (`matchesEntry`) with `MayTransition`;
`Create` checks it in its transaction, after everything that answers `NOT_FOUND`; readings gain *Who starts it* and
a closing line. One Type uses it: **incident@4** = incident@3 + `start: {in: [desk]}`. design.md: "Starting work,
step 2, as built", the V6 table row and counts, the problem-code rows. starting-work.md: step 2 marked built, 1.6's
order corrected, 1.3's notes marked deferred.

**Kernel changes (four, counted):** (1) the `start` block in `typedef` (parse, load checks, `START_NOT_IN`,
`Start.Admits`); (2) `policy.MayStart` and the shared `matchesEntry`; (3) the check in `Create`, with a
`max(version)` lookup per create in its transaction; (4) the readings' *Who starts it* and the closing line. No
proto, schema, migration or go.mod change; the JSON Schema gained `start`.

**Decided:** Phase A's answers: no proto change (the "governs earlier versions" / "loosened" notes deferred, so
`StartNotes`, built in Phase B, was removed at review: production code only tests would call); test-only Types with
unique IDs in the item service (Types are global, and registering incident@4 there would judge other tests'
incident creates by test order); the order of checks (NOT_FOUND first, then `in`, then `allowed`, then the rest:
corrects plan 1.6); a typed `StartRefusedError` whose message names the reason and the Type only; the latest version
read per create, never cached; `nobody` refuses customers too, and `own`/`shared` naming such a Type is `CONFLICT`
at load (fail closed); `field:` matches as in transitions (a field matches only the principal it holds); no change
to `RegisterType`. **Decision 1, option B (owner):** incident@4 gets only `in: [desk]`. `allowed: [role:desk_agent]`
plus the latest-version rule would have shut every staff member without the role out of opening incidents of any
version, and in scenario B the person who opens an incident is the employee with the problem (ana), not a desk
agent; `allowed` is proven on test-only Types instead. Phase D review: the readings say "these rules apply to",
not "hold", since "a hold" is a defined term.

**Tests:** 28 new test functions (typedef 9, policy 2, item service 14, API 1, pli 1, reading 1) and one witness
(`start`, 8 parts) with 5 fixture Types; table cases: 22 stage-2 problems, 11 stage-3 problems and 8 that load,
13 `Admits` rows, 35 `MayStart` rows, 70 combinations held to `MayTransition`; scenario B2 gained one refusal.
**Existing tests changed, each in its own commit, agreed:** a `START_NOT_IN` case in `dirCases`;
`TestPli_Search_NotEmpty` puts its incidents in a desk; the closing line in two layout tests and the two approved
readings; `cite_test` requires `start` to be cited; `TestReadings_TheApprovedOnes` gains incident@4. In Phase D:
the list/count expectations (incident@4 in `TestParseDir_ReadsTheRepositorysTypes` and
`TestListTypes_EveryVersionInOrder`, 51 in `TestPli_Types`); my own incident@4 reading and witness line for "apply
to"; one new test pinning the key's "a hold" as the noun. After the rebase onto the limit raise,
`TestValidateTypes_TheRepositoryTypes` and `TestPli_TypeValidate_Clean` passed unchanged.

**Surprises:** the key's "a hold" matched the verb ("these rules hold", "hold the same"), putting the term in keys
of Types without holds (fixed, pinned). A create's placement check ran before the session's scopes, so an agent
outside them was told `PLACEMENT_NOT_WRITABLE` before `NOT_FOUND`; it now follows them. Types being global made
"latest version governs" a test-isolation question in every package that registers Types piecemeal. The race test
(a create judged by `field:` racing an update of the container's field) failed in every round against the stub:
without the lock the create always committed after the update.

**Numbers.** `go test ./...` green (25 packages; workflow 320 to 340 s, item service 75 to 100 s). Race test
`-count=20`: green, 0 to 3 creates of 20 won per run, never one after the update; `TestStart_` `-race -count=10`
green; `TestMayStart_SharesMatchingWithMayTransition` `-count=20` green. `go test -race -count=1 ./...` after the
rebase: green, 25 packages (the timing margin under Later did not show). Scenario on the compose stack after the
rebase: passed, whole, in 104 s (run word run5da6bb08), the new B2 refusal included. Web Playwright 9 in 9.1 s;
apps/poc journeys 4 in 29.6 s.

**Local workarounds, outside the repository (nothing committed):** dockerd started by hand (twice: the container
restarted mid-Phase C); Docker builds cannot verify the sandbox's TLS proxy, so the Go images built with a
`GOPROXY` served from this machine's module cache (`python3 -m http.server`, builds on the host network,
`GOSUMDB=off`), and the web and poc images as nginx images over sites built here with `VITE_DEV_USERS=1`, through a
compose override in the scratchpad; Playwright could not download its browser, so local configs (in
`.git/info/exclude`) use the installed Chrome (`channel: chrome`). CI builds the real images and runs the pinned
browser.

**Open:** the Later items above. **Next session:** the owner approves the three readings (incident@4 new,
incident@3 and vacation_request@3 with the closing line); then step 2b (the latest-version rule for placement).

## 2026-11-24 — Bake-off: the approvals pilot on Camunda 8.10 and on the kernel (a spike, no kernel change)

**Built:** `spikes/camunda-bakeoff/` for `docs/plans/camunda-bakeoff.md`, run without waiting for answers. The
same purchase approval (manager, then a finance queue from 1,000; an agent prepares, never decides) and an
access request, twice: on the kernel as three Types in YAML with the agent as a script over the agent session
API, and on Camunda 8.10 Self-Managed (the official lightweight Compose setup with H2, authorizations on, BPMN,
a DMN table, forms, the AI Agent Task in its own process and connectors runtime). One deterministic, gullible
model stub and the repo's `connstub` serve both. `scripts/run-all.sh` runs the four tests from clean in about
ten minutes; the evidence is in `results/`. The result is `docs/research/camunda-bakeoff.md`. **No kernel code
changed; nothing on `main`.**

**Result:** the report recommends **Camunda plus a layer of the kernel's ideas** for the pilot. Camunda, hardened,
refused all 17 of the compromised agent's attempts; with its default connectors role 5 succeeded, one an order
with no approval. The kernel refused 26 of 29; two got through (editing a draft before its owner submits it;
planting a rejection reason), and the Temporal dev server let a hand-started workflow place an order for a
purchase still waiting on its manager. Camunda's Tasklist was the inbox for four small forms; the kernel's had
to be scripted. The kernel is about 185 MB idle and up in 7 s against 1.3 GB and 48 s, and free in production
where Camunda needs an Enterprise licence.

**The review changed the answer.** The draft recommended the kernel. A reviewer agent that built nothing argued
Camunda's case and tested both running systems: it found the audit log knows who really submitted a request
(built into the processes: the identity hole closed), that "nothing to build" for the inbox was wrong without
forms (built), and four kernel-side holes the first attack list never tried (each re-run and confirmed before
it went in the report). Its notes and what changed are a section of the report.

**Surprises:**
- `pli type explain` found a hole in my own Type ("anyone who can see it ... narrows who sees it"), and the load
  check refused my first draft (a move cleared a field its state froze). Both did their job on a Type written
  in a hurry by someone who had not written one before.
- The kernel's checks did not find the holes that mattered: open drafts and the planted reason are things the
  Type format cannot say, so no reading mentions them.
- On Camunda, the right to start a process is the right to start it at any step ("start instructions"); the
  permission a worker needs to finish its job is the one that rewrites the process's variables; a misspelled
  variable deploys and stops the first real request with an incident. Starting by message, a called process for
  the agent, and a task-worker role without COMPLETE for candidates were the fixes.
- Camunda's AI Agent connector streams from its model and wants OpenAI's separate usage chunk; FEEL's
  `string()` of a context is not JSON. Stub fidelity, not findings.

**Assumptions** are numbered in the report (17). The two that shaped the work: no `ANTHROPIC_API_KEY`, so the
live-model run was skipped; the roadmap's "A pilot candidate: approvals" and "Pilots serve generality" were
merged to main during the run and the run did not read them, so the brief's scenario was taken as the pilot and
its purpose as delivering approvals. Read with the roadmap (a pilot is a test of the kernel, not its purpose),
the report's answer is to the narrower question; its assumption 2 says so.

**Not done:** Camunda's Modeler, Copilot (needs a model key), linter, process tests and task listeners; a
production-like Camunda; the kernel's stories for the spike's Types; timers; load.

**Numbers.** `go vet ./...` and `go test ./...` green (25 packages; the spike adds one small test package for
the stub). Both rounds took about an hour and a half on the session's clock, inside the one-day box.

**Local workarounds (nothing committed outside the spike):** dockerd started by hand; one Docker Hub pull
failed and worked on the retry; the kernel's programs ran on the host from `go build` (image builds cannot use
the session's proxy); docs.camunda.io was unreachable, so Camunda's docs were read from `camunda/camunda-docs`
on GitHub; BPMN diagrams generated with `bpmn-auto-layout` in the scratchpad.

**Open:** the six Later items at the top of this file, the first two before anything else is built on the
kernel. The roadmap says a pilot is a test of the kernel: then it runs on the kernel, after or with its gaps 1
to 5; the report's Camunda answer is for delivering approvals to real people today. Not compared: the same
approval as plain Temporal code (the report's last paragraph says what that would need). **Next session:** the
owner's decision on the report; otherwise starting work, step 2, as before.

## 2026-11-24 — Bake-off 2: the kernel, or each process written on Temporal (a spike, no kernel change)

**Built:** `spikes/temporal-bakeoff/` for `docs/plans/temporal-bakeoff.md`, run without waiting for answers,
on top of the first bake-off's spike (reused by path). The purchase approval and the access request written
directly on Temporal in Go (updates with validators, a query, activities, Postgres for the inbox and audit, a
small API with the same dev tokens), in two steps: the purchase by me, the access request by a separate agent
given only the step-1 code and the business's five-sentence request. The kernel's access request was rewritten
the same way for symmetry. Measures: the marginal cost of a process, the CFO change with requests in flight
(`workflow.GetVersion`, and a replay checker), what each process rebuilds, and the compromised-agent lists per
process, before and after hardening. `scripts/run-all.sh` repeats them in about five minutes. The result is
`docs/research/temporal-bakeoff.md`. **No kernel code changed; nothing on `main`.**

**Precondition not met:** the brief wanted "an effect re-checks committed state" fixed first. It was not on
main; as instructed, the kernel's forged-workflow start is reported as a known hole and kept out of the
comparison.

**Result:** a second process was 252 lines of Go against 50 of YAML (comments and tests left out); a change
reached requests in flight on plain Temporal and none on the kernel; plain Temporal built the inbox and a
binding claim the kernel lacks. On safety, neither side was safe as built and both could be made so: the
kernel's two holes (drafts the agent can edit; a planted rejection reason) are in every Type and close with two
to five lines of YAML per Type; plain Temporal's (the agent exhausting a workflow's 2,000 updates so no decision
lands; a failed order left reading "approved") were in both processes and took 137 lines to fix. The report
recommends plain Temporal with a shared approvals package for the next few developer-written processes, and
keeping the kernel for what plain Temporal leaves to every author, after it closes the listed gaps.

**The review changed the answer again.** My draft called plain Temporal the safer side and said no Type could
close the kernel's holes. The reviewer broke plain Temporal twice in ways my list never tried, and showed the
kernel's holes close in YAML; I reproduced both before accepting them. It also showed the "blind" builds were
less blind than I claimed (the attack script was reachable; the kernel rewrite is nearly my own Type).

**Surprises:** Temporal's JSON converter drops struct fields tagged `json:"-"` on the way to an activity (the
mocked workflow tests passed; the first real run wrote nulls). Temporal's 2,000-update cap is an attack surface
for anyone allowed one update. A careless change to workflow code fails at read time, not deploy time; the
replay check is the only warning. The kernel quietly gave four things plain Temporal had to discover: a limit
one actor cannot exhaust, retries that never give up, safe resubmission, a record past retention.

**Assumptions** are numbered in the report (9). The session was paused about eight and a half hours mid-run;
active work was about 75 minutes, inside the half-day box.

**Numbers.** `go vet ./...` green; `go test ./spikes/...` green (the two workflows' tests, 18 in all, in
Temporal's test environment).

**Open:** the two new Later items above, and the first bake-off's, the first two (old versions; the worker's
trust in its input) still first. Starting work, step 2 (the `start` block) was merged to main during this run
and not used here: it would replace the access Types' "only inside a system" invariant with `start: {in:
[system]}`, and could stop an agent asking in its own name; it does not retire an old version. **Next session:**
the owner's decision on the two reports.

## 2026-11-22 — build_step@1: how a step of this repository is built, as a Type (Phase C)

**Built:** `types/build_step.v1.yaml`, the Type of `docs/plans/build-step-type.md` with the owner's
decisions: only a maintainer creates a step (`f_owner` is `role:maintainer`'s, invariant
`owner_missing`); every approval is `field:f_owner`, which never matches an agent; frozen fields per
state, with `f_questions` frozen once the step leaves answering (not in dropped), `f_pr_url` from
in_review, `f_journal` in done; six X->dropped lines. Tests first: `internal/api/buildstep_test.go`
(approved), 7 tests and 41 subtests; then an expectation update for a new Type in the three tests that
list every Type, before the YAML. The plan's tracker namespace corrected to `acme`. **No kernel, schema
or API change**, and no test written before this step changed except those three expected lists.

**Surprises:** none in the kernel: every rule the Type needs already existed, and the tests passed on
the first load of the YAML. The agent-session rules mattered for the tests: a fresh session with no
narrowing read creates at the top level without `NEEDS_APPROVAL`. `autonomous-loop.md` named no
tracker namespace, so only `build-step-type.md` needed the `dev` -> `acme` correction.

**Numbers.** `go vet ./...` clean; `go test ./...` green with Docker (the whole repository).

**Open:** the design.md section "build_step as built", whose wording goes to the owner first; the
three Later entries above. **Next session:** the design.md section, then the PR; the owner moves the
tracker stack (`~/code/ori-tracker`) forward and loads build_step@1; the first real build_step is the
next step picked from the tracker after that.

**Rebased onto main 2026-11-24.** Written on 2026-11-22; it sits here, after later entries, because
the branch waited for the draft limits to be raised (PR #58): with build_step@1 the repository's Types
passed 50 files, the most one `ValidateTypes` call took then.

## 2026-11-24 — Bake-off hole 1: an effect re-checks what caused it

**Built:** the rule merged this morning (design.md, "Architecture style", PR #65), with the attack that
found it as its first test. Before an action hook reads its inputs or calls its connector, the
`RunAction` activity has the item service confirm the cause the workflow named
(`Internal().ConfirmCause`, `internal/itemservice/cause.go`): the Type and version are the Item's, the
hook is its action hook, the event is the one the hook is bound to (`lifecycle.BoundTo`, a pure
function), an acting timer and its run are the state's own, a timer at an instant with `sets` has
written for that instant, and the idempotency key is the one the kernel derives. A refusal is final
and is recorded once on the Item as `hook_failed` / `cause_not_confirmed`. The action's own records
(`hook_ran`, `hook_failed`, the write-back) confirm their cause too. design.md has the whole of it
under "An effect re-checks what caused it, as built".

**Kernel changes counted:** one method on the internal door, one reason code, one worker error type,
one case in the workflow's `actionFailure`. 448 lines of production code in 7 files, then a small
commit that gives the idempotency keys one definition (`lifecycle.HookKey` and three more), used by
the workflow, `ConfirmCause` and `ActAt`. No proto, schema, migration, dependency, contract or
hook-protocol change; no production code that exists only for tests.

**Decisions (the owner's, Phase A):** all my defaults. The door is one new internal method, called
first in `RunAction`. The Item need not still be in the state: the check is "this entry really
happened". Checks are unchanged (already safe); enrich is not in this step. The refusal is the
existing `hook_failed` with the new reason `cause_not_confirmed`, which he OK'd by name; payload
`{hook, reason}` only. The key must be the kernel's. The action's own records confirm their cause.
What stays open goes on the roadmap's list. He then OK'd the list of 15 tests before any code.

**Tests:** `internal/workflow/cause_e2e_test.go`, 15 tests, written first: 11 failed, 4 pinned what
holds today (a retry confirms again and an action runs after its Item moved on; three residues).
None was changed afterwards; no existing test was changed. Plus a unit test of `BoundTo` (12 cases).
The first test is the bake-off's attack through a real Temporal server: a workflow started by hand
with a forged `Carried`. His list is covered: the forged start, a cause on another Item, a wrong
Type version, a cause that is not the hook's state entry, a real entry still running after the Item
moved on; and made-up keys, a retry, recorded once.

**The bake-off's own script, re-run** (`spikes/camunda-bakeoff/scripts/test2-sidedoors.py`, its own
stack, unchanged): "closed ... the purchase is in state manager_review and the purchase system
received nothing"; the purchase carries one `hook_failed` by `hook:purchase:place_order`, reason
`cause_not_confirmed`. `results/` is left as the record of the first run.

**Surprises:**
- The key was the bigger hole. The workflow's input carried the idempotency key, so for a purchase
  really approved, a forged activity input with two made-up keys placed two more orders. Reading
  the cause alone would not have closed that.
- A claim of mine in Phase A was wrong, and Phase B's tests showed it: confirming the cause does not
  stop a forged `hook_failed` that names the hook's true entry. It is pinned as a residue.
- A wrong, unregistered Type version in a hook's input was a retryable error before: it would have
  retried until its budget ran out. It is now refused at once.
- Order mattered in `HookRan` and `HookFailed`: the new check first ran before the existing shape
  check and changed which error an older test saw (`TestHookRan_IsChecked`). The shape check goes
  first; the test is untouched.
- The pause: the coordinator's go-ahead for Phase B came before the owner had answered Phase A, so
  the first test commit was pushed on provisional answers. His answers were the same, and nothing
  had to be redone.

**Numbers.** `go vet ./...` clean; `go test ./...` and `go test -race ./...` green (28 packages; the
workflow package about 330 s, with its replay tests; the two thin-margin tests passed). The new tests
under `-race -count=10`: no failure. The check costs about 0.25 ms per side-effecting action (p50
0.25, p95 0.36, p99 0.46 ms over 3,000 calls on a local Postgres, next to 0.27 ms for the existing
`HookInput` read), measured with a throwaway timing test: `cmd/bench -invoices` measures forced
timeouts, not this call. The scenario passed on the stack; web Playwright 9, apps/poc journeys 4.

**Local workarounds, outside the repository (nothing committed):** dockerd started by hand, each time
the container restarted; the stack through a compose override in the scratchpad (the Go programs
built on this machine and copied onto the distroless image, since Docker builds cannot use the
sandbox's proxy; the web and poc sites built here with `VITE_DEV_USERS=1` and served by nginx);
Playwright through local configs (in `.git/info/exclude`) that point at the installed browser. CI
builds the real images and runs the pinned browser.

**Open:** the "Left open by bake-off hole 1" items at the top of this file. **Next session:** the
owner's choice among the roadmap's Track 1 items 2 to 6; locking down Temporal has no step yet.
