# Step 3 plan — relay, lazy workflow, one durable timer

Status: draft for review. No Step 3 code or tests exist yet.

## Approach

- **Type.** A state may carry `timeout: {after: 48h, to: open}`. One function,
  `typedef.Type.HasBehavior(stateID)`, answers "does this state need a workflow?".
  Today it means "has a timeout"; holds, approvals and link reactions extend it later.
- **Relay** (`internal/relay`, run by `cmd/relay`). Single instance, polls every
  200 ms. For each Item with undelivered rows it takes the lowest `item_seq`, reads
  the event's shape, and sends `{seq, op, from, to}` — never field values.
  - The event enters a state with behavior → Update-With-Start (workflow ID = item
    ID, `USE_EXISTING`, update ID `item:seq`). The start input carries the Type id
    and version and `lastAppliedSeq = seq - 1`; an existing run ignores the input.
  - Otherwise → plain Update.
  - Accepted → mark delivered, go on to the Item's next row.
  - Plain Update answered "not found" or "already completed" → nothing listens:
    mark delivered, log at debug level.
  - Anything else (gap rejection, timeout, server down) → leave the row, stop this
    Item for this pass, carry on with the other Items.
- **Workflow** (`internal/workflow`, run by a new `cmd/worker`). Holds
  `lastAppliedSeq`, the current state and the Type (loaded once by an activity;
  a Type version is immutable and holds no personal data).
  - Update validator: `seq <= last` → accept and ignore; `seq > last + 1` → reject;
    `seq == last + 1` → apply: record the state, cancel any timer, arm the new
    state's timeout if it has one.
  - Timer fires → activity calls `kernel.Transition` as actor `system:timer`, key
    `timer:<item>:<seq that armed it>`, `causation_id` = that event, with an
    internal "only if `item_seq` is still N" precondition. A kernel rejection is a
    non-retryable activity error. The resulting event returns through the relay
    like any other.
  - In a state without behavior, with no update in flight → the workflow completes.
  - Continue-as-new when the SDK suggests it, carrying `lastAppliedSeq`, the state
    and the timer's deadline.

## Edge cases and their tests

| # | Case | Test |
| --- | --- | --- |
| 1 | Same seq delivered twice | `testsuite`: accepted, state unchanged, timer not re-armed |
| 2 | Gap (`last + 2`) | `testsuite`: rejected; then `last + 1`, `last + 2` both apply |
| 3 | Timeout elapses | `testsuite` (skipped time): activity called once, with the expected key and seq |
| 4 | Item leaves the state first | `testsuite`: timer canceled, activity never called, workflow completes |
| 5 | Stale timer (Item moved on, relay lagging) | kernel test of the seq precondition; `testsuite`: rejection does not fail the workflow |
| 6 | Continue-as-new | `testsuite`: `lastAppliedSeq` and deadline carried over |
| 7 | Per-Item order; one stuck Item | relay + Postgres + fake Temporal client: 1, 2, 3 in order; a failure on Item A's 2 leaves 2 and 3 pending while Item B drains |
| 8 | Type decides start vs plain Update | unit test of `HasBehavior`; relay test with the fake |
| 9 | New run after a finished one | real server: workflow finishes, a later event re-enters a timer state, a new run starts and applies it |
| 10 | Nothing listening | relay with the fake, and real server: "not found" and "completed" both mark delivered |
| 11 | **Done when**: worker stopped mid-timer | real server (dev-server container), 2 s timeout: stop the worker, restart it, the Item transitions |
| 12 | Relay dies after accept, before marking | redelivery is case 1 |

## Assumed where you did not choose

- `ticket.v1.yaml` gains the timeout in place (v2 stays free for Step 5). Tests use
  their own Type with a 2 s timeout.
- Case 11 stops and restarts the worker in-process, not a process kill.
- New dependency: `go.temporal.io/sdk`. The Temporal container uses the existing
  testcontainers module.

## Unsure

- Which errors the pinned server returns for an Update to a missing workflow and to
  a completed one, and that Update-With-Start starts a new run after completion.
  I will probe both against the dev server before writing the relay tests.
- A workflow that completes while an update is in flight. I expect the relay's
  retry to land in case 9 or 10; needs a real-server check.
- The timer runs from when the workflow applies the event, not from the event's
  `created_at`, so relay lag lengthens the timeout. Acceptable for the spike?
- The design says the workflow "calls the same pure transition function". With no
  field values in Temporal it cannot evaluate guards, so the plan lets the kernel
  decide when the timer fires. Is that deviation acceptable?
