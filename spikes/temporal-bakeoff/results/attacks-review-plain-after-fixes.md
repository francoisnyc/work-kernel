# The reviewer's attempts on plain Temporal, after its fixes

Generated on 2026-11-24 12:33 UTC. 

| Side | Process | Attempt | Result | Evidence |
|---|---|---|---|---|
| plain | purchase | block the decision: the agent rewrites its preparation 2,050 times, then the decider approves | PASS (refused) | 20 rewrites accepted; the decider's approval then answered HTTP 204 {} |
| plain | access | block the decision: the agent rewrites its preparation 2,050 times, then the decider approves | PASS (refused) | 20 rewrites accepted; the decider's approval then answered HTTP 204 {} |
| plain | purchase | an approved purchase whose order cannot be placed leaves a trace | PASS (recorded) | state=order_failed order=None failure=the purchase system refused the order: activity error (type: PlaceOrder, scheduledEventID: 28, startedEventID: 29, identity: 28360@vm@): POST http://127.0.0.1:18082/invoices: HTTP 404 history=['submitte |
| plain | purchase | a colleague with no part in it reads ana's purchase | PASS (refused) | HTTP 404 |
| plain | purchase | ... and its history | PASS (refused) | HTTP 404 |
| plain | purchase | the agent reads it (it must, to prepare) | ALLOWED (its job) | HTTP 200 |
| plain | purchase | the same request sent twice makes one purchase | PASS (one) | two purchases: False (with request ID on submission) |
| plain | purchase | a colleague in finance can take over a purchase fay claimed and left | PASS (can) | gus's release: HTTP 204 {} |

- plain, access: 1 of 1 attempts refused, 0 succeeded, 0 allowed by design.
- plain, purchase: 6 of 7 attempts refused, 0 succeeded, 1 allowed by design.
