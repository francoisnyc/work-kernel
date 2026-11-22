# Proposal: `build_step@1`, the way this repo is built, as a Type

Status: proposal, 2026-11-22. Nothing here is built, and the YAML below is
not in `types/`. `docs/design.md` and `CLAUDE.md` win.

## The idea

Track the kernel's own work in the kernel. The PLAT Types (`space`, `epic`,
`story`, `sprint`) already make a small tracker. This adds one Type for how
a step of this repository is built, so that the process in `CLAUDE.md`
("How we work") is a rule the kernel enforces, not only an instruction an AI
session is asked to follow:

1. Questions first: the session lists its questions, and waits for answers.
2. Tests first: the session writes the failing tests and waits for an OK.
3. Tests are protected: once agreed, changing them needs the OK again.
4. Small commits, a pull request, a review.
5. End of session: a journal entry.

The property that makes this work is already in the kernel: **a `field:`
entry never matches an agent** ("Policy", "On behalf of"). An AI session
working as `agent:<name>` for the owner can do every step of the work, but
it can never make a move that only `field:f_owner` may make, so it can never
approve its own questions, tests or pull request.

## Where it runs

Not on the dev stack: `make reset` deletes everything, and the scenario and
the e2e journeys write their own data into it. The tracker runs on a second,
long-lived stack, pinned to a known-good commit, in its own namespace
(`dev`), with an `ORI` space. Changing the kernel never breaks the tracker
until the tracker's stack is moved forward, on purpose.

`design.md` stays in git, reviewed in pull requests: it is the spec. The
work (steps, stories, the journal) moves into the tracker.

## The Type

```yaml
# A build step of this repository (proposal): how one step is built, from
# questions to a merged pull request. Only the owner, a person, answers,
# agrees and accepts; an agent working for the owner does the rest.
id: build_step
version: 1
initial_state: questions

fields:
  - {id: f_title,     type: string}
  # The person who answers, agrees and accepts. Only a maintainer sets it,
  # so an agent (which holds no role) can never name itself.
  - {id: f_owner,     type: principal, writable_by: ['role:maintainer']}
  # Where the questions are, and where the agreed tests are: a path or a
  # URL, and for the tests the commit that holds them.
  - {id: f_questions, type: string}
  - {id: f_tests_ref, type: string}
  - {id: f_pr_url,    type: string}
  # The journal entry written at the end (a path and heading, or a URL).
  - {id: f_journal,   type: string}
  - {id: f_rank,      type: string}

invariants:
  - {id: owner_missing, rule: 'f_owner != null'}

links:
  - {name: epic,    inverse: steps,      targets: [epic], max: one}
  - {name: blocks,  inverse: blocked_by, targets: [build_step]}
  # A decision this step made or depends on (a `decision` Type, later).

states:
  - {id: questions,       category: todo}
  - {id: answering,       category: waiting}
  - {id: writing_tests,   category: active}
  # The tests are proposed: the reference may not change until the owner
  # agrees or sends them back.
  - {id: tests_proposed,  category: waiting, frozen: [f_tests_ref]}
  # Agreed tests are protected: the reference is frozen while building and
  # in review. Changing a test means going back to tests_proposed.
  - {id: building,        category: active,  frozen: [f_tests_ref, f_owner]}
  - {id: in_review,       category: waiting, frozen: [f_tests_ref, f_owner]}
  - {id: done,            category: done,    frozen: [f_tests_ref, f_owner, f_pr_url]}
  - {id: dropped,         category: canceled}

transitions:
  # 1. Questions first.
  - {from: questions,      to: answering,      required: [f_questions]}
  - {from: answering,      to: writing_tests,  allowed: ['field:f_owner']}
  # 2. Tests first, and only the owner agrees.
  - {from: writing_tests,  to: tests_proposed, required: [f_tests_ref]}
  - {from: tests_proposed, to: building,       allowed: ['field:f_owner']}
  - {from: tests_proposed, to: writing_tests,  allowed: ['field:f_owner']}
  # 3. A test must change: back for the owner's OK. Anyone may ask.
  - {from: building,       to: writing_tests}
  # 4. Review: the owner accepts; a journal entry is required.
  - {from: building,       to: in_review,      required: [f_pr_url]}
  - {from: in_review,      to: building,       allowed: ['field:f_owner']}
  - {from: in_review,      to: done,           allowed: ['field:f_owner'], required: [f_journal]}
  # Dropping a step is the owner's call, from anywhere before done.
  - {from: questions,      to: dropped, allowed: ['field:f_owner']}
  - {from: answering,      to: dropped, allowed: ['field:f_owner']}
  - {from: writing_tests,  to: dropped, allowed: ['field:f_owner']}
  - {from: tests_proposed, to: dropped, allowed: ['field:f_owner']}
  - {from: building,       to: dropped, allowed: ['field:f_owner']}
  - {from: in_review,      to: dropped, allowed: ['field:f_owner']}
  - {from: dropped,        to: questions, allowed: ['field:f_owner']}
```

Answers to the questions, and the session's notes, are `comment` Items
inside the step, as on an issue.

## What the kernel enforces, and what it does not

| Rule from `CLAUDE.md` | Enforced by | Limit |
| --- | --- | --- |
| Wait for answers before writing code | `answering -> writing_tests` is `field:f_owner` only | The kernel does not see the code: a session could still write code early. The record shows when the owner answered. |
| Wait for an OK on the tests | `tests_proposed -> building` is `field:f_owner` only | Same: it governs the record, not the repository. |
| Tests are protected | `f_tests_ref` frozen in `building`, `in_review`, `done`; changing it means going back through `tests_proposed` | The reference is frozen, not the files. A CI check comparing the test files with the agreed commit would close the gap (later). |
| End with a journal entry | `in_review -> done` requires `f_journal` | It checks that one is named, not what it says. |
| An agent never approves its own work | `field:` never matches an agent; `f_owner` is `role:maintainer`'s to write | Holds only if no agent is given `maintainer`. |

So the Type makes the process visible and its approvals unforgeable by an
agent. Keeping the repository in step with the record is CI's job.

## Open questions

1. **Can an agent set `f_owner` at creation?** `writable_by` is checked on
   the public door, which includes `CreateItem`; to confirm with a test that
   an agent creating a step with `f_owner` set is refused
   (`FIELD_NOT_WRITABLE`). A consequence, with the invariant
   `owner_missing`: an agent cannot create a step at all, only a maintainer
   can. That is probably right (the owner decides what is worked on), but it
   is a choice.
2. **Epic progress.** `epic@1` counts only `story`, `bug` and `task`
   (`incoming: issues`). Steps need an `epic@2` that lists `build_step`, or
   their own grouping.
3. **The dropped transitions.** Six lines for "the owner may drop it from
   anywhere". A `from: [list]` form, or a pattern (see
   `docs/plans/ideas-from-ventoux.md`, item 8), would say it once.
4. **Who is `maintainer`?** Only the repository's owner, at first.
5. **The `decision` Type.** Design decisions as Items, with `superseded_by`
   links and never renumbered: a separate proposal.

## First steps

1. A second stack, the `dev` namespace, the `ORI` space, using only the Types
   that exist: the ten items of `ideas-from-ventoux.md` as epics.
2. Then `build_step@1`, through the usual process: questions, tests, build.
   Its first Item tracks its own build.
