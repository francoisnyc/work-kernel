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
   (Superseded, 2026-11-25: `task-queue-internals.md` found that every
   engine read stores who may act in the same write as the task, and
   item 5's Phase A kept a read model fed by events for the inbox over
   other systems only. As built, step 1 derives the view at read time;
   rows written in the same write are its step 3.)
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

## Established human-task designs (added 2026-11-24)

Read for track 1, item 5 (team queues with a claim that binds): the
WS-HumanTask standard (OASIS, 2007 to 2010; implemented by jBPM, Oracle,
Informatica and others), Orkes Conductor's human tasks, and Camunda 8's user
tasks. Temporal's own approvals pattern is summarised at the end of this section.

**Worth borrowing:**

1. **Roles per task** (WS-HumanTask): potential owners (receive it),
   actual owner (does it), excluded owners (may not take it), business
   administrators (stakeholders for a whole kind of task), notification
   recipients. Excluded owners are the four-eyes rule; the kernel has it as
   a transition's `except`. Business administrators are missing: someone
   who sees every task of a kind and can reassign a stuck one or resolve a
   missed deadline, without deciding it. "Nothing is skipped automatically"
   needs that person.
2. **Escalation reassigns and resets the claim** (jBPM): when an escalation
   is reached, its people become the potential owners, and an actual owner
   is reset to ready. A claim that binds needs a rule for when it lapses.
3. **An escalation chain as data** (Orkes): assignment policies in order,
   each with its own time limit (`slaMinutes`); unpicked in time, the task
   moves to the next. `autoClaim` claims for the first assignee. Built on
   the kernel's timers.
4. **Due and follow-up dates, first-class and queryable** (Camunda): what an
   inbox sorts and filters on.
5. **Assignment changes judged like any move** (Camunda's user task
   listeners can correct the assignee, due date or priority, and deny the
   lifecycle transition): in the kernel, claim, release and reassign as
   transitions, so checks and `allowed` apply.
6. **Absences** (Oracle's vacation rules add alternates for absent users):
   the approvals pilot's delegation and out of office.

**A design signal:** Camunda 8 no longer uses candidate users and groups for
who may see or work on a task; its authorization does. A queue should be
defined by the kernel's permissions, not by a separate list.

**Temporal's approvals pattern** ([guide](https://docs.temporal.io/guides/reliable-document-approvals),
read 2026-11-24). One long-running workflow per document waits for the
reviewer, sends reminders, escalates once, and keeps an audit trail in its
state (carried across Continue-As-New). It confirms the gap rather than
filling it:

- no inbox: approvals are found by search attributes per workflow
  (document, submitter, status); nothing lists what waits on one reviewer;
- no authorization: the decision signal stores whatever it receives, without
  checking that the sender is the assigned approver;
- one escalation contact, then an automatic rejection on timeout (the
  kernel's rule is the opposite: nothing is skipped automatically);
- good practice to keep: idempotency keys for notifications, deduplicated
  updates, and Temporal's event-history limits (51,200 events or 50 MB) for
  long-running processes.

**A queue is not a container** (decided for item 5's Phase A). A container
says where an Item belongs: who can see it, under which policy; it is
stable for the Item's life. A queue says who must act next, and changes at
each step. So a queue is a view derived from the Type: the Items, in
containers the reader can see, whose open transitions in their current state
the reader (or the reader's role) may make, minus `except`. "Waiting on me"
is that view for one person; a team queue is it for a role; a container is
a filter on it ("finance, for the Purchasing desk"); the claim binds on top.

Items do move between containers today (`Update` with a `Move` patch; both
ends visible to the mover; widening confirmed; `placement_writable_by`), and
that is right when an Item's home changes: a case transferred to the team
that now owns it, TASKANA's "transfer". A step of a process (manager, then
finance, then the CFO) is not a move: made one, every approval would also
change who can see the Item.
