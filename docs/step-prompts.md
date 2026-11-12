# Session prompts for each build step

A **step** is one row of the build order in `docs/design.md` (Step 1 to Step 5).
It is finished when its "done when" passes, however long that takes: a couple of
hours or several sittings.

Each step runs through four phases, A to D. Start a **fresh session** for each
step, and also mid-step if a session gets long (context around 40% full): have the
agent write its journal entry first, then continue in a new session.
Replace `<N>` with the step number.

---

## Phase A — questions

```
Read CLAUDE.md, docs/design.md and the latest entry in docs/journal.md.
We are on Step <N> of the build order in docs/design.md.

Don't write code yet. Tell me:
1. In 3–5 bullets, what you understand this step's task to be and its "done when".
2. Your questions, and any gaps or contradictions you see in the design for this step.
3. Anything from previous steps that looks wrong or risky.
Keep it short. Wait for my answers.
```

## Phase B — tests first

```
Thanks. Now write the failing tests for this step's "done when", and nothing else.
Show me the test code and explain in one line what each test proves.
Don't implement yet. Wait for my OK.
```

## Phase C — implement

```
Tests approved. Implement until they pass, in small commits.
Explain Go idioms briefly where they differ from Java.
Don't change the tests. If one seems wrong, stop and tell me.
When done, show me the diff summary and run make test.
```

## Phase D — journal

```
Append an entry to docs/journal.md for this session:
- Step number, and whether the step is finished
- What was built
- What surprised you (design, Go, libraries)
- Numbers measured, if any
- Open questions and risks
- Where the next session should start
Keep it under 20 lines. Commit it.
```

---

## Step 1 extra (paste with Phase A on Step 1)

```
The repo is empty. As part of this step, also:
- Init the Go module, the repo layout from docs/design.md, a Makefile with the
  commands in CLAUDE.md, and docker-compose for Postgres + the Temporal dev server.
- Check the current versions of the Temporal Go SDK, connect-go, pgx and goose,
  and whether Update-With-Start is GA in the Temporal Go SDK. Write what you find
  to docs/research.md (one short section per library, with links).
List any setup questions together with your other questions.
```

## When a step is tricky (relay, lazy-start race, versioning)

Add a planning phase between A and B:

```
Before tests, write a one-page plan in docs/plans/step-<N>.md: the approach,
the edge cases, how each will be tested, and what you're unsure about.
Under one page. Wait for my review.
```
