# Jira 2.4b Phase A, with the answers

Written by the 2.4b coding session (Phase A), then answered. Part 1 is the proposal as written; Part 2 is the answers, which win where they differ.

## Part 1: Phase A as proposed

I've done the Phase A reading: CLAUDE.md, the last two journal entries, the Later list, the design.md sections you named, the `sla` package, desk@2, incident@2 and the code where a few load rules live. No code is written yet.

**Housekeeping**
- **The first commit isn't needed.** The Later line about `actions/checkout@v4`, `setup-go@v5` and `setup-node@v4` on Node 20 is already on main (journal.md lines 22–24, added in 89f6f29). I made no commit. If you want the line to also say it was "held back from PR #35", say so and I'll make that one-line commit.
- **Branch.** This session can only push to `claude/jira-access-request-4b-r9ccun`, not `claude/jira-2-4b-access-request`. I'll work and open the PR there unless you tell me otherwise.

## The design in short

The request lives inside its resource. When submitted, it copies N and the approvers from the resource and creates one approval Item per approver. Each approval's decision moves the request through its container. The request's own guards count its approvals, which makes the moves exactly-once and safe if someone makes them by hand.

**Three facts from the code drive most of the kernel changes:**
- Principal fields are refused on purpose in `sets`, in create reactions and in `readable_by_reactions`, so copying approvers needs a declared copy rather than CEL.
- `except: [creator]` on a reaction-created approval matches nobody, because its creator is the reaction.
- Any principal who can see an Item can move it or narrow it. Without a rule against that, the requester could take approvers away (details in item 4).

## 1. Shape: approvals as child Items

**Default: one `approval` Item per approver, contained in the request.**

| | Child Items | Multi-value field of decisions on the request |
|---|---|---|
| History | Each approval has its own: who decided, who handed over to whom | Everything on the request; recording "who approved" needs a new keyword that writes the caller's identity into a set |
| Visibility | Same readers as the request; the Inbox works today (`field:f_approver`) | Same |
| Two approvers at once | Different rows, no contention; the request moves once (item 2) | Both lock the request row and are serialized; detecting the N-th still needs a new rule |
| Hand-over | One field on one Item | Swapping a set member, with a rule that only that member may do it: identity in the write rule |

- **Q1a.** Agree with child Items?

## 2. N of M: a reaction on each decision, checked by the request's guard

**Default: (b).**
- The approval's `approved` state has a move reaction aimed at its container (new: `target: container`).
- The request's `pending → approved` has the guard `children.approval.filter(c, c.state == "approved").size() >= f_required`.
- `pending → rejected` has the guard `children.approval.exists(c, c.state == "rejected")`.
- New: guards read children as `{state, category}`, locked FOR SHARE with the same read, lock, re-read and retry rule links use.

**Why not a rollup.** Rollups are counted at read time over what the reader can see. A count that decides a move must see every Item, under a lock.

**Why not (a), a stored counter plus a transition that fires when it reaches N.** It needs `add` from the children and a new kind of automatic transition with no actor. The counter is a cache that can drift from the approvals' real states.

**Exactly once:**
- **Two decisions at the same moment.** The reaction of whichever approval commits last runs after its own commit, so it sees every approval committed before it. The request's row lock lets only one move leave `pending`. The other reaction records `reaction_skipped not_in_from`.
- **Replays.** Reaction keys are `<reaction>:<source>:<cause>:<target>`, so a replay writes nothing new.
- **A decision after the request is decided.** Refused (item 3).
- **Moves by hand.** Because of the guards, someone moving the request by hand can only reach the outcome the reactions would. So no "nobody may do this" `allowed` entry is needed.

- **Q2a.** The first N−1 approvals' reactions are refused by the guard. Today that is recorded as `reaction_failed GUARD_FAILED`, which reads like an error in the history. Default: a per-reaction opt-in `on_guard: skip` that records `reaction_skipped guard_false` instead. The alternative is to keep the failure records and not count this as a kernel change.

## 3. Rejection and leftover approvals

**Default:**
- On entering `approved`, `rejected` or `withdrawn`, the request runs a children move `close_approvals` (pending → canceled, at most 8).
- An approval decided after that is refused:
  - Normally it is already `canceled`, so the refusal is `INVALID_TRANSITION`.
  - In the short window before the cancel reaction runs, the refusal is `GUARD_FAILED`. The decision has the guard `container.state == "pending"`, which needs another new reader: guards see the container as `{state, category}`, locked FOR SHARE.

