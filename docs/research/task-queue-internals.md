# How open-source task engines build "waiting on me"

2026-11-24. For track 1, item 5 (team queues with a claim that binds, and a
"waiting on me" view; `docs/roadmap.md`). Three questions, answered from the
code of Camunda 7, Flowable, KADAI (TASKANA's continuation) and Polyflow,
read from shallow clones of their default branches. Paths are relative to
each repository.

## 1. How is "waiting on me" fast?

**Every one of them stores who may act, per open task, and queries that.**
None computes it from the process definition at read time.

- **Camunda 7 and Flowable: an identity-link table.** Each open task has rows
  in `ACT_RU_IDENTITYLINK` of type `candidate`, each a user ID or a group ID,
  written when the task is created. The inbox query joins it: unassigned
  tasks (`RES.ASSIGNEE_ is null`) where `I.TYPE_ = 'candidate'` and
  `I.USER_ID_ = <me>` or `I.GROUP_ID_ IN (<my groups>)`.
  (Camunda: `engine/src/main/resources/org/camunda/bpm/engine/impl/mapping/entity/Task.xml`,
  around line 587; Flowable: `modules/flowable-task-service/src/main/resources/org/flowable/task/service/db/mapping/entity/Task.xml`,
  around line 881.)
- **Polyflow: the same idea in a read model.** Its task projection keeps a
  set of authorized principals per task in a side table
  (`PLF_TASK_AUTHORIZATIONS`, each `USER:<id>` or `GROUP:<id>`), fed by
  events from the engines, and filters on it.
  (`view/jpa/src/main/kotlin/io/holunda/polyflow/view/jpa/task/TaskEntity.kt`.)
- **KADAI: permissions on the queue, not the task.** A task sits in one
  workbasket; the query joins `WORKBASKET_ACCESS_LIST` on the caller's
  access IDs (the user and their groups) and its READ and READTASKS flags.
  (`lib/kadai-core/.../task/internal/TaskQuerySqlProvider.java`.)

## 2. What happens when group membership changes?

**Groups are resolved when the inbox is asked, not when the task is made.**
The task stores the group ID; the caller's groups come from the identity
provider at query time (Camunda's `TaskQueryImpl.getGroupsForCandidateUser`
asks `createGroupQuery().groupMember(user)`; KADAI uses the access IDs in
the current user context). So someone who joins a group sees its open tasks
at once, and someone who leaves stops seeing them. What is fixed at creation
is which *groups and users* are candidates: an assignment expression is
evaluated once, when the task is created.

## 3. How do claims behave?

- **Camunda 7 and Flowable: a claim does not check that you are a
  candidate.** `ClaimTaskCmd` refuses only a task already claimed by someone
  else (`TaskAlreadyClaimedException`, `FlowableTaskAlreadyClaimedException`)
  and runs the configured authorization checks; candidacy is not one of them.
  Concurrent claims are settled by optimistic locking (`REV_` on update).
  Completing does not require the claimant either.
  (Camunda: `engine/src/main/java/org/camunda/bpm/engine/impl/cmd/ClaimTaskCmd.java`;
  Flowable: `modules/flowable-engine/src/main/java/org/flowable/engine/impl/cmd/ClaimTaskCmd.java`.)
- **KADAI: the claim binds.** Claiming needs EDITTASKS on the task's
  workbasket and refuses a task another user has claimed (`InvalidOwnerException`),
  unless forced (`forceClaim`). Completing requires the task to be claimed and
  the caller to be its owner (or an admin).
  (`lib/kadai-core/src/main/java/io/kadai/task/internal/TaskServiceImpl.java`,
  `checkPreconditionsForClaimTask`, `checkPreconditionsForCompleteTask`.)

## What it means for the kernel

The kernel's rules for who may act are richer than a candidate list: `role:`,
`principal:`, `field:` (a person named on the Item), `except`, and guards in
CEL. The proposal for item 5's Phase A:

1. **Store who may act, per open Item, in the same transaction as the move
   that opened it**, like the identity-link table: rows of (Item, transition,
   token), where a token is `principal:<id>` (from `principal:` and from
   `field:` resolved to the person the field names) or `role:<name>`. The
   lifecycle's pure function derives the rows from the Type; nothing else
   writes them. A change of a `field:` field rewrites them in the same write.
2. **Resolve roles at query time**, as all of them do: the reader's roles
   (`principal_roles`) are joined when the inbox is asked, so a new member
   sees the queue at once. Groups, when they exist, the same way.
3. **Filter with the existing visibility rule first** (`scope_access`,
   `policy.Project`): the inbox shows only Items the reader may see. KADAI
   does the same with READ before READTASKS.
4. **Guards are not pre-evaluated.** A row means "may act, if the guard
   passes"; the transition still checks everything. `except` is applied
   when the view is read, not when rows are written (corrected at item 5's
   Phase A, 2026-11-25): a requester who holds the role is reached through
   the role's row, so no row can be left out for them.
5. **The claim binds, as KADAI's does, and goes further than Camunda's:**
   only someone the move's rules let in may claim; once claimed, only the
   claimant may make the Item's decisions until it is released, reassigned
   by an administrator, or reset by escalation. The claim is checked by the
   move's own rules under the Item's row lock, like every move, never
   against the stored rows (corrected, 2026-11-25): rows only serve reads,
   so a wrong row can show too much or too little in a list and never lets
   someone act. Concurrent claims need no optimistic lock: the Item row is
   already locked during every write.
6. **A read model fed by the event reader** (Polyflow's way) waits for the
   inbox over other systems, where the rows come from intakes rather than
   from Types.

**As built (2026-11-25, item 5, step 1).** The view came first, derived at
read time with no rows: `docs/design.md`, "Waiting on me, step 1, as
built", with what it costs. Storing the rows is step 3, decided on those
numbers.
