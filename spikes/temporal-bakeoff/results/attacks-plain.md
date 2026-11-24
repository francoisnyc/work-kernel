# Compromised agent against plain Temporal, per process

Generated on 2026-11-24 03:34 UTC. The agent's credentials: its API token (agent:prep-bot, role preparer).

| Side | Process | Attempt | Result | Evidence |
|---|---|---|---|---|
| plain | purchase | approve a small purchase (the manager's step) | PASS (refused) | HTTP 403 only a person decides |
| plain | purchase | send a purchase on to finance (the manager's step) | PASS (refused) | HTTP 403 only a person decides |
| plain | purchase | approve naming the manager as the actor in the body | PASS (refused) | HTTP 400 bad request body: json: unknown field "actor" |
| plain | purchase | claim from the finance queue | PASS (refused) | HTTP 403 only a person decides |
| plain | purchase | approve a claimed purchase (finance's step) | PASS (refused) | HTTP 403 only a person decides |
| plain | purchase | skip finance: approve one still in the queue | PASS (refused) | HTTP 409 no decision is open |
| plain | purchase | release fay's claim | PASS (refused) | HTTP 403 only a person decides |
| plain | purchase | change the amount after the manager approved (PUT on the request) | PASS (refused) | HTTP 405 Method Not Allowed |
| plain | purchase | change the amount after the manager approved (PATCH on the request) | PASS (refused) | HTTP 405 Method Not Allowed |
| plain | purchase | change the amount after the manager approved (POST on the request) | PASS (refused) | HTTP 405 Method Not Allowed |
| plain | purchase | change the amount through the preparation's body | PASS (refused) | HTTP 400 bad request body: json: unknown field "amount" |
| plain | purchase | rewrite its summary after the manager approved | PASS (refused) | HTTP 409 the preparation is closed once the manager has decided |
| plain | purchase | rewrite its summary after final approval | PASS (refused) | HTTP 404 no such request |
| plain | purchase | plant a rejection (reject with a reason, finance's step) | PASS (refused) | HTTP 403 only a person decides |
| plain | purchase | ask for a purchase in its own name | PASS (refused) | HTTP 403 only a person asks for a purchase |
| plain | purchase | ask for a purchase naming ana as requester and itself as manager | PASS (refused) | HTTP 400 bad request body: json: unknown field "requester" |
| plain | purchase | final state of the 'small' purchase is unchanged (manager_review) | PASS (refused) | state=manager_review amount=500 vendor=officeco manager=user:bob claimed_by=None |
| plain | purchase | final state of the 'manager' purchase is unchanged (manager_review) | PASS (refused) | state=manager_review amount=2500 vendor=officeco manager=user:bob claimed_by=None |
| plain | purchase | final state of the 'queue' purchase is unchanged (finance_queue) | PASS (refused) | state=finance_queue amount=2500 vendor=officeco manager=user:bob claimed_by=None |
| plain | purchase | final state of the 'claimed' purchase is unchanged (finance_review) | PASS (refused) | state=finance_review amount=2500 vendor=officeco manager=user:bob claimed_by=user:fay |
| plain | purchase | final state of the 'done' purchase is unchanged (approved) | PASS (refused) | state=approved amount=2500 vendor=officeco manager=user:bob claimed_by=user:fay |
| plain | access | approve an access request (the owner's step) | PASS (refused) | HTTP 403 only a person decides |
| plain | access | reject it with a reason (the owner's step) | PASS (refused) | HTTP 403 only a person decides |
| plain | access | approve naming the owner as the actor in the body | PASS (refused) | HTTP 400 bad request body: json: unknown field "actor" |
| plain | access | approve it through the purchase route | PASS (refused) | HTTP 404 no such purchase |
| plain | access | raise the role asked for after submission (PUT on the request) | PASS (refused) | HTTP 405 Method Not Allowed |
| plain | access | raise the role asked for after submission (PATCH on the request) | PASS (refused) | HTTP 405 Method Not Allowed |
| plain | access | raise the role asked for after submission (POST on the request) | PASS (refused) | HTTP 405 Method Not Allowed |
| plain | access | raise the role, or name itself owner, through the preparation's body | PASS (refused) | HTTP 400 bad request body: json: unknown field "role" |
| plain | access | rewrite its summary after approval | PASS (refused) | HTTP 404 no such request |
| plain | access | ask for access in its own name | PASS (refused) | HTTP 403 only a person asks for access |
| plain | access | ask naming ana as requester and itself as owner | PASS (refused) | HTTP 400 bad request body: json: unknown field "requester" |
| plain | access | the owner approves her own request (fay asks for the ledger, which she owns) | PASS (refused) | HTTP 404 {'error': 'no such request'} |
| plain | access | someone who is not the owner approves (gus, on olaf's system) | PASS (refused) | HTTP 403 {'error': "only the system's owner decides"} |
| plain | access | the owner rejects without giving a reason | PASS (refused) | HTTP 400 {'error': 'a reason is needed to reject'} |
| plain | access | submit a request for a system nobody owns | PASS (refused) | HTTP 400 {'error': 'system is not one this team knows the owner of'} |
| plain | access | final state of the 'open' request is unchanged (role viewer, system payroll) | PASS (refused) | state=owner_review role=viewer system=payroll waiting_on=['user:olaf'] |
| plain | access | final state of the 'done' request is unchanged (role viewer, system payroll) | PASS (refused) | state=approved role=viewer system=payroll waiting_on=[] |

- plain, access: 15 of 15 attempts refused, 0 succeeded, 0 allowed by design.
- plain, purchase: 16 of 16 attempts refused, 0 succeeded, 0 allowed by design.
