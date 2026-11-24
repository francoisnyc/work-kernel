# The reviewer's attempts on plain Temporal, before any fix

Generated on 2026-11-24 04:02 UTC. 

| Side | Process | Attempt | Result | Evidence |
|---|---|---|---|---|
| plain | purchase | block the decision: the agent rewrites its preparation 2,050 times, then the decider approves | FAIL (allowed) | 2000 rewrites accepted; the decider's approval then answered HTTP 500 {'error': 'internal error'} |
| plain | access | block the decision: the agent rewrites its preparation 2,050 times, then the decider approves | FAIL (allowed) | 2000 rewrites accepted; the decider's approval then answered HTTP 500 {'error': 'internal error'} |
| plain | purchase | an approved purchase whose order cannot be placed leaves a trace | FAIL (silent) | state=approved order=None failure=None history=['submitted', 'decided'] |
| plain | purchase | a colleague with no part in it reads ana's purchase | PASS (refused) | HTTP 404 |
| plain | purchase | ... and its history | PASS (refused) | HTTP 404 |
| plain | purchase | the agent reads it (it must, to prepare) | ALLOWED (its job) | HTTP 200 |
| plain | purchase | the same request sent twice makes one purchase | FAIL (two made) | two purchases: True (no request ID on submission) |
| plain | purchase | a colleague in finance can take over a purchase fay claimed and left | FAIL (stuck with the claimer) | gus's release: HTTP 403 {'error': "this step is someone else's"} |

- plain, access: 0 of 1 attempts refused, 1 succeeded, 0 allowed by design.
- plain, purchase: 2 of 7 attempts refused, 4 succeeded, 1 allowed by design.
