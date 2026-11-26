# The reviewer's attempts, both sides (plain Temporal after its fixes)

Generated on 2026-11-26 02:29 UTC. 

| Side | Process | Attempt | Result | Evidence |
|---|---|---|---|---|
| plain | purchase | block the decision: the agent rewrites its preparation 2,050 times, then the decider approves | PASS (refused) | 20 rewrites accepted; the decider's approval then answered HTTP 204 {} |
| plain | access | block the decision: the agent rewrites its preparation 2,050 times, then the decider approves | PASS (refused) | 20 rewrites accepted; the decider's approval then answered HTTP 204 {} |
| plain | purchase | an approved purchase whose order cannot be placed leaves a trace | PASS (recorded) | state=order_failed order=None failure=the purchase system refused the order: activity error (type: PlaceOrder, scheduledEventID: 28, startedEventID: 29, identity: 20786@vm@): POST http://127.0.0.1:18082/invoices: HTTP 404 history=['submitte |
| plain | purchase | a colleague with no part in it reads ana's purchase | PASS (refused) | HTTP 404 |
| plain | purchase | ... and its history | PASS (refused) | HTTP 404 |
| plain | purchase | the agent reads it (it must, to prepare) | ALLOWED (its job) | HTTP 200 |
| plain | purchase | the same request sent twice makes one purchase | PASS (one) | two purchases: False (with request ID on submission) |
| plain | purchase | a colleague in finance can take over a purchase fay claimed and left | PASS (can) | gus's release: HTTP 204 {} |
| kernel | purchase | block the decision: the agent rewrites its preparation 2,050 times, then the decider approves | PASS (refused) | 2050 rewrites accepted; the manager's approval then: state=approved |
| kernel | purchase | an approved purchase whose order cannot be placed leaves a trace | PASS (recorded) | state=approved order=None history=['ITEM_EVENT_KIND_TRANSITION_REQUESTED', 'ITEM_EVENT_KIND_TRANSITIONED', 'ITEM_EVENT_KIND_TRANSITIONED', 'ITEM_EVENT_KIND_HOOK_FAILED'] |
| kernel | purchase | a colleague with no part in it reads ana's purchase | FAIL (allowed) | every member of staff can read every request: these Types narrow nothing |
| kernel | purchase | ... and its history | FAIL (allowed) |  |
| kernel | purchase | the agent reads it (it must, to prepare) | ALLOWED (its job) |  |
| kernel | purchase | the same request sent twice makes one purchase | PASS (one) | two purchases: False (the same request ID) |
| kernel | purchase | a colleague in finance can take over a purchase fay claimed and left | PASS (can) | gus put it back in the queue |

- kernel, purchase: 4 of 7 attempts refused, 2 succeeded, 1 allowed by design.
- plain, access: 1 of 1 attempts refused, 0 succeeded, 0 allowed by design.
- plain, purchase: 6 of 7 attempts refused, 0 succeeded, 1 allowed by design.
