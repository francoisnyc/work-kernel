# Proposal: Claude building this repository on its own, through the tracker

Status: proposal, 2026-11-22. Nothing here is built. `docs/design.md` and
`CLAUDE.md` win. Builds on `docs/plans/build-step-type.md`.

## Where this comes from

Items 2a and the hook protocol were built by coding sessions, with a person
relaying between them and a separate session reviewing each report and branch.
That worked, and the review caught real things:

- a session building a field (`actor`) that had been decided against;
- a refusal test that passed on the stub, so proved nothing yet;
- unreachable code kept "as a safety net";
- a design doc linking to a plan that was not on `main` yet;
- a merge order that would have left `main` pointing at a missing file;
- that no test written before 2a had changed (checked, not assumed).

This note turns those roles into agents, with the tracker (the ORI space on
the long-lived tracker stack) holding the state and the gates, and the person
approving only where a decision is theirs.

## Roles

| Role | Who | Does | Never |
| --- | --- | --- | --- |
| **Owner** | a person (`user:francois`, role `maintainer`) | answers what only they can decide; agrees each step's tests; accepts the pull request and merges | relays reports between agents |
| **Orchestrator** | an agent, acting for the owner | picks the next story; writes the step's brief; starts a builder; runs the reviewer; answers questions the docs already settle; escalates the rest; records everything on the step | approves a gate; merges |
| **Builder** | an agent, one per step | follows `CLAUDE.md`: questions, failing tests, code, pull request, journal | approves its own questions, tests or pull request |
| **Reviewer** | an agent, a different session from the builder | checks each phase against the brief and the checklist below; reports to the step | writes code on the builder's branch |

Each agent works as its own `agent:` principal, in a session started for the
owner, with only the powers its role needs. The kernel already guarantees the
part that matters most: a `field:` entry never matches an agent, so none of
them can pass a gate that `build_step@1` gives to `field:f_owner`.

## The loop, on one `build_step`

1. **Pick.** The orchestrator takes the top open story of the active sprint,
   creates a `build_step` linked to its epic (or the owner does, while only a
   maintainer may create one: `build-step-type.md`, open question 1), and
   writes its brief as a file under `docs/plans/`, on a branch, as
   `item-2-brief.md` was.
2. **Questions.** The builder reads the brief and the docs, and posts its
   questions on the step (`questions -> answering`). The orchestrator answers
   every question the docs already settle, citing where, and escalates the
   rest (rules below). The owner moves the step on (`answering ->
   writing_tests`).
3. **Tests.** The builder writes the failing tests, pushes them, and sets
   `f_tests_ref` (`writing_tests -> tests_proposed`). The reviewer checks them.
   The owner agrees (`tests_proposed -> building`) or sends them back.
4. **Build.** The builder builds, in small commits, and opens the pull request
   (`building -> in_review`, `f_pr_url`). The reviewer checks the branch and
   CI.
5. **Accept.** The owner reads the reviewer's report, merges, and accepts
   (`in_review -> done`, `f_journal`).

Every report, question, answer and review is a comment on the step, so the
step's history is the whole story of how it was built.

## When the orchestrator wakes the owner

**It answers on its own** only when the answer is already written down:
`docs/design.md`, `CLAUDE.md`, the step's brief, a decision recorded on the
step or its epic. It cites the source in the answer.

**It always escalates:**

- a change to the API contract, the schema, or a Type in `types/`;
- a new dependency;
- any change to `docs/design.md` (the builder proposes wording; the owner
  approves it);
- a test written before the step that would have to change;
- a builder's default that differs from a recorded decision, or work outside
  the brief (the `actor` case);
- a conflict between two documents, or between a document and the code;
- the order of merges, and every merge;
- anything that deletes data, on any stack;
- anything it is unsure about. When in doubt, ask.

An escalation is one comment on the step, addressed to the owner: the
question, the options, a recommended default, and why. The owner answers on
the step.

## The reviewer's checklist

From what was checked by hand on 2a and the hook protocol:

1. **Tests are protected.** No test file that existed before the step is
   modified or deleted. Check with git, not by asking:
   `git diff --name-status <base> HEAD -- '*_test.go'` shows only `A` lines
   (and the setup-only changes the owner agreed, in their own commit, with no
   assertion touched).
2. **Phase B fails for the right reason.** Each new test fails at a stub or
   a missing flag, never at setup, a panic or a typo. A test that passes on
   the stub is reported: it proves nothing until the code exists.
3. **Scope.** Everything built is in the brief or in a recorded decision.
   Anything else is reported, even if it is good work.
4. **Promises.** Every claim in the step's design notes and `design.md`
   wording (an error, a refusal, a bound, a default) has a test that would
   fail without it.
5. **No dead code.** Code that can no longer run is removed, not kept "just
   in case".
6. **CI is green**, including the Postgres-backed packages and the scenario,
   on the commit that will be merged.
7. **Merge readiness.** No conflict with `main`; every file the change links
   to exists on `main`, or merges first.
8. **Reports match the branch.** What the builder says it did is what the
   commits contain.

## Where it runs

- **On the owner's Mac**, as Claude Code: the tracker stack listens on
  `127.0.0.1` only (dev tokens are its only security), so a session in the
  cloud cannot reach it. Remote agents wait for real sign-on.
- **Builders** may run anywhere they can push branches and open pull
  requests; the orchestrator relays their state to the tracker.
- **Never on the tracker stack's data destructively.** `docker compose
  down -v`, `make reset` and `make scenario` are never run in
  `~/code/ori-tracker`. Builders test on their own stacks or in CI.

## What is missing

1. **`build_step@1`**, built and loaded on the tracker stack. Without it the
   gates are conventions again.
2. **Notifications.** There is no Watch: the orchestrator polls the tracker
   (every few minutes) for steps waiting on it. The owner needs nothing new:
   the admin UI's Inbox already lists, for every transition whose `allowed`
   names a principal field, the Items in its from-state where that field is
   you, so every step waiting on `field:f_owner` shows up there.
3. **A way to start and check builder sessions** from the orchestrator.
4. **Budgets.** A limit per step (time, rounds of questions, CI runs) after
   which the orchestrator stops and escalates instead of retrying.

## First steps

1. Build `build_step@1` through the usual process, with a normal session.
2. Run **one** step semi-autonomously: V2b. The orchestrator writes the brief,
   starts the builder, runs the reviewer, records everything on the step; the
   owner approves only in the tracker.
3. After V2b, write down every correction the owner had to make. They become
   new lines in the checklist and the escalation rules above, and this note is
   updated before the next step.
