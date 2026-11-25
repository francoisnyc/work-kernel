# The case reacts to its children: joins and sentries, declared on the case

Status: proposal, 2026-11-24. Needs the owner's OK before it touches
`docs/design.md`. It belongs to theme 1's open-ended case features, after the
decision point (`docs/roadmap.md`), unless track 2's "third process of a
different shape" needs it first, which would also be its test.

## The idea in one paragraph

A case is an Item; its tasks are Items inside it. Today a task that finishes
nudges its case, and the case's guard decides whether the case moves on: the
join is spread over two Types. This proposal lets the **case** declare how
it reacts to its children's lifecycle: "when a child of these Types enters
these states, and this condition holds, move me (or create that task)". One
feature then gives joins written where they belong and the sentries open-ended
cases need, and it keeps the rule that triggers hang off the lifecycle (a
child entering a state is a lifecycle event).

## What exists today, and works

The access request with several approvers (`types/access_request.v1.yaml`,
`types/approval.v1.yaml`, built in Jira 2.4b):

1. **Split.** Entering `pending`, the request creates one `approval` per
   approver (`kind: create`, `for_each: f_approvers`).
2. **Nudge.** Each approval, entering `approved` or `rejected`, tries to move
   its container (`kind: move, target: container, to: approved,
   target_from: [pending], on_guard: skip`); the request consents
   (`accepts_reactions`).
3. **Join.** The request's transitions out of `pending` are guarded by counts
   over its children (`children.approval.filter(c, c.state == "approved")
   .size() >= f_required`; rejected as soon as one is). Each nudge is a move
   of the request, under its row lock, so two simultaneous last decisions are
   judged in turn: one skips, one moves.
4. **Cleanup.** Leaving `pending`, the request moves the leftover approvals
   to `canceled`.

What to keep from it: branches as Items, the join condition in one place on
the case, correctness from the case's lock, a manual move giving the same
result as the automatic one, and the cleanup.

## What it costs

1. **The child knows its parent's shape.** The approval names the request's
   states (`to: approved, target_from: [pending]`), so it serves one case
   Type. A reusable task Type (a review, an approval, a document request)
   needs the parent to own the logic, as BPMN puts the join gateway in the
   parent process and CMMN puts sentries on the case.
2. **It is hard to read.** "What happens when the approvals are in?" is two
   files and four declarations: hard for a plain-English author, for the
   readings (item 6) and for a diagram of the Type.
3. **A skip hides mistakes.** `on_guard: skip` treats "not yet" and "never"
   alike: a wrong guard or a misspelt state leaves the case unmoved without a
   word, against fail-closed; and every join of N leaves N-1
   `reaction_skipped` events.
4. **Analysis needs the whole picture.** Path rules checked at load (Ventoux
   item 3) can prove "every path to `approved` has enough approvals" only if
   the join is declared on the case.

## The proposal

A reaction on the case, triggered by its children:

```yaml
reactions:
  # The join: decide when the approvals allow it.
  - id: decide_approved
    kind: move
    on: child_entered            # a child of this Item entered a state
    of: [approval]               # of these Types
    states: [approved]           # entering these states
    from: [pending]              # while this Item is in these states
    to: approved
    when: 'children.approval.filter(c, c.state == "approved").size() >= f_required'
  - id: decide_rejected
    kind: move
    on: child_entered
    of: [approval]
    states: [rejected]
    from: [pending]
    to: rejected

  # A sentry: when the police report is in, open the assessment.
  - id: open_assessment
    kind: create
    on: child_entered
    of: [document]
    states: [received]
    when: 'child.f_kind == "police_report"'
    type: assessment
    version: 1
```

**Rules:**

- **What triggers it:** a child of one of the `of` Types (an Item contained in
  this one) entering one of `states`. `on: created_inside`, which exists,
  stays for "a child was created".
- **What it reads:** `when` reads this Item, `child` (the one that entered)
  and `children.<type>`, as guards do today. A `when` that is false does
  nothing and records nothing: "not yet" is not an event.
- **What it may do:** `kind: move` of this Item (no consent needed: it moves
  itself) and `kind: create` inside it (the created Type consents, as today);
  `kind: set` on this Item. Moving the children stays the existing
  `children:` move, with their consent.
- **Fail closed when it matters:** a `when` that holds but a move the
  transition refuses (its guard, `allowed`, a frozen field) is
  `reaction_failed`, visible on the Item, never a silent skip. A reaction
  that can never fire (an `of` Type that cannot be created inside this one,
  a state the child Type does not have, a `from` with no transition to `to`)
  is refused at load.
- **Concurrency:** judged by the item service under this Item's row lock, so
  the last two children entering together are judged in turn.
- **Delivery:** the child's event is delivered as reactions are today (the
  child's workflow asks the item service), with an idempotency key from the
  child, its event and the reaction: exactly once. Cascades keep the
  existing `hop` limit (8).
- **Compatibility:** additive. The child-side nudge with `on_guard: skip`
  keeps working for the Types that use it; new versions may move to this.

## How BPMN and CMMN map onto it

| BPMN / CMMN | In the kernel |
|---|---|
| Sequence | One Item's transitions |
| Exclusive gateway | Transitions out of one state, each with a guard |
| Parallel split | A `kind: create` reaction with `for_each`, or several creates |
| Parallel join, "n of m", "any rejects" | `on: child_entered` with a `when` over `children` |
| Terminate the rest | The existing `children:` move to `canceled` on leaving |
| CMMN discretionary task | A child a person may create, allowed by starting work's `start` block |
| CMMN sentry (entry criterion) | `on: child_entered` or `created_inside`, `kind: create` |
| CMMN milestone | A state of the case, reached by such a reaction |
| Timer events | State timers and acting timers |
| Machine-only parallel steps | Inside a hook's activity, as Temporal code, not Items |

The difference from BPMN's tokens: a branch is an Item a person or an agent
works, visible in a queue, with its own rules and history; a join is a
condition over Items, not tokens arriving.

## Tests it would come with

- The last two children entering together: exactly one move.
- A `when` that is false: no event on the case.
- A `when` that holds and a refused move: `reaction_failed` with its reason.
- One task Type (an approval) used by two case Types, with no reaction of
  its own.
- Load refusals for each "can never fire" case above.
- The access request rewritten this way passes its existing tests.

## Open questions

1. The name: `child_entered`, or `on: enter` with `of:` meaning children?
2. Should a false `when` be visible somewhere for debugging (a trace, not an
   event)?
3. Several reactions matching one child event: in declaration order, each
   judged after the last has written?
