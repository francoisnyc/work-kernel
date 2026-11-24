# Bake-off 2: the kernel, or each process written on Temporal

Status: result, 2026-11-24. The brief is `docs/plans/temporal-bakeoff.md`
(on branch `docs/plan-temporal-bakeoff` when this ran). The code and the raw
results are in `spikes/temporal-bakeoff/`; `scripts/run-all.sh` there repeats
everything below in about five minutes. It builds on the first bake-off
(`docs/research/camunda-bakeoff.md`, `spikes/camunda-bakeoff/`).

Written for the owner's colleagues as well as the owner.

**Words used.**
- The **kernel** is this repository's own system, which itself runs on
  Temporal. In it a process is a **Type**: a YAML file of fields, states
  and who may make which move. A request is an **Item** of that Type.
- **Temporal** is a workflow engine: a **workflow** is a function whose
  progress survives restarts; an **update** is a message to a running
  workflow that a **validator** may refuse; an **activity** is a call to
  the outside world. **Plain Temporal** here means: each process written
  directly as a Temporal workflow in Go, with no kernel.
- The **agent** prepares each decision (a summary, a policy check, flags)
  and must never decide.

## The question, and the short answer

Where Temporal already runs, the realistic alternative to the kernel is to
write each process directly on Temporal, in code. What does each new
process, and each change to a running one, cost on each side, and how much
of what keeps a process safe has to be written again every time?

**Short answer, for two processes:**

- **A process costs about five times more text on plain Temporal** (511
  lines of Go against 95 lines of YAML for the second process). About 125
  of its 317 lines of code are copies of the first process's, the safety
  rules among them. It also needs a developer; a Type does not need one to
  be read.
- **Plain Temporal was nevertheless the safer side here.** Both of its
  processes refused every attempt of the compromised agent (31 of 31),
  including the second process, written by someone who was not told what
  would be attacked. The kernel let the same two tampering attempts
  through in both of its processes, and no Type can close them: they need
  kernel changes.
- **A change to a running process reached requests in flight on plain
  Temporal and reached none on the kernel.** Made carelessly on plain
  Temporal, the same change broke the reading of requests already decided;
  Temporal's own check catches that, if someone runs it.
- **What plain Temporal rebuilt was small, and one part came out better:**
  an inbox with a claim that binds, in about 80 lines, which the kernel
  still lacks.

The recommendation is at the end, after the reviewer's notes.

## What was built

