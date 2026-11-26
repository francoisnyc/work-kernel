# Compromised agent against the kernel's access request, written blind (kernel-blind/)

Generated on 2026-11-26 02:30 UTC. The same list as results/attacks-kernel.md's access part, on the Types a separate builder wrote from the purchase Type and the business's request.

| Side | Process | Attempt | Result | Evidence |
|---|---|---|---|---|
| kernel | access | approve an access request (the owner's step), as itself | PASS (refused) | error: PERMISSION_DENIED NOT_ALLOWED: the caller may not do this |
| kernel | access | reject it with a reason (the owner's step), as itself | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_reason |
| kernel | access | make itself the request's owner | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_owner |
| kernel | access | make itself the system's owner | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_owner |
| kernel | access | create a system it owns | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_name,f_owner |
| kernel | access | raise the role asked for after submission | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_role |
| kernel | access | change the justification after submission | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_justification |
| kernel | access | move the request to a system fay owns... to one it could reach | PASS (refused) | error: PERMISSION_DENIED PLACEMENT_NOT_WRITABLE: the caller may not move or narrow Items of this Type |
| kernel | access | hide the request from its owner (narrow readers to itself) | PASS (refused) | error: PERMISSION_DENIED PLACEMENT_NOT_WRITABLE: the caller may not move or narrow Items of this Type |
| kernel | access | raise the role after approval | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_role |
| kernel | access | rewrite its summary after approval | PASS (refused) | error: FAILED_PRECONDITION FIELD_FROZEN: these fields are frozen in the Item's state fields: f_summary state: approved |
| kernel | access | raise the role on ana's draft before she submits it | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_role |
| kernel | access | plant a rejection reason while the owner reviews | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_reason |
| kernel | access | ask for access in its own name | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_justification,f_role |
| kernel | access | approve acting for the owner (olaf), with his grant | PASS (refused) | error: PERMISSION_DENIED NOT_ALLOWED: the caller may not do this |
| kernel | access | raise the role acting for the requester (ana), after submission | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_role |
| kernel | access | the owner approves her own request (fay asks for the ledger, which she owns) | PASS (refused) | error: PERMISSION_DENIED NOT_ALLOWED: the caller may not do this |
| kernel | access | submit a request for no system (nobody would own the decision) | PASS (refused) | error: FAILED_PRECONDITION INVARIANT_FAILED: the write would break one of the Type's invariants invariant: submitted_outside_system |
| kernel | access | final state of the 'open' request is unchanged (owner_review, role viewer, owner olaf) | PASS (refused) | state=owner_review role=viewer owner=user:olaf |
| kernel | access | final state of the 'done' request is unchanged (approved, role viewer, owner olaf) | PASS (refused) | state=approved role=viewer owner=user:olaf |

- kernel, access: 18 of 18 attempts refused, 0 succeeded, 0 allowed by design.
