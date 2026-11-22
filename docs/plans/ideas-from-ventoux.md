# Ideas from Ventoux for the kernel

Status: proposal, 2026-11-22. Not settled design: `docs/design.md` wins, and
nothing here is built. Items 1 and 2 are agreed as the next work; the rest are
candidates, to be decided one at a time.

## Why

Ventoux (`francoisnyc/ventoux`, Python, case management on Temporal) spent
twenty layers on one question: what changes when something other than a
careful person writes the rules? The kernel will meet that question soon,
because Types are meant to be drafted by an authoring tool from plain English
as well as written by hand.

The kernel's core is the better base: one item service, Items, scopes and an
append-only history. So this note does not propose adopting Ventoux. It lists
the Ventoux ideas that would make the kernel a safe target for drafted Types,
and the ones to leave behind.

The test for each idea: would it help a Type written from plain English be
**checked before it runs, understood by the person who approves it, and
proven to do what was asked**?

## The gap these ideas close

Load-time checks prove that a Type is **valid** (fields exist, guards
compile, timers name declared moves, hook actions exist). They cannot prove
that it is **right**. Example: `vacation_request@3` loads cleanly, yet its
`deduct` reaction takes days for unpaid leave too, because nothing in it reads
`f_leave_type`. A Type without `except: [creator]` on approval would load just
as cleanly and let the requester approve their own request.

## Agreed next work

### 1. An action catalogue with contracts

**Today.** `internal/hooks/hooks.go` keeps a package-level map from an action
name to its code, the number of values it reads and writes, and whether it
has a side effect. `typedef` checks a hook's `reads` and `writes` against that
arity (`hooks.Arity`). Nothing says what each value is, what the action may
fail with, or how risky it is.

**From Ventoux** (layer 1, decision D17; `src/ventoux/core/contract.py`,
`catalog.py`, `contract_hash.py`): every activity has a contract, looked up by
`name@version`, with input and output schemas, a risk class, its effect (read
or write), retry policy and timeouts, idempotency, and its declared failure
outcomes. A reused `name@version` with different content is refused; a
contract whose hash changed without a new version fails CI; a side-effecting
contract without idempotency is refused.

**For the kernel.**
- Each action declares a contract: `name@version`, typed reads and writes
  (matched to field types at load, not just counted), side effect, risk,
  idempotency, and the reason codes it may answer with.
- Hooks name `action@version`. Versions are pinned like a Type's lifecycle.
- The catalogue is readable through the API (a `ListActions` call, or part of
  `GetType`), so an authoring tool knows the menu it may choose from, and can
  say plainly when a request needs an action that does not exist yet.
- Open: is risk a kernel concept, or only data a Type's rules and a reviewer
  read? (The membership test applies.)

### 2. Structured check results, and a published schema for Types

**Today.** `typedef` returns the first problem as a Go error string
(`typedef <id> v<n>: ...`), and `ParseDir` stops at the first bad file. Good
for a person reading a log; hard for a tool that must repair a draft.

**From Ventoux** (layer 3, `src/ventoux/core/validate.py`; layer 14): the
validator returns every issue as data, `{path, code, message}`, and the
definition format is published as JSON Schema generated from its models.

**For the kernel.**
- A check that returns **all** problems of a Type (or a folder of Types) as
  `{path, code, message}`: `path` points into the YAML
  (`transitions[3].allowed[0]`), `code` is stable and documented like the API's
  refusal reasons, `message` says what is wrong, then what to do.
- The same check behind an API call that **validates without registering**
  (a dry run of `RegisterType`), so a draft is checked against the Types
  already loaded (links, `accepts_reactions`, inverses) without changing them.
- The Type format published as JSON Schema, checked in CI against what
  `typedef` accepts, so the two cannot drift.

## Candidates for later

3. **Checks that prove properties** (Ventoux layers 3 and 10). Beyond
   consistency: every state reachable; every Item able to reach a done or
   canceled state; every action with a side effect behind an approval-like
   transition, when the catalogue says it is risky. Each is a refusal or a
   warning with a code.
4. **Trusted and untrusted values** (Ventoux layer 4, D12, D38). Values carry
   where they came from: a person on staff, a customer, a hook, a model. A
   rule fed by an untrusted value cannot decide. Kernel example: a customer
   sets `f_priority` on `incident@2` (`customer_fields`), and the priority
   chooses the SLA targets.
5. **Stories in simulated time** (Ventoux D45, `ventoux simulate`,
   `examples/*/stories/`). A walkthrough as data: the Type, the people and
   their roles, fakes for every action, a timeline ("day 2 09:00, bob
   approves"), and the **whole** expected timeline, so an unexpected event
   fails it. Run in memory with simulated time, as tests. Complements
   `scripts/scenario.sh`, which needs the stack and the dev clock. Stories are
   written or approved by people from the plain-English request, never
   generated alongside the Type they test.
6. **Plain-language readings, each with a witness** (Ventoux D45, D56). Every
   state, transition, hook and reaction rendered as one sentence from the Type
   alone, and each kind of sentence backed by a test that runs the engine and
   shows it does what the sentence says. This is what a person approves.
7. **A semantic diff between Type versions** (Ventoux layer 13). By effect,
   not text: "the requester may now approve", "a reaction now writes f_used
   on unpaid leave". A loosening is always reported, never hidden.
8. **Patterns that expand into rules** (Ventoux D46: `sla`, `reminder`).
   `incident@2` repeats the same four-way priority expression four times; a
   pattern would state it once and give an authoring tool a smaller, safer
   vocabulary.
9. **A model suggesting moves on Items** (Ventoux layer 15, the step advisor;
   `src/ventoux/authoring/advisor.py`). The model is shown the Item as the
   person sees it, never a value they cannot see, and answers with one move.
   The answer is checked by the lifecycle and `allowed` as a dry run; a move
   that would be refused is never offered. A person makes the move, and the
   event records `suggested_by`. The kernel already has most of the
   foundations: agents act in sessions with scopes and powers, a `field:`
   entry never matches an agent, and widening needs a person's approval
   (`NEEDS_APPROVAL`). What is new is a suggestion that is not a write, and
   its record in history.
10. **Smaller habits.** Messages to people are drafts unless a Type says
    otherwise; and "fail closed" as a written rule with a negative test for
    every evaluator.

## Left behind, on purpose

- **Moving open cases onto a new definition** (Ventoux layer 12). The kernel
  pins an Item's lifecycle to its Type version; keep that.
- **The Python builder** (Ventoux layer 14). A second way to write Types, and
  one that must never run on model output. Drafts stay data.
- **Ventoux's JSON expressions instead of CEL.** CEL is sandboxed and
  compiled at load. What is worth keeping is the closed list: the CEL
  functions a Type may call are declared, and a new one is a design decision.
- **Ventoux's case engine.** Items, scopes and the item service already cover
  it.
