# Roadmap

Status: proposal, 2026-11-22. Where the kernel is headed, and in what order.
`docs/design.md` holds the settled design and the step-by-step build orders;
this file is the direction those steps serve. It changes when the direction
does, not with every step.

## What the kernel is for

> A kernel for work that people, tools and agents can drive safely: proven
> first by rebuilding our Bonita workflows, with Types drafted in plain
> English by the team's authoring tool.

Three things follow from it:

- **Safety is the product.** Rules live in Types, as data, and the kernel
  enforces them the same way for a person, a tool or an agent. Whatever drives
  the work cannot talk its way past a rule.
- **Real workflows decide what to build.** The Bonita processes are the test:
  what they need is built, what they do not is Later.
- **Types are written by tools as well as by hand.** A Type drafted from plain
  English must be checked, explained and tested before anyone relies on it.

## Six months from now

Done when: **three of our Bonita workflows run on the kernel for real users,
at least one of them drafted from plain English and checked by the kernel
before it ran.**

## Themes, in order

### 1. The Bonita rebuild

The company's real Bonita processes, mapped against the BPMN pattern table,
then rebuilt (the "Bonita" row of the proof of concept build order in
`docs/design.md`).

- Choose two or three processes to rebuild first, and write each down in plain
  English before writing its Type: the description is also a test case for
  theme 2.
- Start from what the vacation request and the Requests area already found
  missing, marked **Bonita** in `docs/design.md` ("The Requests area"): hooks
  in `GetType`, a balance check at submit, a decision comment, an approver who
  can see the requester's balance.
- Each process pulls in the basics it needs from theme 4, and nothing more.

### 2. A safe target for Types written by tools

`docs/plans/ideas-from-ventoux.md`, in the order the "Ideas from Ventoux build
order" in `docs/design.md` sets.

- **Now:** V2b (`ValidateTypes`, `pli types validate`), V2c (the Type format
  as JSON Schema). V2a is done.
- **Next:** V1, action contracts. The hook protocol is their first version.
- **Then, as the Bonita rebuild shows the need:** checks that prove properties
  (item 3), trusted and untrusted values (4), stories in simulated time (5),
  plain-language readings with witnesses (6), a semantic diff (7), patterns
  (8).

### 3. Agents driving the work

Used first to build this kernel, so the kernel is tested by its own builders.

- `build_step@1` (`docs/plans/build-step-type.md`): the build process as a
  Type, whose gates an agent cannot pass.
- The autonomous loop (`docs/plans/autonomous-loop.md`): an orchestrator, a
  builder and a separate reviewer, with the owner approving at the gates.
- Later: action hooks and async completion, so a step's own rulebook starts
  the agents it needs; a model suggesting moves on Items (item 9).

### 4. The basics for real use

Only what the Bonita processes force, in the order they force it. Likely
first:

- real sign-on, replacing dev tokens;
- Watch and notifications (today every client polls);
- groups and grants, and readers taken from a field;
- issue keys, totals, `update_time`, search inside a container (the PoC apps'
  list in `docs/design.md`, "PoC apps as built: what the API lacked").

### 5. Tracking agents' work as a product

Parked. The ORI tracker stays the internal tool this kernel is built with,
and the proof that agents can be governed by it. Trackers that assign work to
agents already exist (Linear, Jira, GitHub, and many open-source ones); a
product here would pull effort away from themes 1 to 4.

## How it is tracked

The tracker stack's ORI space holds the work: one epic per theme above, the
ten Ventoux items as their own epics under theme 2 until there is an
`initiative` Type to group them, stories and, once built, build steps.
