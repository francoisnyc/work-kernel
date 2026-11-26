# The Poly stack (Cognitive Fab): its design, and what the kernel could take

2026-11-26. Read from a shallow clone of
[cognitive-fab/poly](https://github.com/cognitive-fab/poly) (v1.7.0,
2026-11-08): the README, the functional specification, the architecture and
technical specifications (version gate, gate, checks), `STATUS.md`, the M3
decisions and the paper draft. Builds on
`agent-governance-cognitive-fab-polyflow.md`, which covered polyflow alone.
The project is by one pseudonymous author, Cognitive Fab LLC, and was
written largely with coding agents (about 1,400 commits in six weeks); its
documents call it unproven, and its own experiment did not show a benefit
(below).

## What it is

**poly is a language and runtime for the stateful core of software that
coding agents write.** A program is a set of declared state machines. Each
carries its state (typed fields with finite *check domains*), actions, one
handler per action (requires first, then writes; accepted or rejected with a
named reason), properties, effects and the policies that govern them. The
compiler explores every reachable state over the declared domains before the
program may run; the runtime executes only what was checked ("admitted"),
gates every effect, and journals every step so a run replays exactly.

It is a front end over an older set of engines, each lowering target an
existing tool:

| Engine | Answers |
|---|---|
| polygraph (with polygen, polyrun, polynv) | Does every reachable state satisfy the invariants? What may the machine emit, on every path? Are the invariants adequate (by mutation)? |
| polyvers | Does this version break the runs already in flight? |
| Polyaxion | May this effect happen now, given what already happened? (the gate) |
| polycheck | Does any path reach credential egress with no gate? |
| polyflow, polycrew | Workflows whose effects are work orders that agents or people claim, run and report |
| Polyx, polyness | Rules and workflows mined from journals, proposed to a person |
| Polytrion | Simulation (private, v2) |

Its principles, abridged: the semantics is the definition; one program, three
interpretations (checked, simulated, run); machines talk only by messages;
one source per machine; **no silent clean** (every check says clean,
findings, refused, underpowered or inconclusive); **no model call on a check
or decision path**; the journal is the corpus.

## The ideas, one by one

- **Checking behaviour, not structure.** Every machine is checked for
  *deadlock* (a reachable non-terminal state where no action is accepted)
  with no declaration needed. Authors add invariants, step properties,
  reject properties, *path properties* over emitted effects (`count(charge)
  <= 1`) and liveness under a stated fairness. A finding comes with a
  *witness*: the shortest concrete sequence that breaks the rule, exportable
  as a test, and typed fixes guarded by a digest of the node they edit.
- **A cheap lint with a deep cause.** `EMIT_WITHOUT_STATE_CHANGE`: a step
  that emits an effect and leaves the state unchanged can be repeated without
  limit. The README's example is a refund paid twice.
- **Configurations.** Several machines checked together with messages
  arriving in any order: two orders confirmed against the last unit of
  stock, a defect no single machine shows.
- **Check domains, strict by default.** A parameter's finite domain is a
  constraint at dispatch, not just a hint to the checker: a value outside it
  is refused ("outside-check-domain") unless the author writes `disclosed`.
  This default was *changed* after the experiment: every defect the poly arms
  shipped traced to the earlier default.
- **Effects with a class and labels.** Each effect declares its class
  (none, reversible, compensable, irreversible), labels from a closed
  vocabulary (`egress:external`, `read:private`), targets, completion
  actions and retries. A handler receives only the capabilities its labels
  and targets grant.
- **Three-valued policies.** Policy inputs can be missing, so policies use
  true, false and unknown; an unknown never satisfies a requirement, and a
  deny rule whose condition is unknown *escalates* to a person by default.
  Escalations time out to deny.
- **People sign, agents cannot.** Accepting a finding and approving an
  escalation need a key registered to a person; an agent's key cannot sign
  either. Signed policies are the owner's: "Never edit a signed policy or
  accept a finding".
- **A version gate against live runs (polyvers).** A new version is checked
  against snapshots of running instances in lanes (semantic, shape,
  migration, vocabulary, intent). Renaming a rejection reason is a breaking
  change. A new field with no migration is a hole that is refused. An empty
  corpus is refused rather than passed. Each instance is then auto-upgraded,
  migrated or pinned.
- **Seeded defects.** A defect patch seeds one fault and names the finding
  the check must report; a fault no longer found means a property stopped
  seeing what it was written for.
