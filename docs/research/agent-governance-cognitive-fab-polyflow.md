# Agent governance on Temporal: Cognitive Fab's polyflow

2026-11-24. Not Holunda's Polyflow (the task pool, in
`task-inboxes-taskana-polyflow.md`): a different project with the same name.
Read from its page ([cognitivefab.com/tools/polyflow.html](https://cognitivefab.com/tools/polyflow.html))
and a shallow clone of [cognitive-fab/polyflow](https://github.com/cognitive-fab/polyflow)
(`main`, 2026-09-23), including its platform notes (`docs/platform/`).
It calls itself experimental, unproven and not peer-reviewed; its own
platform notes say many of their sources are single-author preprints.

## What it is

**A workflow engine for AI agents, as an MCP server** (Apache-2.0). The agent
runs a small state machine instead of choosing one tool call at a time:

- **Admission by path checking.** A workflow ships rules ("invariants"),
  small predicates over an emission path, for example "every `post_brief` has
  an `APPROVED` before it" and "at most one post per path". At startup the
  engine enumerates every reachable path over the finite domains the
  workflow's contract declares, and a workflow that breaks a rule is not
  registered. It states the limit: a consistency check over declared domains,
  not a proof.
- **Work orders.** The engine names the next action and its payload; the
  agent carries it out with its own tools, permissions and credentials, and
  reports; the engine decides what comes next. The engine holds no
  credentials and calls nothing itself.
- **Stale reports are observable rejections, not faults** (post equals pre),
  which makes an agent's at-least-once reporting safe.
- **A workflow is six files:** a contract (state keys, actions, finite data
  domains, terminal states), a state machine, a pure effect mapper (state
  change to work orders), an effect manifest (retries, success and failure
  actions), the invariants, and a descriptor.
- **Its evidence:** 48 runs on two agent harnesses. A scheduled job fired
  twice: without the engine the agent posted twice in 8 of 8 runs, with it 0
  of 8. No run posted without approval, with or without it.

**polycrew**, a sibling, adds several people or sessions on one run: a shared
broker, orders they claim, a page a person watches.

**"Polyflow for Temporal"** (Business Source License 1.1, converting to
Apache-2.0 after four years): a worker plugin that governs agents running on
Temporal without changing their code, in levels: G0 records every effect on
a hash-chained, signed ledger in activity headers; G1 checks rules before
each effect (requires-prior, at-most, budgets, rates, approvals); G2 lets a
certified state machine decide the next step; G3 refuses a machine whose
bytes differ from its certificate and vets a new version against runs in
flight. Its stated aim is acquisition by Temporal.

## What its research says about the field

From its platform notes (`docs/platform/00-findings.md`, section 2.7; not
verified here):

- **Temporal's own Agent Harness** (previewed 2026-08-20) ships tool-call
  approval policies, and lists policy engines as future integrations.
- **OpenBox** holds the governance slot in Temporal's AI partner programme
  (joint launch 2026-07-13) and fails open by default.
- **Tenuo** carries signed, attenuated warrants in Temporal headers.

Its conclusion: runtime policy on agent tool calls is becoming a feature
Temporal will own; what is left is checking a workflow before it runs and a
new version against runs in flight.

## How it relates to the kernel

**The same belief:** rules decide, not the model, and they are checked
before anything runs.

**A different centre:** it governs an agent's run, step by step. The kernel
governs the work: Items, people and agents acting on them, who may see and
decide what. Neither does the other's job; neither has an inbox of human work.

**What this means for the positioning** ("the human-and-agent work layer for
Temporal", `docs/roadmap.md`): governance of agent *tool calls* on Temporal
is crowded and moving into Temporal itself. The kernel's part, the work
people and agents share, with declared rules, an inbox and binding
decisions, is not what any of them builds. Say so plainly in the pitch.

## Worth borrowing

1. **Path rules checked at load.** A Type is a finite state machine, so rules
   over its paths are checkable before it runs: "every path to `ordered`
   passes `approved`, made by a person", "no path sends twice". Concrete
   shape for Ventoux item 3 (checks that prove properties), stated as
   polyflow states it: exhaustive over what the Type declares, not a proof.
2. **A new version vetted against Items in flight** (its G3): before open
   Items move to a new version, check each one's state and history against
   the new version's rules. For track 1, item 6.
3. **Work orders for agent tasks.** When the kernel asks an agent to do a
   task, it names the action and inputs; the agent reports the result. Stale
   or duplicate reports are refused observably (the kernel already refuses a
   transition from the wrong state; say so as a rule for agent reports). For
   theme 3 and the context pack.
4. **Approvals bound to one action and consumed once.** The signed-link
   fallback for gated moves (roadmap, "work done from a chat") should say
   both.
5. **The re-fire test.** Fire a scheduled job twice and count the duplicate
   effects. Add it to the bake-off scripts for the kernel's timers and hooks
   (it would also test the timer follow-up to the effect re-check).
6. **A hash-chained ledger of effects,** for tamper-evident audit. Later,
   with the event stream.
