# Can a Type close the kernel's two holes? (role staff on what is asked and the reason)

Generated on 2026-11-24 12:41 UTC. Types: kernel-staff-role/types. The agent holds role preparer only; every person also holds staff.

| Side | Process | Attempt | Result | Evidence |
|---|---|---|---|---|
| kernel-staff-role | purchase | change the amount and vendor of ana's draft before she submits it | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_amount,f_vendor |
| kernel-staff-role | purchase | ... the same, acting for ana with her grant | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_amount |
| kernel-staff-role | purchase | a colleague (olaf, staff) changes ana's draft | FAIL (allowed: not the agent) | allowed: staff is every person |
| kernel-staff-role | purchase | plant a rejection reason while finance reviews | PASS (refused) | error: PERMISSION_DENIED FIELD_NOT_WRITABLE: the caller may not write these fields fields: f_reason |
| kernel-staff-role | purchase | ... so fay can reject without giving a reason | PASS (refused) | error: FAILED_PRECONDITION MISSING_FIELDS: the transition needs fields that are not set fields: f_reason from_state: finance_review to_state: rejected |
| kernel-staff-role | purchase | fay rejects, giving a reason (must still work) | PASS (works) | state=rejected |
| kernel-staff-role | purchase | the agent still writes its preparation (must still work) | PASS (works) | written |

- kernel-staff-role, purchase: 6 of 7 attempts refused, 1 succeeded, 0 allowed by design.