- **Q3a.** Count "guards read the container and its children" as one kernel change?
- **Q3b.** Is `GUARD_FAILED` enough for the window? Named guard codes are on the Later list.

## 4. Where approvers come from, and why the requester can't change them

**Default:**
- `resource@1` holds `f_approvers` (principal, a set, `max: 8`, the reaction target cap) and `f_required` (int). Both are written only by `role:access_admin`.
- The invariant `quorum_invalid`: `f_required >= 1 && f_required <= size(f_approvers)`.
- The request is created inside the resource, so the container says which resource it is for. It starts as `draft`.
- `submit` (draft → pending) copies both onto the request in its own write: `sets: {f_required: 'container.f_required', f_approvers: 'container.f_approvers'}`.
  - New: `sets` and create reactions may read `container.<field>` when the container's Type lists the field in `readable_by_reactions`.
  - New: a principal field may be set only by a plain reference to another principal field, never computed. This relaxes "who never travels through a reaction" for copies only.
- Entering `pending` runs `request_approvals`, a create reaction with `for_each: f_approvers` (new): one approval per member, `f_approver: each`.
  - It reads the request's own copy, so N and M come from the same write.
  - Per-member keys and per-member records, as children moves already do.
- If the resource changes while a request is pending, the request keeps its copy.

**Every way the requester could add, remove or replace an approver, and what stops it** (one row per write path):

| Write path | What stops it |
|---|---|
| Fields on the request at create, update or submit | `f_approvers` and `f_required` are written only by the kernel (`FIELD_NOT_WRITABLE`) |
| Fields on the resource | Written only by `access_admin` |
| Creating an approval directly | `f_approver` is written only by the kernel, so it can't be set; the invariant `approver_missing` refuses the create |
| Updating an approval | `f_approver` is the kernel's; `f_delegate` is held by an invariant (item 5) |
| Links | None of the three Types declares a link the requester could write; the calendar and policy links are `sla_admin`'s |
| Moving an approval in or out, or the request to another resource | New rule below. Without it, an approver could move their own approved approval from someone else's request into their own: self-approval |
| Narrowing the request, an approval, the resource or the desk to hide a likely rejecter | Same new rule |
| Reactions the requester triggers | Submit copies role-written values; withdraw cancels; the request declares no `created_inside` reactions |

**The new rule:** a Type may say who may move or narrow its Items: `placement_writable_by: kernel` (nobody) or a list of roles.
- approval and access_request: `kernel`.
- resource: `[role:access_admin]`.
- **desk@3**: `[role:sla_admin]`. This is the reason for a new desk version: today anyone who sees the desk can narrow it and hide every request under it.

- **Q4a.** Agree with `draft → pending`? Starting directly in `pending` would need `sets` on create, which is on the Later list.
- **Q4b.** Agree with `placement_writable_by` (one kernel change) and desk@3?
- **Q4c.** If the requester is one of the approvers, their approval is still created and `except` stops them deciding it, so with N = M the request can't be approved until `access_admin` hands that approval to someone else. Agree, rather than skipping the requester's approval when creating them?

## 5. Handing over an approval

**Default:** a self-transition on the approval, `pending → pending`:
- `allowed: [field:f_approver, role:access_admin]`, `except: [container_creator]`
- `required: [f_delegate]`, `sets: {f_approver: 'f_delegate'}`, guard `container.state == "pending"`

**Refused:**
- **To themselves:** guard `f_delegate != f_approver`. It compares two stored values; nobody's identity is in CEL.
- **To the requester:** a new field attribute on `f_delegate`, `excludes: [container_creator]`, checked by `policy`.
- **To someone who already holds a pending or approved approval on this request:** new: `unique_among_siblings` on `f_approver` while pending or approved.
  - The container row is locked FOR UPDATE (locked, not written), so two hand-overs to the same person at once are serialized.
  - It also covers a duplicate created while approvals are being created after submit.
- **Someone else filling in `f_delegate` by update:** invariant `f_delegate == null || f_delegate == f_approver`.

**In the history:** the approval shows a `pending → pending` move by the approver or the admin, with `f_approver` and `f_delegate` changed. The old approver's decision after that is `NOT_ALLOWED`.

**One more kernel change:** today `sets` may not write a field that its own `allowed` names. `allowed` is judged on the stored value before the write, so a hand-over is safe. I'd relax the rule for a principal copied by plain reference only.

- **Q5a.** Agree with `container_creator` (one keyword in `policy`, used by `except` and `excludes`)? The other option is to create approvals on behalf of the requester so that `except: [creator]` works literally, but that changes what "creator" means for Items reactions create.
- **Q5b.** Agree with an in-place hand-over? The other option is a new approval for the delegate plus a create reaction into the container; it needs one more kernel change and has a window with no approval.

