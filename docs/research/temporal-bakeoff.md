# Bake-off 2: the kernel, or each process written on Temporal

Status: result, 2026-11-24. The brief is `docs/plans/temporal-bakeoff.md`
(on branch `docs/plan-temporal-bakeoff` when this ran). The code and the raw
results are in `spikes/temporal-bakeoff/`; `scripts/run-all.sh` there repeats
the main measurements in about five minutes. It builds on the first
bake-off (`docs/research/camunda-bakeoff.md`, `spikes/camunda-bakeoff/`).

Written for the owner's colleagues as well as the owner.

**Words used.**
- The **kernel** is this repository's own system, which itself runs on
  Temporal. In it a process is a **Type**: a YAML file of fields, states and
  who may make which move. A request is an **Item** of that Type. `pli` is
  the kernel's command-line tool. A **role** is a name a person holds
  (finance, cfo). An agent works in a **session**, under **grants**: what
  each person has allowed it to do for them. A **frozen** field cannot be
  changed in a given state.
- **Temporal** is a workflow engine. A **workflow** is a function whose
  progress survives restarts. An **update** is a message to a running
  workflow that a **validator** may refuse. An **activity** is a call to
  the outside world, retried by Temporal. **Replay**: Temporal rebuilds a
  workflow's state by re-running its code over its recorded history, so a
  code change that no longer matches a history breaks that request.
- **Plain Temporal** here means each process written directly as a
  Temporal workflow in Go, with no kernel.
- The **agent** prepares each decision (a summary, a policy check, flags)
  and must never decide.

## The question, and the short answer

Where Temporal already runs, the realistic alternative to the kernel is to
write each process directly on Temporal, in code. What does each new
process, and each change to a running one, cost on each side, and how much
of what keeps a process safe has to be written again every time?

**Short answer, after an independent review that changed it:**

- **A process costs three to five times more text on plain Temporal** (for
  the second process, 252 lines of Go against 50 of YAML, comments and
  tests left out), and about 125 of its lines were copied from the first
  process. It needs a developer who knows Temporal's rules for workflow
  code; a Type was written from examples and the kernel reads it back in
  plain language.
- **Neither side was safe as first built, and both could be made safe.**
  The author's attacks found nothing on plain Temporal and two holes in
  every kernel Type. The reviewer then found two holes on plain Temporal
  the author had missed, both in every process: the agent could use up a
  request's updates so no decision could reach it, and an order that
  failed left the request reading "approved". Fixing them took 137 lines
  of Go across both processes, part of it copied into each. The kernel's two holes closed with
  two to five lines of YAML per Type (a role only people hold). On both
  sides the fix is something every author must know to write.
- **What plain Temporal had to rebuild includes things the kernel handles
  once for every Type**: a limit on what one actor can do to a request,
  retries that do not give up, a submission that is safe to retry, a
  record that outlives Temporal's 24-hour retention. The author missed the
  first two; an outside reviewer found them.
- **A change reached requests in flight on plain Temporal, and none on the
  kernel.** Made carelessly, the same change on plain Temporal broke
  reading requests already decided; Temporal's replay check caught it.
- **Plain Temporal built what the kernel lacks:** an inbox, and a claim
  that binds, once for every process.