- **For agent authors.** `poly guide` prints instructions for a coding
  agent; every command is on the command line, over MCP and in a language
  server; output has a JSON form; findings have stable codes.
- **A hash-chained ledger** per run with signed heads and a periodic signed
  anchor, so no run can be dropped unseen.

## The evidence, honestly

The project pre-registered its decisive experiment (M3, 2026-10-04): ten
stateful tasks, 200 runs, a coding agent writing TypeScript alone,
TypeScript checked after the fact, and poly in two surfaces. Escaped defects
per task were 0, 0, 1.04 and 1.08; the TypeScript baselines had none, so no
comparison was possible, and every poly defect traced to one language
default. The poly arms used about 3.5 times the tokens of TypeScript alone.
On the good side, 100 of 100 poly runs produced an admitted program, with no
poly in the model's training data. The paper reports the null result plainly.

Two lessons for the kernel: formal machinery is not free (tokens, time,
another language), and a default that is not fail-closed is where the
defects went.

## How it relates to the kernel

The same family: declared state machines, a durable runtime, gated effects,
agents as authors and actors, a refusal to let a model decide. The aims
differ. poly checks *code* that coding agents write, exhaustively, before it
runs. The kernel judges *work* that people and agents do, under rules, one
write at a time. poly has no notion of who acts (principals, roles,
`writable_by`, the requester, a claim); the kernel has no notion of checking
a Type's behaviour before it runs: its load checks are structural (every
state, transition and field a rule names exists, in `internal/typedef`), and
its stories test the paths someone thought to write.

That is the gap poly points at.

## What the kernel could take

In order of value for the cost:

1. **A behavioural check of a Type at load, `pli check`, over its control
   part.** Explore the reachable states of one Type over a small abstract
   world: its states; its enum and boolean fields; a few principal kinds
   (the requester, a person holding each role, an agent holding each role,
   someone else, the kernel's own timers and reactions); a claim holder;
   children counted up to a small bound. Evaluate guards with CEL over those
   values; treat a guard over an unabstracted field (an amount, a text) as
   either way, and say so ("inconclusive over `f_amount`"), never clean.
   Report, each with the shortest witness:
   - **stuck states:** a reachable state that is not done or canceled, out
     of which no principal kind, timer or reaction can move. This is the
     bake-off's "access request for no system: nobody owns the decision",
     found before anyone files one;
   - **who can reach what:** a decision state an agent can reach without a
     person's move; a requester who can approve their own request; a
     colleague who can change someone's draft. These are the attack scripts'
     rows, found by the checker instead of written by hand;
   - **dead rules:** a transition, reaction or timer that can never fire.

   A witness prints as a story, the kernel's existing test format, so a
   finding becomes a test in one step, and as plain words a non-developer
   can read: "Ana submits; an agent with the approver role approves". That
   is what track 2's author needs as much as the reading: not only "what the
   rules say" but "a path you may not have meant".
2. **A repeat-effect lint, now, with no checker:** an action hook on a
   transition that leaves the state unchanged, or on a state a move can
   re-enter, with no record of having run, can repeat its effect. The
   effect re-check (track 1, item 1) and its timer follow-up meet the same
   cause from the runtime side.
3. **No silent clean, everywhere a check reports.** A load check, a reading
   that falls back to "a rule over … holds", a dry run of a version move
   over no Items: each says *inconclusive* or *underpowered*, never fine.
4. **For track 1, item 6 (moving open Items), from polyvers:** renaming or
   removing a refusal reason or a state counts as breaking; a field the new
   version adds with no value for open Items is refused, not defaulted
   silently; a dry run over an empty set of Items is refused; and each Item
   ends moved, pinned to its version, or listed as needing a person.
5. **Effect class on action hooks:** `irreversible` (a payment, a message
   to a customer) asks for an at-most-once proof from item 1's re-check and
   the lint above; `compensable` must name its undo, which the kernel's
   undos already model. A small, opt-in key.
6. **Unknown escalates to a person,** as a choice a Type can make, beside
   today's fail-closed refusal: where a guard cannot be evaluated (a missing
   field, an external answer not yet in), route the move to an approval
   instead of refusing it outright. It meets "ask a person" (`question@1`,
   `approval@1`).