## 6. The SLA

**Default:** `sla_goal@2` and `sla_policy@2`.
- **Why new versions:** the package README already says a third metric would be `sla_goal@2`. Reusing `resolution` would mislabel the queue field, and the policy's targets are per priority, which a request doesn't have.
- **sla_goal@2:**
  - Adds metric `approval` and its four set reactions for a host field `f_due_approval`.
  - Adds the invariant `f_metric != null`, which refuses a bare goal created by hand (a Later item).
  - Gives its moves an `allowed` that no person passes. Today anyone who sees a goal can complete it by hand as "met"; the requester could do that to their own request.
- **sla_policy@2** adds `f_approval_s`, written by `sla_admin`.
- **When it runs:** `sla_start_approval` on entering `pending`; `sla_stop` on approved or rejected (`f_outcome` met or missed); `sla_cancel` on withdrawn.
- **Calendar and policy:** read from `container.linked.*`, which is the resource. So the resource links to them, written by `sla_admin`, exactly as the README requires of a host's container.
- **Breach mail "to the desk":** a role can't receive mail (a fallback role is on the Later list). The resource holds `f_contact` (principal, `access_admin`), copied on submit into `f_desk_contact`, and the request offers it as `breach_contact`.
- **Pausing:** none. A hand-over doesn't pause the clock, because the goal measures the requester's wait.

- **Q6a.** The calendar comes from the resource, not from the desk above it. Reading two levels up would be one more kernel change. Agree?
- **Q6b.** Agree with the hand-move fix and the bare-goal invariant in @2? @1 keeps both gaps and goes under Later.
- **Q6c.** In `allowed`, the entry no person passes is `principal:system:timer` today, which is a kludge. Add an explicit entry for that (one more kernel change), or keep the kludge?

## 7. Fulfilment

**Default:**
- Entering `approved` creates `fulfilment_task@1` inside the request. Its `f_resource` is copied from `container.f_name` and is written only by the kernel; the invariant `f_resource != null` refuses one made by hand.
- `f_assignee` is unset. States `open → in_progress → done`, `allowed: [role:desk_agent]`.
- Desk agents find it with a Type and state search.

## 8. Visibility

**Default: no narrowing anywhere.**
- Everyone in the namespace sees requests, approvals, goals and tasks, as staff see incidents today.
- **Why not narrow:** readers are principals, roles can't be readers, and `group:` is reserved. So "requester + approvers + desk agents" can't be written as readers, and narrowing to the requester would hide the request from its approvers (`field:` grants nothing).
- **How approvers find their requests:** the web Inbox already lists the moves whose `allowed` names `field:X`. For `approval`, that is approvals in `pending` where `f_approver` is me, with a link to the request. The approval copies the request's title so the Inbox row reads well.

- **Q8a.** Accept that the whole namespace sees who asked for what, and add a Later item: groups as readers, or an approver who joins the readers when named?

## 9. Types and scenario

**Types:**
- **New:** `resource@1`, `access_request@1`, `approval@1`, `fulfilment_task@1`.
- **New versions:** `desk@3` (who may move or narrow it), `sla_goal@2`, `sla_policy@2`.

**Scenario E, on a fresh desk@3:**
- **Setup:** lea, who also gets `access_admin`, sets up a resource: 3 approvers (bob, dana, erin), N = 2, `f_contact` = sam.
- **Steps:**
  - 2 of 3 approve and the request moves once. The third approval is canceled and refuses a late decision.
  - A rejection after one approval rejects the request and cancels the rest.
  - bob hands over to sam. Refused: to ana (the requester), to dana (already approving), to bob himself.
  - ana, the requester and also an approver on one resource, tries to approve her own request, add an approver, change N, move an approval and narrow the request. Each is refused.
  - The SLA is met on one request and missed on another (P1-like seconds), with the breach mail to sam.
  - The fulfilment task appears for sam and lea.

- **Q9a.** Agree with giving lea `access_admin` in the dev-token file? The web's dev-user test then changes, which I'd count as an agreed test change.
- **Q9b.** GetType will show the new rules (`for_each`, `target: container`, `excludes`, `unique_among_siblings`, `placement_writable_by`, `on_guard`), so proto v1 only grows. The web shows them as text only, which is still no Type-specific code. Agree?

## Kernel changes this design needs (13)

