# The reviewer's attempts on the kernel

Generated on 2026-11-24 12:28 UTC. 

| Side | Process | Attempt | Result | Evidence |
|---|---|---|---|---|
| kernel | purchase | block the decision: the agent rewrites its preparation 2,050 times, then the decider approves | PASS (refused) | 2050 rewrites accepted; the manager's approval then: state=approved |
| kernel | purchase | an approved purchase whose order cannot be placed leaves a trace | PASS (recorded) | state=approved order=None history=['ITEM_EVENT_KIND_TRANSITION_REQUESTED', 'ITEM_EVENT_KIND_TRANSITIONED', 'ITEM_EVENT_KIND_TRANSITIONED', 'ITEM_EVENT_KIND_HOOK_FAILED'] |
| kernel | purchase | a colleague with no part in it reads ana's purchase | FAIL (allowed) | every member of staff can read every request: these Types narrow nothing |
| kernel | purchase | ... and its history | FAIL (allowed) |  |
| kernel | purchase | the agent reads it (it must, to prepare) | ALLOWED (its job) |  |
| kernel | purchase | the same request sent twice makes one purchase | PASS (one) | two purchases: False (the same request ID) |
| kernel | purchase | a colleague in finance can take over a purchase fay claimed and left | PASS (can) | gus put it back in the queue |

- kernel, purchase: 4 of 7 attempts refused, 2 succeeded, 1 allowed by design.
