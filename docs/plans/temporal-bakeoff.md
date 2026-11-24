# Bake-off 2: the kernel on Temporal, or each process written on Temporal

Status: brief, 2026-11-23. A throwaway spike, run autonomously, ending in one
report. Run it **after the first hole the Camunda bake-off found is fixed**
(`docs/roadmap.md`, theme 2: an effect re-checks committed state), so both
sides face the same attacks with the kernel's worst hole closed.

## The question

Where Temporal already runs, the realistic alternative to the kernel is not
another engine: it is writing each process directly on Temporal, in code.
The kernel runs on Temporal too, so this is not a rematch of engines. The
question is **cost and consistency at scale**: what does each new process,
and each change to a running one, cost on each side, and how much of what
keeps a process safe has to be written again every time?

Hand-written code can be made as safe as anything, if every check is
written correctly in every process. So the attacks are not asked "can it
pass?", but "what did passing take, and did the second process get it right
without being told?".

## The scenario

The same as the first bake-off (`docs/plans/camunda-bakeoff.md`): the purchase
approval (manager; finance from a shared queue; an agent that prepares and
never decides; a stub purchase system), and the access request. Reuse the
first spike's stubs, seed data, agent stand-in and attack scripts
(`spikes/camunda-bakeoff/`, on `main` once its pull request is merged,
otherwise on branch `spike/camunda-bakeoff`).

## The plain-Temporal side

Written the way a competent team at a company would write it, following
Temporal's own documentation and samples: Go, the Temporal Go SDK, Postgres
for anything Temporal should not hold, a small HTTP API for people and the
agent, and the team's own sign-in stand-in (the same dev tokens as the
kernel side). No kernel code is used or copied.

**Built in two steps, measured separately:**
1. The purchase approval, complete, until it passes the first spike's test 2
   (the compromised agent) as well as the builder can make it.
2. The access request, as a second process, written as a team would add one:
   reusing what step 1 made reusable, and nothing more. Its attacks are run
   **before** any hardening specific to it, then after, and both results are
   recorded.

## The four measures

1. **The marginal cost of a process.** Lines, files and hours for the second
   process on each side (the kernel: a Type; plain Temporal: code). Count
   separately what plain Temporal shares in a library after step 1.
2. **The cost of a change.** "Purchases over 5,000 also need the CFO, after
   finance", with requests already in flight at each step. On plain
   Temporal, use Temporal's documented way of changing running workflows
   (versioning or patching) and record what in-flight requests do. On the
   kernel, record what Type versions do to Items already open.
3. **What every process rebuilds.** For plain Temporal, list and count what
   each process needs that the kernel gives every Type: the inbox and claim,
   who may see and do what, the audit trail, frozen fields, the agent's
   limits, searching, history. Say which of these a shared library would
   absorb, and what that library would then be.
4. **Consistency under attack.** The first spike's compromised-agent script,
   extended to the access request and to plain Temporal's own side doors
   (its API, Temporal itself, the database). Report per process, and the
   result for the second process before and after its own hardening.

Also record, as in the first bake-off: footprint, start time, and what each
side would cost a team to maintain.

## Rules for a fair result

- **Steelman plain Temporal.** Use Temporal's recommended patterns (signals
  and updates for decisions, queries for state, versioning for change, a
  shared activity library), and read its documentation before deciding
  something is hard. If a pattern makes the plain side much better, use it.
- **No kernel code changes.** Gaps on the kernel side are recorded and
  counted against it, as before.
- **Same effort,** time-boxed: at most half a working day in all.
- **The live model run stays optional,** as in the first brief; the
  compromised-agent script needs no model.
- **A reviewer who didn't build it** reads the report and the code before it
  is final, argues plain Temporal's case, and lists anything unfair. Its notes
  and what changed because of them go in the report.

## Where it runs

A cloud coding session on this repository, as for the first bake-off:
`dockerd` started by hand if needed, images through `mirror.gcr.io` when
Docker Hub answers 429. Branch `spike/temporal-bakeoff`; code under
`spikes/temporal-bakeoff/`, reusing the first spike's stubs and scripts by
path rather than copying them where possible.

## Autonomy and when to stop

Work without waiting for answers; record each assumption in the report as you
make it. Stop and report early, with what you have, if a step needs a
licence, an account, a key or a host the session can't reach; if the images
can't be pulled after one retry through a mirror; if the kernel side would
need a code change to continue; or at the time box. Never push to `main`,
merge, open anything but the one pull request, or change `docs/design.md` or
the roadmap.

## The deliverable

One pull request from `spike/temporal-bakeoff` with:

- `docs/research/temporal-bakeoff.md`: the four measures, side by side, with
  evidence; what plain Temporal had to rebuild, as a list a team could use to
  estimate their own processes; the reviewer's notes; and a recommendation in
  one paragraph, with what would change it.
- `spikes/temporal-bakeoff/`, runnable from its README.
- A short journal entry.

Plain words, for the owner's colleagues as well as the owner.