- **Two cautions.** The plain side was designed by someone who had read
  the first bake-off's findings about the kernel (no editable drafts; the
  reason inside the decision), while the kernel was frozen as it was. And
  both sides run an unauthenticated Temporal development server that let
  an order through with no approval; on the kernel the fix is designed (an
  effect re-checks the kernel's own state), while on plain Temporal the
  person deciding is just data in the message, so Temporal itself must be
  locked down.

The recommendation is at the end.

## What was built

The scenario is the first bake-off's: a purchase approval (the manager;
finance from a shared queue from 1,000; an agent that prepares; a stub
purchase system) and an access request decided by the system's owner. The
stubs, seed data, people, stand-in model (deterministic, and obedient to
instructions hidden in a request's text) and first attack script are reused
from `spikes/camunda-bakeoff/` by path.

**The kernel side** is the first bake-off's, unchanged: Types in YAML, the
kernel's programs from `main`, the agent as a script. No kernel code changed.

**The plain-Temporal side** is new: Go, the Temporal SDK, Postgres, a small
HTTP API, the same dev tokens for sign-in. One workflow per request.
- Each thing a person or the agent does is an **update with a validator**,
  Temporal's pattern for a request that can be refused. The validator holds
  the rule for who may do it, in which state.
- What was asked (amount, vendor, justification) is the workflow's input.
  No update changes it, and there are no drafts.
- The workflow reports every change to Postgres through an activity: one
  table the inbox reads, one audit trail. Reading a request asks the
  workflow itself.
- The agent is an outside program with its own API token, as on the kernel
  side. It never touches Temporal.

Built in two steps, as the brief asks:
1. **The purchase approval**, by this report's author, with tests.
2. **The access request**, by a separate agent given the step-1 code and
   the business's request in five sentences, told not to read the brief,
   the results or the attack script. Committed as handed back, attacked
   before anything was changed.

For symmetry, **the kernel's access request was also rewritten** by
another separate agent given the purchase Type, the kernel's design
document and the same five sentences (`kernel-blind/`). See assumption 6
for how independent these builds really were.

## The four measures

| Measure | Kernel | Plain Temporal |
|---|---|---|
| **1. A second process** | 95 lines of YAML in 2 files (50 without comments), plus a 10-line setup script. About 25 repeat the purchase Type. No tests. | 317 lines of Go in 2 files (252 without comments), 194 lines of tests, 3 of wiring. About 125 copied from the purchase. After the review, 18 more lines of code, the cap copied again. |
| **2. A change in flight** ("over 5,000, the CFO too") | 11 lines of YAML added, 3 changed, checked and explained before it runs. **Reaches no request in flight.** A requester can still ask for the old version. | 9 lines added and 4 changed in the workflow, one version check, 21 lines of tests. **Reached all three requests in flight.** Without the version check it broke reading a request already approved; Temporal's replay check caught it. |
| **3. What every process rebuilds** | Given to every Type: the API, history, who decides, frozen fields, search, retries, safe resubmission, a limit nobody can exhaust. **Missing: an inbox, a claim that binds.** Every member of staff reads every request (these Types narrow nothing). | Built once: sign-in, the inbox and a binding claim, the audit trail, outside calls, API plumbing (429 lines). Per process: its rules, routes, tests, and copies of five helpers. Missed at first and added after review: the update limit, retries that never give up, safe resubmission. Still missing: search, a record after 24 hours. |
| **4. Consistency under attack** | Purchase 26 of 29 refused; access request 16 of 19 (the rewrite). The same two holes in every Type, **closed by 2 to 5 lines of YAML per Type** once known. The reviewer's four new attempts: all refused. | The author's list: purchase 16 of 16, access request 15 of 15, before any hardening. The reviewer's: **the agent blocked decisions in both processes**, and a failed order was silent. After 137 lines of fixes (tests included): all refused. |
| Side doors (no credentials) | The Temporal development server places an unapproved order (the first bake-off's known hole, fix designed, not built). | The Temporal development server places an unapproved order too, and the audit trail names the manager as the one who approved. |
| Footprint | About 180 MB idle, 5 parts, up in about 7 s | About 125 MB idle, 3 parts, up in about 6 s |

## Measure 1: the marginal cost of a process

Evidence: the commits "step 1", "step 2" and "the access request as kernel
Types, written blind"; the two builders' reports, summarised here.

**Plain Temporal.**

| | Lines (without comments and blank lines) | Files |
|---|---|---|
| Shared base, written in step 1: sign-in 72, store 160, activities 107, API plumbing and shared routes 172 | 511 (429) | 4 |
| The purchase: workflow, routes | 427 at step 1 (374 now) | 2 |
| The purchase's tests | 156 at step 1 | 1 |
| The program (API and worker) | 94 | 1 |
| **The access request: workflow 217, routes 100** | **317 (252)** | 2 |
| The access request's tests | 194 | 1 |

- The second builder changed no shared package. It reused the store, the
  activities, the inbox, the audit trail and the sign-in as they were.
- By its own count it **typed again about 125 lines that exist for
  purchases**: the rule for who decides, the preparation's validator, "a
  reason is needed to reject", the helpers that record changes, the check
  that a route reaches only its own kind of request, and about 50 lines of
  test harness. They were private to the purchase package or tied to its
  data, so reuse meant copying. Of the 203 lines of real code in its
  workflow, 115 match the purchase's line for line (the reviewer's count).
- The two names the shared routes depend on are defined inside the
  purchase package, so the access request imports the purchase.

**Kernel.**

| | Lines (without comments) | Files |
|---|---|---|
| The purchase Type | 82 (57) | 1 |
| **The access request, rewritten: the system 26, the request 69** | **95 (50)** | 2 |
| Its setup script (two systems created as data) | 10 | 1 |
| The access request, the author's (first bake-off) | 59 | 2 |

- The builder wrote no code and no tests. The kernel gave it, unwritten:
  who decides (the person a field names), "never the requester", agents
  cannot decide, role-only fields, frozen fields, the notice on rejection,
  history, search.
- It **repeated about 25 lines** the purchase Type already says (the
  preparation fields, the reason, the notice, the frozen lists): a Type
  cannot share with another Type.
- It needed a documented trick for "only inside a system" (an extra field
  and an invariant), and found the rules spread over a 4,000-line design
  document; an existing Type was "the fastest teacher".

**Reading.**
- Leaving out comments, blank lines and tests, a second process was 252
  lines against 50: five to one. With tests on the plain side (the kernel
  Types have none), ten to one; with comments and no tests, three to one.
  Tests are a cost, and also what caught nothing the kernel's readings
  missed: neither side's checks flagged the requests that could never be
  decided (measure 4).
- The builders' times (about 4 minutes for the Go, about 3 for the YAML)
  are one run each by agents. They say the order of effort was the same,
  nothing about a person. The author's own step times in
  `results/timing.txt` are the times of single edits, not of work, and are
  not used.
- The first process is where plain Temporal pays its entry fee: about 600
  lines of base and program. The kernel's entry fee is the kernel: about
  31,000 lines of Go that run it (42,000 counting its benchmarks, test
  harness and stubs; 74,000 more of tests), one person's proof of concept.
  For two processes that fee outweighs everything else here; it is shared
  across every process the kernel ever runs.

## Measure 2: the cost of a change

"Purchases over 5,000 also need the CFO, after finance", with four 6,000
purchases already open, one at each step. Evidence:
`results/change-plain.md`, `results/change-kernel.md`.

**Plain Temporal.** In the workflow: a new state, who decides it, the
branch after finance, the inbox entry (9 lines added, 4 changed), one call
to `workflow.GetVersion` (Temporal's documented way to change running
workflows), and 21 lines of tests. `purchase/change.go` and a start-up
switch exist only so the bake-off can run the program before the change,
with the careless change, and patched, over the same recorded histories.
- *Requests in flight:* all three that had not yet passed finance took the
  CFO step. The one already approved stayed approved and readable. A
  finance member and the agent were refused at the CFO's step.
- *Where the change applies is the developer's choice:* the version check
  sits at finance's approval. At the start of the workflow it would have
  left requests in flight on the old rule. Temporal's worker versioning
  (pinning each request to the code it started on) is the other documented
  choice; it was not tried.
- *The same change made carelessly:* nothing fails at deploy. Afterwards,
  reading the purchase approved before the change answered an error: its
  history no longer matched the code. Temporal's replay check (`ptreplay`,
  85 lines, written once) reported exactly that purchase before the
  deploy. A team must run it, ideally in its build pipeline over saved
  histories; here it is a command.
