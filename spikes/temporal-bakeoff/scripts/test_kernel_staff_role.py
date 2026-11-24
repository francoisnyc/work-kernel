"""The reviewer's suggestion, tested: can a Type close the kernel's two
holes (a draft changed by the agent before its owner submits; a rejection
reason planted by the agent) by making what is asked, and the reason,
writable only by a role every person holds and the agent does not?

Restarts the kernel side, fresh, on kernel-staff-role/ (the first bake-off's
Types with that one change, and its dev tokens with every person holding
role staff). Writes results/kernel-staff-role.md. Leaves the kernel side on
that folder: run ../camunda-bakeoff/scripts/kernel-down.sh afterwards.
"""
import os
import subprocess

from attacks_plain import Recorder
from pt import FIRST, SPIKE, pli, short

V = f"{SPIKE}/kernel-staff-role"
subprocess.run([f"{FIRST}/scripts/kernel-down.sh"], capture_output=True)
subprocess.run([f"{FIRST}/scripts/kernel-up.sh"], check=True, capture_output=True, env=dict(os.environ, TYPES=f"{V}/types"))
subprocess.run([f"{FIRST}/scripts/kernel-restart-itemservice.sh", f"{V}/types", f"{V}/dev-tokens.yaml"], check=True, capture_output=True)

rec = Recorder()


def draft(amount=2500):
    _, c = pli("ana", "item", "create", "--type", "purchase@1", "--set", f"f_amount={amount}", "--set", "f_vendor=officeco",
               "--set", "f_cost_centre=CC-100", "--set", "f_justification=Monitors")
    return short(c["item"]["name"])


def to(i, state, who, *extra):
    ok, out = pli(who, "item", "transition", i, "--to", state, *extra, check=False)
    if not ok:
        raise RuntimeError(out)


def attempt(attack, as_, session, *args):
    ok, out = pli(as_, *args, session=session, check=False)
    rec.add("kernel-staff-role", "purchase", attack, ok, out if not ok else f"state={out['item']['state']} fields={out['item']['fields']}")
    return ok


_, s = pli("prep-bot", "agent", "start", "--scopes", "namespaces/acme", "--powers", "read,create,edit_fields,transition:*,move")
own = s["session"]["id"]
_, s = pli("prep-bot", "agent", "start", "--scopes", "namespaces/acme", "--powers", "read,create,edit_fields,transition:*,move", "--for", "user:ana")
as_ana = s["session"]["id"]

d = draft()
attempt("change the amount and vendor of ana's draft before she submits it", "prep-bot", own, "item", "update", d, "--set", "f_amount=950", "--set", "f_vendor=shadyparts")
attempt("... the same, acting for ana with her grant", "prep-bot", as_ana, "item", "update", d, "--set", "f_amount=950")
ok, out = pli("olaf", "item", "update", d, "--set", "f_amount=950", check=False)
rec.add("kernel-staff-role", "purchase", "a colleague (olaf, staff) changes ana's draft", ok, "allowed: staff is every person" if ok else out,
        verdict="FAIL (allowed: not the agent)" if ok else None)
pli("ana", "item", "update", d, "--set", "f_amount=2500")
to(d, "manager_review", "ana", "--wait", "5s")
to(d, "finance_queue", "bob")
to(d, "finance_review", "fay", "--set", "f_claimed_by=user:fay")
attempt("plant a rejection reason while finance reviews", "prep-bot", own, "item", "update", d, "--set", "f_reason=planted by the agent")
ok, out = pli("fay", "item", "transition", d, "--to", "rejected", check=False)
rec.add("kernel-staff-role", "purchase", "... so fay can reject without giving a reason", ok, out if not ok else "rejected with no reason")
ok, out = pli("fay", "item", "transition", d, "--to", "rejected", "--set", "f_reason=Over budget", check=False)
rec.add("kernel-staff-role", "purchase", "fay rejects, giving a reason (must still work)", not ok,
        f"state={out['item']['state']}" if ok else out, verdict="PASS (works)" if ok else "FAIL (broken)")
_, s2 = pli("prep-bot", "agent", "start", "--scopes", "namespaces/acme", "--powers", "read,edit_fields")
ok, out = pli("prep-bot", "item", "update", draft(), "--set", "f_summary=prepared", "--set", "f_policy=ok", "--set", "f_flags=none", session=s2["session"]["id"], check=False)
rec.add("kernel-staff-role", "purchase", "the agent still writes its preparation (must still work)", not ok, "written" if ok else out,
        verdict="PASS (works)" if ok else "FAIL (broken)")
rec.write(os.path.join(SPIKE, "results/kernel-staff-role.md"), "Can a Type close the kernel's two holes? (role staff on what is asked and the reason)",
          "Types: kernel-staff-role/types. The agent holds role preparer only; every person also holds staff.")
