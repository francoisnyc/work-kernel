# Plan: the Inbox, the look

A brief for the orchestrator. Proposed 2026-11-28, after the owner saw the
Inbox for people as built (PRs #109 to #117) beside its mockup. Follows
`docs/plans/inbox-for-people.md`, whose behaviour is all built: this brief
changes how the screen looks and reads, not what it does.

## Why

The behaviour matches the brief; the look does not match the mockup (the
design canvas "Ori Inbox redesign", owner's claude.ai artifacts). The
first brief specified behaviour precisely and only pointed at the mockup
for the look, so the builders did what was asked. Seen on the screen
(screenshots of the real page with sample data, 2026-11-28):

- the admin page's styling: grey default buttons of equal weight, a row as
  four stacked lines, the panel's moves as full-width blocks one under the
  other;
- Type IDs shown to people: "approval", "equipment_request";
- a sentence built from a field label that does not read: "you are its
  Handled by";
- a raw instant: "Decide by: 2026-11-28T13:43:48.660Z";
- an agent shown by its ID ("support-bot"), and its text shown twice in
  the panel (under "What the agent wrote", then again in the field list);
- "Open it to answer" on a row that has nothing for the reader to do.

## What this brief builds

One PR, web only, plus one presentation hint on the Type (below). No new
call, no change to what any call returns beyond that hint.

1. **The mockup's styling** (`web/src/styles.css`, the Inbox and its
   panel; other pages untouched):
   - a row is a card: title on the first line; who asks, why it is yours,
     how long it waited (amber when overdue) on the second; the summary
     fields on a third line only when the Type has them;
   - the main move is the one dark button; the others sit behind a quiet
     "⋯" menu button (an accessible menu, labelled "More actions");
   - the panel's moves in one row, main move first and dark;
   - tabs as tabs (an underline for the selected one, the count as a
     pill); the order and place controls in one quiet line;
   - an agent's request marked with the mockup's purple badge and a small
     robot icon (inline SVG), never colour alone;
   - contrast 4.5:1, touch targets 44 px, keyboard focus visible.
2. **Words that read:**
   - why it is yours: "{Field label}: you" ("Approver: you", "Handled by:
     you") in place of "you are its {label}"; "for your team ({role})"
     stays;
   - a row with nothing for the reader says why and offers no link
     ("Jon has it"; "You can't handle your own request"); the title is
     already the link to open it;
   - a Type by its label: "Approval", "Equipment request" (item 4 below),
     its ID when it has none.
3. **Dates and times in words,** in the reader's locale: "today 1:43 pm",
   "tomorrow 9:00 am", "28 Nov", and "4 h ago" or "in 19 h" beside a due
   time; the full instant in a tooltip.
4. **A label for the Type,** the one kernel change: `label` (and `help`) at
   the top of a Type, checked as the other labels are (plain text, 60
   characters), out through `GetType` and `ListTypes` as an additive field,
   used by the reading's first sentence. The Types in `types/` that the
   Inbox's journeys use gain one (as new versions or in place, as the
   version rules allow).
5. **An agent's text once:** "What the agent wrote" shows the fields the
   agent set last; the panel's field list leaves those fields out while
   the box is shown.
6. **Agents by name where the screen has one:** the dev-token file's agents
   get a `name` the screen can show ("Support agent"), as people's names
   come from it today (`web/src/model/people.ts`); the ID otherwise.

The web tests that pin today's words change with them: say which, and
why, at Phase A (CLAUDE.md, tests are protected).

## Done when

- The screen, on the stack, side by side with the mockup's three screens,
  reads the same at a glance; screenshots in the PR.
- The journeys still pass; the scenario script is unchanged.
- An accessibility pass: every control reachable by keyboard, labelled,
  with visible focus; no colour-only meaning.

## Out of scope

- The chat-app card (the mockup's third screen): with "Ask a person".
- "Not yet", a refusal reason and the approver's own values: with
  `approval@3`.
- Any page other than the Inbox.

## For the owner to decide

1. A `label` on the Type itself (a Type format change and a line in
   `docs/design.md`; an additive field in `GetType`).
2. Agents' names from the dev-token file, as people's are, until sign-on
   brings real names.