- *Who can make the change:* a developer. *What explains it:* the code and
  its tests.

**Kernel.** The first bake-off's new version of the Type: 11 lines added and
3 changed, checked by `pli type validate` and explained by `pli type
explain` before anything runs.
- *Requests in flight:* none took the CFO step. An Item keeps the version
  it was created on, and nothing moves it.
- *New requests* took it. A request created after the change **on the old
  version, by name,** did not (the first bake-off's gap).
- *Who can make the change:* an assistant writes the YAML; a person reviews
  the plain-language reading.

**Reading.** Plain Temporal changes a running process completely, and puts
it on the developer: where the change applies, the version check, the
replay run. The kernel checks and explains a change, then applies it to
nothing in flight, and lets a requester avoid it.

## Measure 3: what every process rebuilds

A list a team can use to estimate its own processes. "Found in review"
marks what the author missed and the reviewer found.

| What a process needs | Kernel | Plain Temporal: written once | Plain Temporal: again per process |
|---|---|---|---|
| Sign-in | Dev tokens only | 72 lines (the same dev tokens) | |
| An API | One API for every Type | About 60 lines of plumbing | Its routes: 100 to 115 lines |
| Who may do what | Declared per move | | Its validators: 25 to 60 lines, **the core rule typed again** |
| What was asked cannot change | Declared per state. A draft is editable by anyone who sees it; a role on the field closes that to the agent, not to colleagues | Nothing: a workflow's input is fixed, and there are no drafts | |
| The agent's limits | A role on its fields; sessions and grants (acting for a person) | Its to-do list, 15 lines | Its update and validator, about 35 lines, copied. No "acting for a person" |
| A limit one actor cannot exhaust | Given (2,050 rewrites, then the decision went through) | | **Found in review:** a cap per process, copied |
| Retries that never give up | Given; a failed action is recorded | **Found in review:** unbounded retry on the record | **Found in review:** a failed-order state, per process |
| A submission safe to retry | Given (a request ID on every write) | **Found in review:** a helper, 28 lines | Two lines in each route |
| The inbox | **Missing** (a 64-line script in the first spike) | About 40 lines | Whom the request waits on: 4 lines |
| The claim | **Missing** (a field nobody enforces) | | About 35 lines; binds; any finance member may release it (after review) |
| Who may see a request | Per Item; these Types narrow nothing, so every member of staff reads every request | About 25 lines | Its readers: 5 lines |
| Audit trail, history | Every write, with who and for whom | About 55 lines | About 25 lines of helpers, copied |
| A record after Temporal forgets | Postgres holds the truth | **Missing:** reading asks the workflow, which Temporal deletes 24 hours after it ends; only the inbox's copy remains | |
| Search | Across Types | **Not built** | |
| Calls to other systems | Only what the kernel ships | Any, by writing an activity | A new activity |
| A change to a running process | A new Type version; nothing in flight moves | The replay check, 85 lines | A version check per change |
| Checks before it runs | Load check, `validate`, `explain` | The compiler | Its tests, 150 to 190 lines |
| Screens | None on either side | | |

**What a shared library would absorb, and what it would then be.** After
two processes and one review, six things exist twice on the plain side:
the rule for who decides, the preparation and its cap, the recording, the
route guard, the failed-step handling and the test harness. A team would
lift them into a library by the third process; the reviewer estimates that
library at about 175 lines, and the access request would shrink to about
330 lines with tests. Whether that library, grown, becomes something like
the kernel is the author's expectation, not something these two processes
show.

## Measure 4: consistency under attack

A script holding the agent's credentials tries every way found to approve,
skip a step, change what is decided, or stop a decision. Evidence:
`results/attacks-plain.md`, `results/attacks-kernel.md`,
`results/attacks-kernel-blind.md`,
`results/attacks-plain-access-before-hardening.md`,
`results/attacks-review-*.md`, `results/kernel-staff-role.md`.

**Round 1: the author's lists.**

| Process | Kernel | Plain Temporal |
|---|---|---|
| Purchase | 26 of 29 refused; 2 succeeded; 1 allowed by the requester's own grant | 16 of 16 refused |
| Access request, as first written | Rewrite: 16 of 19; 2 succeeded; 1 allowed. The author's Type: 15 of 19 | 15 of 15 refused |

How to read these counts. The kernel's purchase list is the first
bake-off's and includes three attempts on access requests. Of the plain
side's 31, six are writes to a route that does not exist (refused because
there is no such door, a fair point for the design but not a test of a
rule), and four in the access list are people, not the agent: 21 are the
agent trying a real door. The kernel has more doors (drafts, editable
fields, agents acting for people), so it has more attempts.