| # | Change | Who else would use it |
|---|---|---|
| 1 | `container.<field>` reads (with consent) in `sets` and create reactions | a task copying its project's key |
| 2 | Copying a principal field by plain reference | the reviewer onto a review Item |
| 3 | `for_each` on create reactions | one review per reviewer, one subtask per checklist entry |
| 4 | Guards read the container and its children (state and category) | a parent can't close while a child is open |
| 5 | A move reaction with `target: container` | the last subtask done closes its parent |
| 6 | `on_guard: skip` (optional, Q2a) | any move "when ready" |
| 7 | `container_creator` in `except` | the author may not approve their own review |
| 8 | `excludes:` on a principal field | the reviewer isn't the author |
| 9 | `unique_among_siblings` | one review per reviewer (and the leave-balance Later item) |
| 10 | `placement_writable_by` | goals and comments stay where they are |
| 11 | Handing over a field `allowed` names | handing over an assignment |
| 12 | An explicit "no person" `allowed` entry (Q6c) | moves only reactions make |
| 13 | Reading two levels up (Q6a) | default is not to build it, so not counted |

That's 12 if Q6c goes ahead and 13 isn't built; 11 if you also drop `on_guard` (Q2a), and 10 if you also keep the kludge for Q6c. It is more than steps 4a or 4b-fixes had.

- **Q0.** Is there any you'd rather accept as a documented weakness than build? Candidates: #6 (the failure records), #9 (rely on the admin to catch double approvals), #12 (keep the kludge).

I'm stopping here for your answers before Phase B.
## Part 2: the answers

Delivery: two PRs from one session, in order.
- PR 1, building blocks: the kernel changes below, each tested on test Types (as 4a did with
  kiosk/kticket), plus desk@3 and sla_goal@2/sla_policy@2. Phases B, C, D for PR 1, then stop
  for merge.
- PR 2, the access request: resource, access_request, approval, fulfilment_task, scenario E.
  Its own Phase B, C, D after PR 1 is merged.

Kernel changes: build 1, 2, 3, 4, 5, 6, 7, 9, 10, 11, 12. Not 8, not 13.
- #8 excludes: dropped. A hand-over to the requester is accepted and leaves an approval the
  requester can't decide (except: [container_creator]) until access_admin reassigns it: the
  same documented case as Q4c. Write both in design.md as one weakness.
- #6 on_guard: skip: yes, opt-in per reaction. The existing default (REACTION_FAILED) is
  unchanged.
- #12: yes, an explicit "no person" entry in allowed. Choose its spelling and say it in
  design.md; sla_goal@2 uses it. The system:timer kludge stays in @1.

Locks (must be in PR 1): #4 reads children and the container FOR SHARE from guards, and
decisions on approvals lock the approval. Without one order, a request's transition (holding
the request, reading approvals) and an approval's decision (holding the approval, reading the
request) can deadlock. Define one order and apply it everywhere: container before child. A
decision on a child locks its container FOR SHARE first, then the child FOR UPDATE. Write the
order in design.md next to the existing lock ordering, and add a concurrent test on test Types
(N decisions and a container move at the same time, repeated) that never deadlocks and moves
the container exactly once. #4 reads children with no caller visibility filter (a guard
judges the data, not the reader): confirm, and test that a narrowed child still counts.

Questions:
- Q1a child Items: yes.
- Q2a: on_guard: skip, yes.
- Q3a: yes, one change. Q3b: GUARD_FAILED is enough for the window.
- Q4a draft -> pending: yes. Q4b placement_writable_by and desk@3: yes. Q4c: yes, keep the
  requester's approval; documented with #8.
- Q5a container_creator: yes. Q5b in-place hand-over: yes.
- Q6a calendar from the resource: yes. Q6b: yes, both fixes in @2, the @1 gaps go to Later.
  Q6c: explicit entry (above).
- Q8a: accepted for the PoC. Later: groups as readers, or named approvers joining the readers.
- Q9a lea gains access_admin, the dev-user test change is agreed. Q9b: yes.
- Node 20 Later line: already on main, no commit needed.

Two existing holes go to the Later list, at the top, marked high: (1) any principal who sees
an Item may move or narrow it: apply placement_writable_by to incident, reply, note, sla_goal@1's
hosts and the PLAT Types in new versions; (2) a goal under sla_goal@1 can be completed by hand
as met by any staff member who sees it. Don't fix them in this step beyond desk@3 and
sla_goal@2.

Coordination: main now includes PR #37 (apps/poc), which also edited design.md and journal.md.
Don't touch apps/. Rebase on main before each Phase D.
