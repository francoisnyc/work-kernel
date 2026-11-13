# Step 3 plan — relay, lazy workflow, one durable timer

Status: for review, after the Phase A answers. No Step 3 code or tests exist yet.

## Approach

- **Type.** A state may carry `timer: {after: 4h, to: <state>}`. Load-time check:
  `to` must be a declared transition from that state. `ticket.v1.yaml` is edited in
  place (`in_progress`: `{after: 48h, to: open}`), with a comment that this is a
  spike stand-in for standalone SLA objects.
- **Two pure functions in `internal/lifecycle`.** `TimerFor(type, state)` returns
  the state's timer, if any. `NeedsWorkflow(type, enteredState)` is the one rule
  for "does this event enter a state with behavior?" — today "has a timer".
- **Kernel additions.**
  - Optional `expected_item_seq` on `Transition` and `Update` (Go and proto);
    a mismatch is `ErrRejected` with code `stale`.
  - Write requests can carry `originating_hook`, `causation_id`, `correlation_id`
    and `hop` (Go only for now), stored in the existing columns.
- **Relay** (`internal/relay`, `cmd/relay`). Single instance, polls every 200 ms.
  Per Item it takes the lowest undelivered `item_seq` and sends the event's shape
  `{seq, op, from, to, at}` — never field values.
  - `NeedsWorkflow` true → Update-With-Start (workflow ID = item ID,
    `USE_EXISTING`, update ID `item:seq`); otherwise plain Update.
  - Update completed → mark delivered, take the Item's next row.
  - Plain Update answered "not found" or "already completed" → nothing listens:
    mark delivered, log at debug level.
  - Any other failure → leave the row; that Item backs off, the others carry on.
- **Workflow** (`internal/workflow`, `cmd/worker`).
  - Every new run first runs an activity that loads the Item's state, `item_seq`,
    Type, and the seq and time of the event that entered the current state. It
    sets `lastAppliedSeq = item_seq` and arms the timer from that loaded state.
  - Update handler waits for the load, then: `seq <= last` → ignore;
    `seq > last + 1` → fail the update (the relay retries); `seq == last + 1` →
    apply. A state change cancels the timer and arms the new state's, if any.
  - Timer deadline = time the state was entered + `after`; already past → fire now.
  - Timer fires → activity calls `kernel.Transition` with `expected_item_seq =
    lastAppliedSeq`, actor `system:timer`, `originating_hook
    timer:<type>@v<version>:<state>`, `causation_id` = the `item:seq` that entered
    the state, `correlation_id` inherited from that event, key `timer:<item>:<seq>`.
    A kernel rejection is a non-retryable activity error, logged.
  - In a state with no behavior the run waits for `AllHandlersFinished`, then
    completes. Continue-as-new carries `lastAppliedSeq`, state and deadline.

## Edge cases and their tests

| # | Case | Test |
| --- | --- | --- |
| 1 | Same seq delivered twice | `testsuite`: ignored, timer not re-armed |
| 2 | Gap (`last + 2`) | `testsuite`: fails; then `last + 1`, `last + 2` both apply |
| 3 | Timer elapses | `testsuite` (skipped time): activity called once, with the expected key, seq and attribution |
| 4 | Item leaves the state first | `testsuite`: timer canceled, activity never called, run completes |
| 5 | Stale timer | kernel tests of `expected_item_seq` on `Transition` and `Update`; `testsuite`: a rejection does not fail the run |
| 6 | Field update not yet delivered when the timer fires | `testsuite`: `stale` keeps the timer due; it fires again once the update is applied |
| 7 | Run starts while the relay lags (`item_seq > N`) | `testsuite` with a mocked load: right timer armed, updates at or below the watermark ignored |
| 8 | Continue-as-new | `testsuite`: `lastAppliedSeq` and deadline carried over |
| 9 | Per-Item order; one stuck Item | relay + Postgres + fake Temporal client: 1, 2, 3 in order; Item A stuck on 2 while Item B drains |
| 10 | Type decides start vs plain Update | unit tests of `TimerFor`, `NeedsWorkflow`, the load-time timer check; relay test with the fake |
| 11 | Nothing listening | fake and real server: "not found" and "completed" both mark delivered |
| 12 | New run after a finished one | real server: the run finishes, a later event re-enters a timer state, a new run applies it |
| 13 | Update arrives while the run is closing | real server: applied, or lands on a new run; never lost |
| 14 | **Done when**: worker stopped mid-timer | real server, 2 s timer: (a) restart before it is due; (b) restart after it was due — both transition the Item |
| 15 | Pinned-server behavior | real server: Update-With-Start works; errors for Update to a missing and to a completed workflow; Update-With-Start after completion starts a new run |

Real-server tests use a Temporal dev-server container and an in-process worker.

## Commit order (each tests-first)

1. `expected_item_seq` and the attribution fields on write requests.
2. Timer syntax, `TimerFor`, `NeedsWorkflow`, ticket fixture.
3. Add `go.temporal.io/sdk`; pinned-server tests (case 15).
4. Workflow (cases 1–8). 5. Relay (9–11). 6. Activities, `cmd/worker`,
   `cmd/relay`, real-server tests (11–14).

## Unsure

- **Gap as a failed update, not a validator rejection.** The handler must wait for
  the load, and validators cannot wait. So the relay waits for the update to
  complete, not just to be accepted. OK?
- **Timer counted from the event's `created_at`**, not from when the workflow sees
  it, so relay lag and restarts do not lengthen it. This is why the shape carries
  `at`. OK?
- **Where code `stale` lives.** The check is kernel logic and covers `Update`, but
  the only coded rejection today is `lifecycle.Rejection`. I propose reusing that
  type with a new code, so callers read one error type.
- **`hop` for a timer write**: the arming event's hop + 1?
- **Case 13** depends on how the server treats an update racing a completion;
  case 15 will tell me before I write it.