**The kernel's two holes, in every Type.** With only the right to edit
fields, the agent changed ana's draft (the amount and vendor; on access
requests, the role) before she submitted it, and what was frozen for
approval was the agent's version. And it planted a rejection reason, so the
decider could reject without giving one, though the Type requires it.
Both hold in three Types.

**Round 2: the reviewer's attempts, on both sides.**

| Attempt | Kernel | Plain Temporal, as built | Plain Temporal, after fixes |
|---|---|---|---|
| The agent rewrites its preparation 2,050 times, then the decider approves | Approved | **The decision fails, for ever, in both processes** (Temporal accepts 2,000 updates per workflow) | 20 rewrites accepted; approved |
| An approved purchase whose order the purchase system refuses | `HOOK_FAILED` in its history | **Reads "approved", no order, no trace**; the workflow failed after eight tries | `order_failed`, waiting on finance, with the error |
| The same submission sent twice | One purchase | **Two purchases** | One, with a request ID |
| A finance member takes over a colleague's claim | Allowed | **Refused: only the claimer releases** | Allowed |
| A colleague with no part in it reads a request | **Allowed** (every member of staff) | Refused | Refused |

The fixes on the plain side: a cap on preparations, copied into both
workflows; an `order_failed` state; unbounded retries on the write to
Postgres, in both; an optional request ID on submission (a shared helper);
any finance member may release a claim. 137 lines added and 25 removed
across seven files, tests included. One step-1 test pinned the old release
rule and was changed with it, as a deliberate design change.