The scenario is the first bake-off's: a purchase approval (the manager;
finance from a shared queue from 1,000; an agent that prepares; a stub
purchase system) and an access request decided by the system's owner. The
stubs, the seed data, the people, the stand-in model (deterministic, and
obedient to instructions hidden in a request's text) and the first attack
script are reused from `spikes/camunda-bakeoff/` by path.

**The kernel side** is the first bake-off's, unchanged: Types in YAML, the
kernel's programs from `main`, the agent as a script. No kernel code changed.

**The plain-Temporal side** is new: Go, the Temporal SDK, Postgres, a small
HTTP API, the same dev tokens for sign-in. One workflow per request.
- Each thing a person or the agent does is an **update with a validator**
  (the pattern Temporal recommends for a decision that can be refused):
  the validator holds the rule for who may do it, in which state.
- What was asked (amount, vendor, justification) is the workflow's input.
  No update changes it, so there is nothing to "freeze".
- The workflow reports every change to Postgres through an activity: one
  table the inbox reads, one audit trail.
- The agent is an outside program with its own API token, as on the kernel
  side. It never touches Temporal.

It was built in two steps, as the brief asks:

1. **The purchase approval**, complete, with tests, then attacked until it
   held (it held on the first run).
2. **The access request, written blind.** A separate agent was given the
   step-1 code and the business's request in five sentences, and nothing
   about attacks, the brief or the first report: the position of a teammate
   asked to add a process. Its work was committed as handed back and
   attacked before anyone touched it.

To compare like with like, **the kernel's access request was also written
again, blind**, by another separate agent given the purchase Type, the
kernel's design document and the same five sentences (`kernel-blind/`). The
first bake-off's own access Type, written by this report's author, is kept
as a second data point.

## The four measures

| Measure | Kernel | Plain Temporal |
|---|---|---|
| **1. A second process** | 95 lines of YAML in 2 files, plus a 10-line setup script. About 25 lines repeat the purchase Type. No tests written. About 3 minutes of an agent's time. | 511 lines of Go in 3 files (317 of code, 194 of tests), plus 3 lines of wiring. About 125 lines are copies of the purchase's code. About 4 minutes of an agent's time. |
| **2. A change in flight** ("over 5,000, the CFO too") | 14 lines of YAML, checked and explained before it runs. **Reaches no request in flight:** all three kept the old rule. A requester can still ask for the old version. | 17 lines of code and 21 of tests, using Temporal's versioning. **Reached all three requests in flight.** The same change without versioning broke reading a request already approved; Temporal's replay check caught it before the deploy. |
| **3. What every process rebuilds** | Nothing per Type for the API, history, who decides, frozen fields, search. **No inbox and no real claim** (first bake-off). | A shared base of 511 lines built once (sign-in, the inbox, the audit trail, outside calls, API plumbing). Per process: its rules, its routes, its tests, and copies of five helpers. **Inbox and claim built once, and the claim binds.** Search not built. |
| **4. Consistency under attack** | Purchase: 26 of 29 attempts refused. Access request: 16 of 19 (blind build), 15 of 19 (the author's). **The same two holes in every Type**, not closable in a Type. | Purchase: 16 of 16 refused. Access request: **15 of 15, before any hardening.** No hardening was needed, so "after" is the same. |
| Side doors (no credentials) | The Temporal development server places an unapproved order (the first bake-off's known hole). | The Temporal development server places an unapproved order too. Not allowed to decide the comparison: see below. |
| Footprint | About 187 MB idle, 5 parts, up in 7 s | About 125 MB idle, 3 parts, up in 6 s |

## Measure 1: the marginal cost of a process

Evidence: the commits "step 1", "step 2" and "the access request as kernel
Types, written blind"; `results/timing.txt`; the two builders' reports
(summarised here).

**Plain Temporal.**

| | Lines | Files | Agent time |
|---|---|---|---|
| Step 1, the purchase: shared base (sign-in 72, store 160, activities 107, API plumbing and shared routes 172) | 511 | 4 | |
| Step 1, the purchase itself (workflow 312, routes 115, tests 156) | 583 | 3 | |
| Step 1, the program (API and worker) | 94 | 1 | |
| **Step 1 in all** | **1,188** | 8 | about 5 minutes |
| **Step 2, the access request** (workflow 217, routes 100, tests 194, wiring 3) | **514** | 3 new, 1 touched | about 4 minutes |

- The second builder changed no shared package. It reused the store, the
  activities, the inbox, the audit trail and the sign-in as they were.
- By its own count it **typed again about 125 lines that exist for
  purchases**: the rule for who decides (a person, never the requester, the
  named person), the preparation's validator, "a reason is needed to
  reject", the helpers that record changes, the check that a route only
  reaches its own kind of request, and the test harness (about 50 more
  lines). They were unexported or tied to the purchase's data, so reuse
  meant copying.
- It found the shared code's hidden contracts awkward: two names the shared
  routes depend on are defined inside the purchase package, so the access
  request imports the purchase.

**Kernel.**

| | Lines | Files | Agent time |
|---|---|---|---|
| The purchase Type | 82 | 1 | not timed (first bake-off: minutes) |
| **The access request, blind build** (the system 26, the request 69) | **95** | 2 | about 3 minutes |
| Its setup script (two systems created as data) | 10 | 1 | |
| The access request, the first bake-off's (the system 10, the request 49) | 59 | 2 | not timed |

- The blind builder wrote no code and no tests. The kernel gave it, with
  nothing written: who decides (the person a field names), "never the
  requester", agents cannot decide, role-only fields, frozen fields, the
  notice on rejection, history, search.
- It **repeated about 25 lines** the purchase Type already says (the three
  preparation fields, the reason, the notice, the frozen lists): the Type
  format has no way to share between Types.
- It needed a documented trick for "only inside a system" (an extra field
  and an invariant), and found the rules it needed spread over three
  sections of a 4,000-line design document; an existing Type was "the
  fastest teacher".

**Reading.**
- In text, a second process is 95 lines against 514: about five to one, or
  three to one leaving tests out (the kernel side wrote none; whether that
  is a saving or a debt is a fair question).
- In an agent's time the two were close (3 and 4 minutes). These are
  agents' minutes, not people's. For a person the difference is who can do
  it: the Temporal process needs a developer who knows Temporal's rules
  for workflow code; the Type was written from examples and can be read
  back in plain language by the kernel (`pli type explain`).
- The first process is where plain Temporal pays its entry fee: 605 lines
  that the kernel side does not write. The kernel's own entry fee is the
  kernel: 42,000 lines of Go (and 74,000 of tests) that someone has to
  own. For two processes that fee dwarfs everything else in this table.

## Measure 2: the cost of a change

"Purchases over 5,000 also need the CFO, after finance", with four 6,000
purchases already open, one at each step. Evidence:
`results/change-plain.md`, `results/change-kernel.md`.

**Plain Temporal.** The change is 13 lines added and 4 changed in the
workflow (a new state, who decides it, the branch after finance, the inbox
entry), 21 lines of tests, and one call to `workflow.GetVersion`,
Temporal's documented way to change a running workflow.
- *Requests in flight:* all three that had not yet passed finance took the
  CFO step after the deploy. The one already approved stayed approved and
  readable. A finance member and the agent were refused at the CFO's step.
- *Where the change applies is the developer's choice:* the version check
  sits at finance's approval. Placed at the start of the workflow it would
  have left requests in flight on the old rule. Nothing asks the developer
  which was meant.
- *The same change made carelessly* (the new branch, no version check):
  nothing fails at deploy. Then reading the purchase that had been
  approved before the change answered an error: its recorded history no
  longer matched the code. Temporal's replay check (`ptreplay`, 85 lines,
  written once) reported exactly that purchase before the deploy: "1 of 4
  purchase histories no longer replay". The team has to know to run it.
- *Who can make the change:* a developer. *What explains it:* the code and
  its tests.

**Kernel.** The change is the first bake-off's: a new version of the Type,
11 lines added and 3 changed, checked by `pli type validate` and explained
by `pli type explain` before anything runs.
- *Requests in flight:* none of the three took the CFO step. An Item keeps
  the version it was created on, and the kernel has no way to move it.
- *New requests* took it. A request created after the change **on the old
  version, by name,** did not (the first bake-off's gap).
- *Who can make the change:* an assistant writes the YAML; a person reviews
  the kernel's plain-language reading.

**Reading.** Plain Temporal changes a running process more completely, and
puts more on the developer: where the change applies, the version check,
the replay run. The kernel changes a process more safely on paper and less
usefully in practice: checked and explained, but blind to everything in
flight, and avoidable by naming the old version.

## Measure 3: what every process rebuilds

What plain Temporal had to write that the kernel gives every Type, and the
reverse. Line counts are from the code (`README.md` has the table by file).
A team can use the list to estimate its own processes.

| What a process needs | Kernel | Plain Temporal: written once | Plain Temporal: again per process |
|---|---|---|---|
| Sign-in | Dev tokens only | 72 lines (the same dev tokens) | nothing |
| An API for the process | Given: one API for every Type | About 60 lines of plumbing | Its routes: 100 to 115 lines |
| Who may do what | Declared per move in the Type | nothing | Its validators: about 60 lines for the purchase, 25 for the access request, **the core rule typed again** |
| What was asked cannot change | Declared per state (`frozen`). A draft can be changed by anyone who sees it | nothing: a workflow's input cannot change, and there are no drafts | nothing |
| The agent's limits | A role on three fields; sessions and grants (an agent acting for a person) | Its to-do list: 15 lines | Its update and validator: about 35 lines, typed again. No "acting for a person" at all |
| The inbox | **Missing** (a 64-line script in the first spike) | About 40 lines | 4 lines: whom the request waits on |
| The claim | **Missing** (a field anyone in the team may ignore) | nothing | About 35 lines where a queue exists; the claim binds |
| Who may see a request | Given per Item; not used by these Types (all staff see all requests) | About 25 lines | 5 lines: its readers |
| Audit trail, history | Given: every write, with who and for whom | About 55 lines | About 25 lines of helpers, **copied** |
| Search | Given, across Types | **Not built** | |
| Calls to other systems | Only what the kernel already ships | 107 lines; any system, by writing an activity | nothing, or a new activity |
| A change to a running process | A new Type version; nothing in flight moves | The replay check: 85 lines | A version check per change |
| Checks before it runs | The kernel's load check, `validate`, `explain` | The compiler | Its tests: 150 to 190 lines |
| Screens | None for approvals on either side | | |

**What a shared library would absorb, and what it would then be.** After two
processes, five things exist twice on the plain side: the rule for who
decides, the preparation, the recording of changes, the route guard and
the test harness. A team would lift them into a library by the third
process; the access request would then be about 330 lines instead of 514.
That library would hold: a request's states, who may act in each, the
agent's preparation, the inbox entry, the audit line. Add a way to declare
those instead of coding them, and it is the kernel's Type. The kernel is
what such a library becomes when it is finished; the open question is
whether to arrive there by extracting from working processes or by
building it first.

## Measure 4: consistency under attack

A script holding the agent's credentials tries every way found to approve,
to skip a step, or to change what is being decided. Evidence:
`results/attacks-plain.md`, `results/attacks-kernel.md`,
`results/attacks-kernel-blind.md`,
`results/attacks-plain-access-before-hardening.md`. Counts are attempts.

| Process | Kernel | Plain Temporal |
|---|---|---|
| Purchase | 26 of 29 refused; 2 succeeded; 1 allowed by the requester's own grant | 16 of 16 refused |
| Access request, as first written | Blind build: 16 of 19 refused; 2 succeeded; 1 allowed. The author's: 15 of 19; 3 succeeded; 1 allowed | **15 of 15 refused** |
| Access request, after its own hardening | The same: its holes cannot be closed in a Type | The same: nothing needed hardening |

The lists differ in length because the sides differ: the kernel has drafts,
fields anyone may edit and agents acting for people, so there is more to
try; the plain side has no such doors.

**Plain Temporal: nothing got through, in either process.** The agent could
not decide, claim, name itself in a request's body, reach a request
through the other process's route, change what was asked, or rewrite its
preparation after the decision. The second process, written blind, also
got right two rules the request did not spell out: the owner cannot
approve their own request (it ends at once as "nobody else could decide"),
and a rejection needs a reason.
- *Why it held:* the second builder copied the purchase's rule for who
  decides, by hand, correctly, and the workflow's design leaves little to
  attack: no drafts, no editable fields, one door per action.
- *What it rests on:* one copy made correctly, by a careful builder, from
  code whose comments state the rule. The rule now lives in two places and
  will live in twenty with twenty processes, unless it is lifted into a
  library. Nothing checks that a new process has it.

**Kernel: the same two holes in every Type.**
- **A request can be changed before its owner submits it.** With only the
  right to edit fields, the agent raised the role on ana's draft to
  "admin" (and, on purchases, changed the amount and the vendor); ana
  submitted; what was frozen for approval was the agent's version. The
  kernel has no "only the requester edits a draft" rule.
- **A rejection reason can be planted.** The agent wrote a reason while the
  owner reviewed; the owner could then reject without giving one, though
  the Type requires it. The kernel has no "only the decider writes this
  field" rule.
- Both hold in the purchase, in the author's access Type and in the blind
  builder's: three Types, two authors. The kernel is consistent here in
  the wrong way: every Type inherits the hole, and no Type author can
  close it.
- *Mistakes in a Type the kernel did not catch.* The author's access Type
  let a request with no system be submitted; it then waits for ever with
  nobody able to decide. The blind builder avoided that, but left another:
  an owner asking for their own system waits for ever (the plain side ends
  it at once). On both Types an agent may ask for access in its own name;
  on purchases it cannot, by the accident of the HR check. The kernel's
  plain-language reading did not flag any of these.
- Everything else was refused, as in the first bake-off: approving,
  acting for the owner with the owner's grant, changing what was asked
  after submission, hiding the request, creating or re-owning a system.

**Side doors** (`results/sidedoors.md`), with no credentials at all:
- *Temporal.* Both sides run a development server with no sign-on, and on
  both it places an order nobody approved. On the kernel, a hand-started
  workflow claims the purchase is approved (the first bake-off's known
  hole; its fix, "an effect re-checks committed state", was not on `main`
  when this ran). On plain Temporal, the manager's approval is sent
  straight to the workflow, naming the manager, and the audit trail then
  shows the manager deciding. **As the task set, this does not decide the
  comparison:** it is the same open port on each side, closed the same way
  (a Temporal that checks who connects). With the kernel's fix the kernel
  would refuse its version; plain Temporal has no committed state outside
  Temporal to re-check against, so its version would need the Temporal
  server itself secured.
- *Postgres with the development password.* On plain Temporal it holds
  only a copy: changing an amount there made the manager's inbox show the
  wrong amount and let a stranger read the request, and decided nothing;
  the workflow kept the truth. On the kernel it holds the truth: whoever
  can write it can write any state (not done).
- Both APIs refuse a call without a token.

**Reading.** The brief asked not "can it pass?" but "what did passing take,
and did the second process get it right without being told?". It took
25 to 60 lines of rules per process, typed again each time, and yes: the
second process got it right. On this evidence plain Temporal is the more
consistent side at two processes, and the kernel's promise (a rule given
once to every Type) is only as good as the rules the kernel has; two it
lacks.

## Footprint, and what each side costs to maintain

Evidence: `results/footprint.md`, `results/start-*.txt`.

| | Kernel | Plain Temporal |
|---|---|---|
| Parts | Postgres, Temporal, 3 programs | Postgres, Temporal, 1 program |
| Memory, idle | About 187 MB | About 125 MB |
| Memory, after the tests | About 268 MB | About 172 MB |
| Start | 7 s | 6 s |
| Programs on disk | 120 MB | 38 MB |

Both run on the same two pieces of infrastructure, and a production
Temporal costs the same to run and secure on either side.

- **Plain Temporal, to maintain:** about 1,850 lines of the team's own Go
  for two processes, growing by 300 to 500 per process, each with tests
  that must be kept. Every process and every change needs a developer who
  knows Temporal's rules for workflow code (what may not change, how to
  version, when to replay). Safety rules are code to review in each
  process.
- **The kernel, to maintain:** 150 to 200 lines of YAML for two processes,
  and the kernel: 42,000 lines of Go with 74,000 of tests, one person's
  proof of concept, with its known gaps to close (the first bake-off's
  list, to which this one adds nothing new but confirms the first four).

## Gaps found

**On plain Temporal:**
1. Safety rules are copied between processes; nothing checks a new process
   has them. It held at two processes.
2. A change needs a developer who knows versioning; a careless change
   breaks requests already decided, silently at deploy.
3. Whoever reaches Temporal can decide anything, and the audit trail will
   name whoever they claim to be.
4. Who may see a request is decided from the copy in Postgres.
5. No search, no screens, no "acting for a person", no draft a requester
   can edit: none were needed here, each is code when it is.
6. A late update to a finished request answers "no such request" rather
   than a refusal that says why.
7. No plain-language account of a process: the code and its tests are the
   description.

**On the kernel** (all from the first bake-off; this run adds evidence, not
items):
1. An effect does not re-check committed state (the side door).
2. No "only the requester edits a draft"; no "only the decider writes this
   field". Now shown in three Types.
3. No inbox, no claim that binds.
4. A change reaches nothing in flight; an old version stays askable.
5. A Type cannot share anything with another Type, and cannot say where
   its Items may be created or who may ask.
6. The kernel's checks and readings did not catch a request that can never
   be decided.

## Assumptions, as they were made

1. The brief was not on `main`; it was read from `docs/plan-temporal-bakeoff`
   and not copied into this branch.
2. The first bake-off's pull request was not merged; this branch is built
   on `spike/camunda-bakeoff` and reuses its files by path.
3. **The precondition was not met:** the kernel's first hole ("an effect
   re-checks committed state") was not fixed on `main`. As instructed, the
   run went ahead, the kernel's forged-workflow-start is reported as a
   known open hole, and it is kept out of the comparison.
4. No `ANTHROPIC_API_KEY`: no live model. The deterministic stand-in was
   used; the injection run of the first bake-off was not repeated (the
   compromised-agent script is the stronger test and needs no model).
5. The plain side's agent is an outside program with an API token, not an
   activity inside the worker. Inside the worker it would hold the worker's
   connection to Temporal, which can do anything.
6. Step 2 was built by a separate agent, so that the builder's knowledge of
   the attack list could not leak into it. It is still an AI agent, working
   from code written minutes earlier by another: a best case for copying
   correctly.
7. The kernel's access request was rebuilt blind for symmetry. That builder
   reports its names were suggested by the agent script it was allowed to
   read; its design (a request inside a system Item) follows the kernel's
   own example Type.
8. The plain side keeps its inbox and audit trail in Postgres, written by
   the workflows, rather than using Temporal's own search: the brief names
   Postgres "for anything Temporal should not hold".
9. `purchase/change.go` and the `--purchase-code` switch exist only so the
   bake-off can run the program before the change, with the careless
   change, and patched, over the same histories. A real team has only the
   last.
10. Times are an agent's minutes on the session's clock. They say the two
    sides took the same order of effort; they do not say what a person
    would take.
11. Tests-first with the owner's approval was not possible in an
    autonomous run; tests were written with the code.
12. Elapsed: about 35 minutes on the session's clock for the build and the
    first full run, inside the half-day box.

## Not done

- A live model; the injection test.
- A shared library on the plain side (described under measure 3, not
  built), and a third process.
- Temporal's own sign-on (mTLS, an authorizer, Temporal Cloud) on either
  side: both used the development server.
- A screen, on either side.
- Load, and a long-lived change history (several versions of one workflow).

## The reviewer's notes, and what changed

REVIEWER-NOTES-PLACEHOLDER

## Recommendation

*(Draft, before the review.)* Where Temporal already runs and developers
will write the processes, **write the first few directly on Temporal, with
a deliberately small shared base, and do not adopt the kernel for them on
this evidence.** At two processes plain Temporal was the safer side, built
the inbox the kernel lacks, and changed a running process where the kernel
could not; its price is five times the text per process and a developer
for every process and every change. **Keep the kernel for what plain
Temporal cannot give:** processes that people who are not developers
describe, read back and change, checked before they run; and treat this
bake-off as its bar to clear first: an effect that re-checks state, "only
the requester" and "only the decider" on a field, a claim that binds, an
inbox, a change that can reach requests in flight. **What would change
this answer:** many processes or many authors (the copied rules are the
plain side's risk, and two processes cannot show how it grows); processes
that non-developers must change weekly; the kernel closing the gaps above;
or a team with no developer who knows Temporal's rules for workflow code.
