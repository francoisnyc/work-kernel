# Re-planning the tracker for the three tracks

Status: approved by the owner on 2026-11-24, **not yet applied**. The
tracker stack listens only on the owner's Mac, so a session there applies
this. When it is applied, say so here (one line, with the date) or delete
this file.

Source: `docs/roadmap.md`, "Now: prove, then broaden". The roadmap is the
truth for what the tracks are; the tracker (the ORI space) mirrors it.

## Before writing anything

Pull main. List what the ORI space already holds and reuse it: never create
a duplicate epic or story. Change nothing this note does not name. At the
end, report what was created, what was reused and what was paused.

## Epic "Track 1: close the gaps", ranked first

Done when both bake-offs' scripts (`spikes/camunda-bakeoff/`,
`spikes/temporal-bakeoff/`) pass in full on the kernel, re-run unchanged.
One story per item, in this rank order, each with the bake-off attack that
found it as its test:

1. An effect re-checks what caused it (the design rule is merged, PR #65).
2. Fields closed to agents by default: a Type opens a field to agents by
   saying so.
3. Required means given by the mover, where a Type asks for it.
4. Only the requester edits a draft, until it is submitted.
5. Team queues with a claim that binds, and a "waiting on me" view over
   every Type.
6. Moving open Items to a new version, and retiring old versions. It
   includes starting work, step 2b (the latest version governs placement),
   and the roadmap's open hole: a create may still name an older version.

## Epic "Track 2: test what is untested", ranked second

1. A non-developer authors a real process: a colleague describes a process
   from the BPM suite being retired, an assistant writes the Type, and they
   correct it from the reading and the story.
2. A third process of a different shape: an open-ended case by a fresh
   author, on the kernel and on plain Temporal with the shared package.

## Track 3: one story, no epic

"Reminder: the shared approvals package on plain Temporal lives outside
this repository." Nothing is built here. The loop never picks it.

## Paused until the decision point

From the roadmap's "Waiting until then":

- a semantic diff (Ventoux item 7);
- patterns (Ventoux item 8);
- checks that prove properties (Ventoux item 3);
- the security operations domain (theme 4);
- cards in chat, and context packs (both inside theme 3);
- hosting, if the tracker has it.

Provenance (theme 2) is not paused, but goes only as far as track 1 needs.

The `epic` Type has no paused state and no labels, so for each paused epic:

- move it to `todo`;
- start its summary with "[paused until the decision point]";
- label each of its stories `paused` (stories have `f_labels`);
- rank those stories below every track 1 and track 2 story.

The loop does not pick a story labelled `paused`.

## Also

`build_step@1` is on main (PR #68). Load it when the owner moves the
tracker stack forward.
