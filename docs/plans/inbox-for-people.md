# Plan: the Inbox for people

A brief for the orchestrator. Proposed 2026-11-27; the owner reviewed a
mockup and asked for this brief. Builds on track 1 item 5 (the web Inbox,
the binding claim, "sent back to the requester") and is not part of item 5's
definition of done.

## Why

The web Inbox built in item 5 is right underneath: it asks the kernel and
decides nothing about who may act. As a screen for people it is a
developer's tool: buttons read "Move to in_hand" with
`allowed: field:f_approver` beside them, every move is a full-width
button, people show as `user:lea`, and an agent's request looks like any
other row. The owner's verdict: not user friendly.

## What good inboxes do (the review behind this brief)

Read from Linear, LangChain's Agent Inbox, Microsoft Teams Approvals,
Front and ServiceNow (November 2026):

- **A few verbs, fast** (Linear's triage: accept, decline, snooze, by
  keyboard; batch processing).
- **An agent works, a person stays responsible** (Linear's delegation:
  both shown, the agent's state working, waiting, failed or done).
- **Four answers to an agent** (LangChain: accept, edit, respond, ignore).
- **Decide where people are, buttons only for those who may** (Teams
  role-based cards that update after the decision, so nobody decides twice).
- **Ownership visible in a shared queue** (Front: assigned to me, who is on
  it, no thread worked twice).
- **Work as cards, with presence** (ServiceNow).

## The mockup

A design canvas, "Ori Inbox redesign" (owner's claude.ai artifacts), three
screens with sample data:

1. **Waiting on me:** rows grouped Overdue and Today; one line per row
   (title; who asked and why it is yours; how long it waited, in amber past
   its due); one primary button from the moves, the rest under a menu; a
   side panel for the selected row instead of a new page. Open in the
   panel: an agent's refund request, its note marked as the agent's, the
   proposed amount editable, and three answers.
2. **A team queue:** "Nobody has these yet" (oldest first, "Take it"),
   then "Being handled" with who has each ("Jon Okafor has it"; "Desk agent
   working, for Jon Okafor"); several rows selected, "Take both"; the
   caller's own request says why they cannot take it.
3. **The same request in a chat app:** buttons only for those who may
   decide; the card turns into the decision once made.

## What this brief builds

Three PRs, in order. Each ends with the web tests, a journey on the stack,
and the owner's look at a screenshot.

### PR 1: words for people (the Type, then the screen)

The one kernel change, and the start of theme 1's "presentation hints on
the Type" (roadmap): only the hints the Inbox needs now.

- **On a Type, optional:** `label` and `help` on a state, a transition and
  a field; `primary: true` on at most one transition out of a state (the
  row's main button); `summary: [field ids]` on the Type (what a row says
  under its title). Hints only: they enforce nothing and decide nothing.
- **Checked at load:** a label is short (60 characters), plain text; at
  most one primary per source state; `summary` names fields the Type has.
  A Type without hints loads as today, and every screen falls back to the
  IDs it shows now.
- **Out through `GetType`**, additive fields in `kernel.v1` (asks first:
  an API contract change, CLAUDE.md rule 6).
- **The reading** uses labels where they exist ("Approve" for the move
  `pending → approved`), IDs in code font otherwise, as `docs/design.md`
  already foresees.
- **Types in `types/`:** labels for the Types the stack's journeys use
  (`approval@1`, `access_request@1`, `equipment_request@1`, `incident@6`
  once it exists), as new versions or in place as the version rules allow.
- **Not here:** field order and groups, input hints, views (theme 1's
  later parts).

### PR 2: the Inbox screen, rebuilt over the same calls

Web only; the same four calls as today (`WhoAmI`, `CountItems`,
`SearchItems` with `waiting_on`, `TransitionItem`); no new dependency.

- **Rows:** one line; the summary fields; who asked; why it is yours (as
  today); waited, in amber past a due time when the Type gives one; the
  primary move as a button, the others in a menu; labels, never IDs.
- **Groups:** Overdue, then the rest, oldest first (the order switch stays).
- **A side panel** for the selected row: its fields, its moves, required
  fields asked for in place before the move, its history folded.
- **Team tabs:** unclaimed first; claimed rows shown with who holds the
  claim, not hidden; a caller's own request says why it cannot be taken.
- **Several rows at once:** a move offered on every selected row, made
  as one `TransitionItem` per row, each reported.
- **Keyboard:** J and K to move, Enter to open, the primary move on a key
  the screen shows.
- **Names:** a person's display name where the screen knows one (today the
  dev-token file, as the PoC apps do: `docs/design.md`, "PoC apps as built",
  gap 1), the ID otherwise.
- **Accessible as built:** real buttons and links, labelled controls,
  contrast 4.5:1.

### PR 3: an agent's request, shown as one

- A row and a panel mark what an agent did: "Agent request" when the Item
  was created or moved by an agent principal, and "for {person}" when it
  acted on someone's behalf (`on_behalf_of`, already recorded).
- An agent's free text is shown as the agent's, rendered as plain text
  (`docs/roadmap.md`, "Ask a person", borrowed item 7: untrusted in the
  browser too).
- No new answers yet: the moves the Type offers. "Not yet", a refusal
  reason and the approver's own values arrive with `question@1` and
  `approval@2` ("Ask a person"), and the panel takes them then.

## Out of scope

- A chat-app card (screen 3): with "Ask a person"'s events out, later.
- Snooze for oneself, presence, a "Requested by me" tab beyond what item 5
  step 5 gives the requester.
- Real display names from sign-on (hosting, after the decision point).

## For the owner to decide

1. Presentation hints now, as this subset, before theme 1: a change to the
   Type format and to `docs/design.md`.
2. Additive fields in `GetType` (an API contract change).
3. Where it sits: after item 6 and item 5 step 5's PR 2, before track 2's
   authoring test, whose author reads the same labels in the reading.
