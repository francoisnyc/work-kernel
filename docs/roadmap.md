# Roadmap

Status: proposal, 2026-11-22. Where the kernel is headed, and in what order.
`docs/design.md` holds the settled design and the step-by-step build orders;
this file is the direction those steps serve. It changes when the direction
does, not with every step.

## What the kernel is for

> A kernel for any kind of work (workflows, case management, issue tracking,
> security operations), agent-ready by design: a non-developer describes an
> automation in plain English, an assistant builds it, the kernel checks and
> explains it, and nothing in the data it handles can make it do what its rules
> do not allow. A task in it can be done by a person or by an agent.

Four properties follow from it:

- **General.** One small kernel; each domain is Types and apps, never a kernel
  change of its own (the membership test in `docs/design.md`).
- **Authored in plain English, safely.** A Type drafted by an assistant from a
  non-developer's description is checked, explained and tested before it runs.
  For a non-developer, the plain-language reading and the test story are the
  review.
- **Resistant to prompt injection by design.** Text from outside (an alert, an
  email, a customer's reply) and anything a model produced from it never
  decides a move, chooses a recipient or triggers an action. This is enforced
  by the kernel, not asked of a model.
- **Agents as task performers.** A task can be done by a person or by an
  agent. An agent's output is untrusted: it proposes (a summary, a next step,
  a draft), and the Type's rules decide what that proposal may change.

## Six months from now

Done when: **in each of four domains (workflow, case management, issue
tracking, security operations), a non-developer's plain-English description
becomes Types the kernel checks, explains and tests; each runs with at least
one agent task; and an injected instruction planted in its data changes
nothing its rules do not allow.**

## What exists already

- **Three domains proven:** the vacation request (workflow), the IT desk with
  SLAs and a customer portal (case management), the PLAT project (issue
  tracking).
- **Parts of the injection defence:** an agent never passes a gate a Type
  gives to a person (`field:` never matches an agent); an agent's write needs
  approval when it would widen who sees what it read; hooks read and write
  only what they declare; a connector's answer is a reason code, never stored
  free text; every outbound path goes through `policy.Project`.
- **Parts of safe authoring:** every problem of a Type as data (V2a).

## Themes, in order

### 1. Safe authoring in plain English

`docs/plans/ideas-from-ventoux.md`, in the order the "Ideas from Ventoux build
order" in `docs/design.md` sets.

- **Now:** V2b (`ValidateTypes`, `pli types validate`), V2c (the Type format
  as JSON Schema).
- **Next:** V1, action contracts (the hook protocol is their first version):
  the catalogue an assistant chooses actions from.
- **Then:** plain-language readings with witnesses (item 6), stories in
  simulated time (item 5), a semantic diff (item 7), patterns (item 8), checks
  that prove properties (item 3). Together they are what a non-developer
  reviews instead of YAML.

### 2. Injection resistance by design

Ventoux item 4, made central.

- **Where each value came from**, carried with it: a person on staff, a
  customer, an outside system, a model. Kept in the kernel, checked at load
  and at write.
- **Untrusted values decide nothing:** a guard, a transition's `allowed`, a
  recipient or an action's input that depends on one is refused when the Type
  loads, or held for a person's approval.
- **A test domain built to be attacked:** security operations (below), where
  the data is attacker-controlled by nature.

### 3. Agents as task performers

- **An agent task:** a step whose performer is a model, declared like a hook
  with a contract (V1): its inputs, a typed output, and that output marked
  untrusted. It proposes; the rules decide. (Ventoux's step advisor, layer 15,
  made general.)
- **A model suggesting moves on Items** (item 9).
- **Agents building this kernel:** `build_step@1`
  (`docs/plans/build-step-type.md`) and the autonomous loop
  (`docs/plans/autonomous-loop.md`), the first users of agent tasks and their
  gates.
- Later: action hooks and async completion, so a step's own Type starts the
  agents it needs.

### 4. A fourth domain: security operations (SOAR)

An alert arrives, is enriched by outside systems, triaged (by an agent task),
and contained (an action with a side effect, behind a person's approval).
The domain is chosen because it is the hardest test of theme 2: an alert's
text is written by an attacker. Built as Types and an app, like the others,
counting every kernel change it needs.

### 5. The basics for real use

Only what the domains force, in the order they force it. Likely first:

- real sign-on, replacing dev tokens;
- Watch and notifications (today every client polls);
- groups and grants, and readers taken from a field;
- issue keys, totals, `update_time`, search inside a container (the PoC apps'
  list in `docs/design.md`, "PoC apps as built: what the API lacked").

### 6. Tracking agents' work as a product

Parked. The ORI tracker stays the internal tool this kernel is built with.
Trackers that assign work to agents already exist (Linear, Jira, GitHub, and
many open-source ones); what the kernel adds, rules an agent cannot get
around, is better shown in themes 2 and 3.

## How it is tracked

The tracker stack's ORI space holds the work: one epic per theme above, the
ten Ventoux items as their own epics until there is an `initiative` Type to
group them, stories and, once built, build steps.
