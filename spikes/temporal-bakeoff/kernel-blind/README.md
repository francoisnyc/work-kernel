# Access requests on the kernel

Two new Types beside the purchase (`types/purchase.v1.yaml`, unchanged):

- `system@1` (`types/system.v1.yaml`): one Item per system, holding its
  name (`f_name`) and its owner (`f_owner`). Only `it_admin` (ivy) writes
  them. No lifecycle.
- `system_access@1` (`types/system_access.v1.yaml`): the request, made
  **inside** the system's Item. `draft -> owner_review -> approved | rejected`.

All commands run from `spikes/camunda-bakeoff`.

## Once: start the kernel on this folder and create the systems

    scripts/kernel-down.sh
    TYPES=/home/user/work-kernel/spikes/temporal-bakeoff/kernel-blind/types scripts/kernel-up.sh
    ../temporal-bakeoff/kernel-blind/setup.sh

`setup.sh` creates `payroll` (owner `user:olaf`) and `ledger` (owner
`user:fay`) as ivy. Running it again changes nothing. A new system, or a
new owner, is one write by ivy:

    scripts/pli.sh --as ivy item create --type system@1 --set f_name=crm --set f_owner=user:gus
    scripts/pli.sh --as ivy item update SYSTEM --set f_owner=user:gus

A new owner decides only requests submitted after the change.

## A person submits a request (ana, for payroll)

    # 1. Find the system's Item: its ID is the last part of "name".
    scripts/pli.sh --as ana item search --type namespaces/acme/types/system

    # 2. Create the request inside it, then submit it.
    scripts/pli.sh --as ana item create --type system_access@1 --in SYSTEM \
        --set f_role=admin --set "f_justification=I run the month-end payroll close"
    scripts/pli.sh --as ana item transition REQUEST --to owner_review

Submitting copies the system's name and owner onto the request
(`f_system_name`, `f_owner`) and freezes the role and the justification.

## The agent prepares (prep-bot; it never decides)

    scripts/pli.sh --as prep-bot agent start --scopes namespaces/acme --powers read,edit_fields
    scripts/pli.sh --as prep-bot --session SESSION item search \
        --type namespaces/acme/types/system_access --state owner_review --empty f_summary
    scripts/pli.sh --as prep-bot --session SESSION item update REQUEST \
        --set "f_summary=..." --set f_policy=check --set "f_flags=..."

`f_policy` is `ok`, `check` or `fail`.

## The owner decides (olaf, for payroll)

    # What waits for me:
    scripts/pli.sh --as olaf item search --type namespaces/acme/types/system_access \
        --state owner_review --where f_owner=me

    scripts/pli.sh --as olaf item transition REQUEST --to approved
    scripts/pli.sh --as olaf item transition REQUEST --to rejected --set "f_reason=Admin is for the payroll team only"

A rejection needs `f_reason`; the requester is then told (`email.notify`,
hook `notify_rejected`, as for a purchase). An approval does nothing more.

## Open question for the business

Nobody decides a request they made themselves (as for a purchase). So an
owner who asks for access to their own system waits for ever: there is no
withdraw move, because none was asked for.