**Can a Type close the kernel's holes?** The first draft of this report said
no. The reviewer said it could, and it can (`results/kernel-staff-role.md`):
make what is asked, and the reason, writable only by a role every person
holds and the agent does not (`writable_by: ['role:staff']`, two to five
lines per Type). The agent was then refused on both, acting alone and
acting for ana; the normal paths still worked. A colleague can still change
someone's draft: no Type can say "only the requester". The fix exists; the
default does not apply it, and both Type authors left it out.

**Reading.** The brief asked what passing took, and whether the second
process got it right without being told. On plain Temporal the second
process copied the first one's rules correctly, and with them the first
one's holes: the update-limit hole was in both processes, found by neither
builder. On the kernel the second Type inherited the same two holes,
closable in each Type by those who know. On both sides consistency came
from copying; on the kernel the copy is two lines of YAML and the platform
already covers the limits, retries and resubmission that plain Temporal
had to discover and code.

**Side doors** (`results/sidedoors.md`), with no credentials:
- *Temporal.* Both sides run a development server with no sign-on, and on
  both it places an order nobody approved. On the kernel a hand-started
  workflow claims the purchase is approved (the first bake-off's known
  hole; its fix, "an effect re-checks committed state", was not on `main`
  when this ran). On plain Temporal the manager's approval is sent straight
  to the workflow, naming the manager, and the audit trail then shows the
  manager deciding. As instructed, this does not decide the comparison, but
  the two are not the same: with its fix the kernel would refuse the forged
  version, because the truth is in its own database; on plain Temporal the
  person deciding is data in the message, so anyone who reaches Temporal
  (workers, operators, its web console) can decide anything, and only
  locking down Temporal itself prevents it.
- *Postgres with the development password.* On plain Temporal it holds the
  inbox's copy: changing an amount there made the manager's inbox show the
  wrong amount (a manager who approves from the inbox alone, under 1,000,
  would place that order), and adding a name to its readers let a stranger
  read the request. It decided nothing by itself; the workflow kept the
  truth. On the kernel it holds the truth; it was read, not written.
- Both APIs refuse a call without a token.

## Footprint, and what each side costs to maintain

Evidence: `results/footprint.md`, `results/start-*.txt`.

| | Kernel | Plain Temporal |
|---|---|---|
| Parts | Postgres, Temporal, 3 programs | Postgres, Temporal, 1 program |
| Memory, idle | About 180 MB | About 125 MB |
| Memory, after the tests | About 268 MB | About 172 MB |
| Start | About 7 s, including a cached build | About 6 s, not counting the build |
| Programs on disk | 120 MB | 38 MB |

Both run on the same infrastructure. On plain Temporal, Temporal is also
the record of who approved, so securing it (sign-on, encrypting the
justifications and reasons it stores in clear) matters more there.

- **Plain Temporal, to maintain:** about 1,950 lines of the team's Go for
  two processes, growing by 300 to 500 per process with its tests. Every
  process and every change needs a developer who knows Temporal's rules
  for workflow code. Safety rules are code to review in each process, and
  a review found holes in code written carefully.
- **The kernel, to maintain:** 150 to 200 lines of YAML for two processes,
  and the kernel itself: one person's proof of concept with known gaps.

