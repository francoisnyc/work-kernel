# Task inboxes: TASKANA (KADAI) and Polyflow

2026-11-23. Read for the approvals pilot (`docs/roadmap.md`, "A pilot
candidate: approvals"): what two open-source task managers built on or
beside Camunda do, and what the kernel should borrow. Written from the
projects' READMEs and documentation pages; claims not found on a fetched
page are marked UNVERIFIED.

## TASKANA, continued as KADAI

**What it is.** A task management component, Apache 2.0, embedded in an
application or run standalone. It manages human tasks, not processes: the
process logic lives in an engine (Camunda, through an adapter). It adds
workbaskets and classifications to distribute and monitor many tasks across
an organisation. Components: a Java library, a REST API, and three Angular
apps (workplace, admin, monitor).
([TASKANA](https://github.com/Taskana/taskana))

**Status.** The TASKANA repository's last push was 2024-11-08. KADAI
([kadai-io/kadai](https://github.com/kadai-io/kadai), by envite consulting,
one of TASKANA's two developers) has the same components, concepts and
extension points under `io.kadai`. That KADAI is TASKANA's continuation is
our reading; neither page says so. UNVERIFIED.

**Concepts.**

- **Workbaskets.** A task belongs to exactly one workbasket, a queue that
  says who may work on it: personal, team or topic. A workbasket separates
  the permission or skill a task needs from the person who completes it.
  Task routing (an extension point) picks the workbasket.
- **Workbasket permissions**, per user or group, one flag each: READ (the
  workbasket's existence), READTASKS (query its tasks without naming it),
  OPEN (query it by name), EDITTASKS (update, claim, complete), APPEND
  (create or transfer in), TRANSFER (move out), DISTRIBUTE (send to set
  targets), and twelve custom flags the library never checks.
  ([Security and permissions](https://kadai-io.github.io/kadai-doc/docs/user-guide/core-concepts/securityAndPermissions))
- **Roles**: USER, TASK_ADMIN, BUSINESS_ADMINISTRATOR, ADMINISTRATOR,
  MONITOR, and TASK_ROUTER, which creates tasks in any workbasket without
  read access, meant for automated scripts.
- **Classifications**: the kind of a task, carrying defaults such as service
  level and priority; per domain, with a master fallback.
- **Object references**: each task points to the business object it is
  about, in another system (one primary, several secondary); attachments
  describe documents.
- **Lifecycle**: READY, CLAIMED, READY_FOR_REVIEW, IN_REVIEW, then
  COMPLETED or CANCELLED (reopenable) or TERMINATED (final).
  ([Task lifecycle](https://kadai-io.github.io/kadai-doc/docs/user-guide/core-concepts/taskLifecycle))
- **Extension points** (Java SPIs): history, priority, routing, before and
  after review, create preprocessor, user refresh.

Delegation and substitutes are not mentioned on the permissions page.

## Polyflow (formerly camunda-bpm-taskpool)

**What it is.** A "library for pooling user tasks and process related
business objects", Apache 2.0, by Holunda: the inbox across several process
engines. ([Repository](https://github.com/holunda-io/camunda-bpm-taskpool),
[documentation](https://www.holunda.io/camunda-bpm-taskpool/stable/))

**Concepts.**

- **Process platform and process applications.** Applications run the
  processes; the platform is process-agnostic: one task inbox, one list of
  business objects, user management, audit.
- **CQRS.** Task and business-data events are captured on the write side and
  served from read-optimised projections (in memory, JPA or Mongo, one API).
- **Collectors.** A Camunda collector through the engine's plug-in
  mechanism; a generic collector for other engines and systems.
- **Enrichment.** Tasks carry business data, so the inbox shows context
  without asking the engine.
- **Data entries.** Business objects that exist before a process and
  outlive it, listed beside tasks.
- **Event-sourced**, on Axon Framework (Kotlin, Spring Boot); the event
  store is Axon Server, JDBC, JPA or Mongo. Deployed single-node or
  distributed with Axon Server or Kafka.

Not found on the pages read: the task commands, how a command reaches the
engine, the task attributes, and authorisation rules.

## What the kernel should borrow

1. **Queues** (TASKANA's workbaskets). An approval waits on a team and is
   claimed, not only on a named person (`field:`) or a role. "Waiting on me"
   is then my Items and my teams' unclaimed ones. Claim, transfer and
   request review are a reusable approval-task pattern (Ventoux item 8).
2. **TASKANA's permission flags as a checklist** for what an approvals inbox
   will be asked for: knowing a queue exists, seeing its tasks, working on
   them, adding, moving out.
3. **An automated creator without read**: TASK_ROUTER is the starting work
   plan's intake principal (`docs/plans/starting-work.md`). Confirmation, not
   a change.
4. **The inbox as a projection** (Polyflow). The "waiting on me" view across
   every Type is a read model fed by events: the event reader of roadmap
   theme 5, not a query over every Type.
5. **Collectors and enrichment** for one inbox over other systems: intakes,
   enrich hooks, and an object reference per Item (`external_keys`). Enriched
   copies of business data fall under the erasure rule.

## Where the kernel differs on purpose

- **Rules live with the work.** Both manage or pool tasks while process logic
  lives in an engine; here the task, its rules and its case are one Item,
  governed by its Type.
- **Not event-sourced.** `docs/design.md`, "Architecture style": current state
  plus an event log. Polyflow is a reference for the read side only.
- **Agents and injection.** Neither has agents as actors or a model of
  untrusted text.
- **Delegation** ("out of office") is not on the KADAI permissions page read;
  the approvals pilot lists it.

## On Temporal specifically (added 2026-11-24)

Temporal has no human task management of its own; its forum says so, and its
answer is a documented pattern ("Reliable document approvals with
human-in-the-loop workflows"), not a component. Open-source work nearby:

- **OpenNSW `taskflow`** ([package](https://pkg.go.dev/github.com/OpenNSW/core/taskflow),
  [repository](https://github.com/OpenNSW/core), Apache-2.0, no tagged
  version). A task layer between a business workflow and its human steps,
  in Go: each task is its own small Temporal workflow of sequential steps
  run by plugins (user input, API call, payment, external review); the
  parent's activity returns `ErrResultPending` and is completed when the
  task ends. Built for one application.
- **Ballerina workflows**: human tasks and a management API, running on
  Temporal, for programs written in Ballerina.
- **Zigflow** ([repository](https://github.com/mrsimonemms/zigflow)):
  Temporal workflows declared in YAML (CNCF Serverless Workflow), listed in
  Temporal's Code Exchange. Steps only: no work items, deciders, inbox or
  agents. The nearest neighbour to "Types instead of code"; to watch, maybe
  complementary.
- **iWF**: a programming model interpreted on Temporal, for developers.

**Borrowed from OpenNSW** (each placed where it will be used):

1. **Completing a step with a signed link** (`CompleteTaskStepByToken`):
   for the kernel, a single-use link naming one person, one Item and one
   transition, with an expiry, the click still judged by the Type's rules.
   The fallback for gated moves in "work done from a chat"
   (`docs/roadmap.md`), and "approve from the email".
2. **A move writes only its own fields** (a step's submission is scoped to
   its own namespace, the server stamping the key): a transition declares
   the fields it may set, nothing else. Raised for track 1's items 3 and 4.
3. **One call with everything a screen needs** (`GetTaskRenderInfo`): the
   Item, the moves this person may make now, the Type's presentation hints.
   For the inbox (track 1, item 5) and cards in chat.
4. **Waiting on outside work with asynchronous activity completion**
   (`ErrResultPending`): how the hook protocol's `pending` outcome could
   work.

**The same gaps as the kernel's:** a claim is "UI guidance" there, not
enforced (track 1, item 5 makes it bind); tasks are pinned to the version
they started with, with no migration (track 1, item 6).