7. **People sign Types.** Registering a Type, or accepting a known finding
   in one, needs a person; an agent may draft Types but never register one.
   This closes the authoring path to injection the way poly closes its
   policies.
8. **Seeded defects for the kernel's own checks:** each bake-off attack is
   already a seeded defect; keeping them as a suite that must stay found
   guards the checks the way poly's `--defects` does (and as the
   "Promises" table of a design note does in another project).
9. **For agent authors:** a `pli guide` for coding agents that write Types,
   stable codes on every load error, fixes where one is clear, JSON output.
   It belongs with "Developer adoption" in the roadmap.

## What not to take

- **A language of its own.** The kernel's contract stays YAML and CEL; poly's
  own experiment shows the cost of a new language for its authors.
- **A stack of engines.** poly folds about ten engines, under three licences
  (one private), behind one command. The kernel's checker should be one Go
  package over the Type it already loads.
- **Exhaustive checking of data.** Work items carry amounts, dates and text
  with no small domain. The check is over the control part (states, who
  acts, enums, booleans, small counts) and says *inconclusive* about the
  rest. It is a bug finder for rules, not a proof.
- **Configurations of many Types,** until one Type's check has paid for
  itself. The case and its children are serialized by the case's lock, so
  the arrival-order defects poly finds between machines are mostly
  prevented at run time; a join that can never complete (a stuck case) is
  the one worth checking first, and a pair of Types (`access_request@1` with
  `approval@1`) is the first configuration if it comes.

## Ventoux already does much of this, more cheaply

The owner's other project, ventoux (case management on Temporal, in Python),
built a behavioural check of its definitions in its validation layers
(`src/ventoux/core/validate.py`, `src/ventoux/case/reach.py`,
`src/ventoux/case/assignment_checks.py`; brief, D34). Its method differs from
poly's, and suits the kernel better:

- **Over-approximate, then claim only what holds on every path.** Every
  condition is taken as possibly true and every outcome as possible. What is
  still unreachable can never happen on any path, so it is an **error**;
  what depends on data or on who holds a role is a **warning**, which lets a
  definition through but is shown. An issue that says neither is an error
  (fail closed). No domains to declare, no state explosion over data, and no
  "clean" claimed beyond what was proven: a fix in ventoux restored exactly
  that when a check over-claimed (`docs/layers/fix-check-proofs.md`).
- **What it proves, as built:** every state reachable (`UNREACHABLE_STATE`),
  the plan able to end from every reachable state (`NO_PATH_TO_FINAL`), steps
  that wait on each other (`DEADLOCK`), what can never start, fire or close
  (`NEVER_STARTS`, `NEVER_FIRES`, `NEVER_CLOSES`), milestones never reached;
  every high-risk activity behind an approval on every path, with the
  shortest unapproved path as the witness, and one approval never unlocking
  two high-risk runs; who is asked: a rule that can pick nobody
  (`MAY_ASSIGN_NOBODY`), or someone the approval excludes; where a deciding
  value came from (`OPENER_DECIDES`, `WRITER_DECIDES`,
  `UNTRUSTED_TRANSITION`).
- **The case's reach is a fixpoint** over steps and milestones grown from
  nothing until a round adds nothing, so steps that wait on each other never
  get in.

For the kernel this means `pli check` is a port of a method already built
and tested, not research: the same two severities, the same "possibly true"
reading of guards, reachability forwards (from the initial state) and
backwards (from done and canceled), the shortest path as the witness. What
the kernel adds is the actor: each arrow is a transition *and the principal
kinds that may take it* (`allowed`, `except`, `writable_by`, a claim, the
kernel's own timers and reactions), so "an agent can reach approved without
a person" is a reachability question over arrows an agent may take. With
guards taken as possibly true it is a warning; with no guard on the path it
is an error. Port the method, not the code: ventoux is Python over its own
plan format, the kernel is Go over Types.

## Where it would sit in the roadmap

Agreed by the owner (2026-11-26): the trial goes ahead as part of track 2's
preparation (roadmap, "Track 2"). The checker is not a track 1 item. It strengthens track 2,
the authoring test, and the decision point's question of whether rules as
data are safer than code: a non-developer reading a witness in plain words is
the strongest form of "checked before it runs". A first spike, the stuck
states and "who can reach what" over one Type, would show its worth on the
bake-offs' Types. The repeat-effect lint (2) and no silent clean (3) are
small enough to ride with work already planned.