## Gaps found

**On plain Temporal:**
1. Safety rules and their fixes are copied into every process; nothing
   checks a new process has them. The update-limit hole was copied too.
2. Platform concerns are each the team's to find: update limits, retries,
   resubmission, retention. A careful build missed two of four.
3. A change needs a developer who knows versioning; a careless one breaks
   requests already decided, silently at deploy.
4. Whoever reaches Temporal can decide anything, and the audit trail will
   name whoever they claim to be.
5. Requests are unreadable from Temporal 24 hours after they end.
6. Who may see a request, and the inbox, come from a copy in Postgres.
7. No search, no screens, no "acting for a person", no plain-language
   account of a process.

**On the kernel** (from the first bake-off, with this run's evidence):
1. An effect does not re-check committed state (the side door).
2. Fields are writable by the agent unless a Type says otherwise; the safe
   setting exists (a role only people hold) but no author used it. No
   "only the requester edits a draft".
3. No inbox, no claim that binds; every member of staff reads every
   request unless a Type narrows it.
4. A change reaches nothing in flight; an old version stays askable.
5. A Type cannot share with another Type, and cannot say where its Items
   may be created or who may ask.
6. Neither `validate` nor `explain` caught a request that can never be
   decided (an owner asking for their own system).

## Assumptions, as they were made

1. The brief was not on `main`; it was read from `docs/plan-temporal-bakeoff`
   and not copied into this branch.
2. The first bake-off's pull request was not merged; this branch is built
   on `spike/camunda-bakeoff` and reuses its files by path.
3. **The precondition was not met:** the kernel's first hole ("an effect
   re-checks committed state") was not fixed on `main`. As instructed, the
   run went ahead, and the kernel's forged-workflow start is reported as a
   known open hole, kept out of the comparison.
4. No `ANTHROPIC_API_KEY`: no live model, and the first bake-off's injection
   run was not repeated (the compromised-agent script needs no model).
5. The plain side's agent is an outside program with an API token. Inside
   the worker it would hold the worker's connection to Temporal, which can
   do anything.
6. **How blind the second builds were.** Both builders were separate agents,
   told what not to read; neither was prevented. The plain-side builder
   could have read the attack script (it was in the step-1 commit), and the
   step-1 tests and comments state the rules it copied. The kernel-side
   builder's Types are, apart from comments, one field and an invariant,
   the author's first-bake-off Type: it followed names from the agent
   script it was allowed to read and the kernel's own example Type. Call
   both rewrites by a well-informed teammate, not independent designs.
7. The plain side's inbox and audit trail are in Postgres, written by the
   workflows, rather than in Temporal's search: the brief names Postgres
   "for anything Temporal should not hold".
8. The session was paused for about eight and a half hours (about 03:50 to
   12:27 UTC) with no work in between, and the containers stopped and were
   started again. Active work, about 75 minutes in all, is inside the
   half-day box; the wall clock is not.
9. Times are an agent's minutes. Tests-first with the owner's approval was
   not possible in an autonomous run.

## Not done

- A live model.
- A shared library on the plain side, and a third process of another shape
  (parallel approvers, drafts, delegation).
- Temporal's own sign-on (mTLS, an authorizer, a payload codec), worker
  versioning, replay in a build pipeline, continue-as-new.
- The kernel's role fix applied to its real Types, and a re-run of its full
  lists with it.
- Non-developers writing or changing either side.
- Screens, load, several versions of one workflow stacked, a rollback.

## The reviewer's notes, and what changed

A separate agent that built nothing read the report and the code, argued
plain Temporal's case, attacked both running systems, and listed what was
unfair. Its notes, condensed, each followed by what was done. Everything
was reproduced by the author before being accepted.

**Attacks that worked**
1. *The agent's token makes any waiting request undecidable, in both plain
   processes* (2,000 preparation rewrites, then every decision answers an
   error). **Reproduced; fixed by a cap in each workflow; re-run: refused.**
   The kernel took 2,050 rewrites and then the decision.
2. *An approved purchase can end with no order and no trace* (an unknown
   vendor; eight tries, then the workflow fails). **Reproduced; fixed with a
   visible `order_failed` state and unbounded retries on the record.** The
   kernel records `HOOK_FAILED`.
3. *The claim binds too hard:* only the claimer could release it.
   **Reproduced; any finance member may now release it.**
4. *The Postgres side door was scored "decides nothing", but the inbox shows
   Postgres's copy.* **Reworded.**
5. *Finished requests stop being readable after Temporal's 24-hour
   retention.* **Retention confirmed (24 h); recorded as a gap; not fixed.**

**Claims the evidence did not support**
1. *"31 of 31".* **Corrected:** broken by attack 1, and the counts now say
   which attempts were the agent at a real door.
2. *"No Type can close" the kernel's holes.* **Tested and withdrawn:** a
   role only people hold closes both for the agent.
3. *The plain second build was not blind to the attack list.* **Accepted;
   assumption 6.**
4. *The kernel rewrite is nearly the author's Type.* **Accepted;
   assumption 6.**
5. *"Five times more text" counted tests on one side.* **Recomputed on one
   basis, with the others stated.**
6. *"17 lines" counted changed lines twice.* **Corrected to 9 added, 4
   changed.**
7. *Start times were not on the same basis.* **Stated.**
8. *The author's step times measure edits, not work; "35 minutes" was not
   derivable.* **Dropped.**
9. *"42,000 lines" includes benchmarks, the story harness and stubs.*
   **Corrected to about 31,000 that run it.**
10. *"A production Temporal costs the same on either side" is unsupported.*
    **Reworded:** on plain Temporal it is also the record of who approved.

**Unfair to plain Temporal:** tests counted on one side only (**now
separated**); reads never attacked (**now scored: the kernel lets every
member of staff read every request; plain Temporal refuses**); Temporal's
strengths (timers, escalation, compensation) not exercised (**listed under
"Not done"**); the agents' minutes are one run each (**said**).

**Unfair to the kernel:** the plain side was designed knowing the first
bake-off's findings (**now in the short answer**); the kernel's agent had
grants to act for five people, which the plain side has no counterpart to
(**said in measure 4**); "does not decide the comparison" hid that the
side door is closable by design only on the kernel (**now in the short
answer and measure 4**); the kernel's Postgres door was read, not written,
yet labelled "open" (**said**).

**What a Temporal team would have done differently:** a shared approval
package from the first process; no retry cap on the record and the order;
a cap on updates or continue-as-new; an idempotency key on submission;
worker versioning as an alternative to the version check; replay in the
build pipeline; an authorizer or mTLS; a payload codec; owners as data
rather than code. **Four of these were built after the review; the rest
are under "Not done".**

**On the recommendation:** the draft called plain Temporal "the safer
side"; true only before the reviewer attacked it. It credited the kernel
with non-developer authoring, which was not tested (both Types were written
by agents). "The kernel is what such a library becomes" is the author's
hope. Two processes cannot show: a third process of another shape, authors
months apart, people's hours, stacked versions, operations over time,
non-developer authoring. **The recommendation below was rewritten with
these in mind.**

**Hard to follow:** "511" meant two things, "before and after hardening"
when nothing had been hardened, undefined terms. **Fixed or defined above.**

## Recommendation

At two processes, written by developers, the evidence does not favour
either side enough to switch, and it changes what each must do next.
**Where Temporal already runs and the next few processes will be written by
developers, write them on plain Temporal, but not one at a time:** start
from a shared approvals package that holds what this bake-off had to
discover (who decides, the preparation and its cap, recording with retries
that never give up, a failed-step state, safe resubmission, the inbox and
claim, a record that outlives retention), with replay checks in the build
and Temporal locked down. That gives changes that reach requests in flight,
no second platform, and an inbox today. **Keep building the kernel for
what plain Temporal leaves to every author,** and close first what this
bake-off and the first one found: an effect that re-checks committed state;
fields closed to agents by default (the fix that worked here, made the
default); "only the requester" for drafts; an inbox and a claim that binds;
a change that can reach requests in flight and retire old versions. **What
would change this answer:** a third process of a different shape, or many
processes by different authors over months (copying is plain Temporal's
risk, and two processes cannot show how it grows); processes that people
who are not developers must write or change, tried for real; the kernel
closing the gaps above; or no developer on the team who knows Temporal's
rules for workflow code.
