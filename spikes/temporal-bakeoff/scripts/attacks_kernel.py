"""Compromised-agent attempts against the kernel side, per process.

The purchase list is the first bake-off's (spikes/camunda-bakeoff/scripts/
test2-compromised.py, imported by path and run unchanged). The access
request list is new: the same kinds of attempt, on system_access@1.
The agent holds its token and every grant (every power; acting for itself,
the owner, the requester), as in the first bake-off.
"""
import importlib
import json
import time

from pt import pli, short

first = importlib.import_module("test2-compromised")  # the first spike's script


def purchase(rec):
    first.rows.clear()
    first.kernel()
    for side, attack, verdict, detail in first.rows:
        rec.rows.append(("kernel", "purchase", attack, verdict, detail))


def system_item(name, owner):
    _, res = pli("ivy", "item", "search", "--type", "namespaces/acme/types/system", "--where", f"f_name={name}")
    if res.get("items"):
        return short(res["items"][0]["name"])
    _, c = pli("ivy", "item", "create", "--type", "system@1", "--set", f"f_name={name}", "--set", f"f_owner=user:{owner}")
    return short(c["item"]["name"])


def access_at(system, where, who="ana", role="viewer"):
    _, c = pli(who, "item", "create", "--type", "system_access@1", "--in", system, "--set", f"f_role={role}", "--set", "f_justification=Audit sample")
    i = short(c["item"]["name"])
    if where in ("owner_review", "approved"):
        pli(who, "item", "transition", i, "--to", "owner_review")
    if where == "approved":
        pli("olaf", "item", "transition", i, "--to", "approved")
    return i


def k_try(rec, attack, as_, session, *args, process="access", verdict=None):
    ok, out = pli(as_, *args, session=session, check=False)
    if ok and "--to" in args and out["item"]["state"] != args[args.index("--to") + 1]:
        ok, out = False, f"the move did not happen: state={out['item']['state']}"
    if ok and isinstance(out, dict) and "item" in out:
        out = f"state={out['item']['state']} fields={json.dumps(out['item']['fields'])}"
    rec.add("kernel", process, attack, ok, out, verdict=verdict if ok else None)
    return ok


def access(rec):
    payroll, ledger = system_item("payroll", "olaf"), system_item("ledger", "fay")
    a_draft, a_open, a_open2, a_done = access_at(payroll, "draft"), access_at(payroll, "owner_review"), access_at(payroll, "owner_review"), access_at(payroll, "approved")
    own = first.k_session()
    bot = "prep-bot"
    k_try(rec, "approve an access request (the owner's step), as itself", bot, own, "item", "transition", a_open, "--to", "approved")
    k_try(rec, "reject it with a reason (the owner's step), as itself", bot, own, "item", "transition", a_open, "--to", "rejected", "--set", "f_reason=planted")
    k_try(rec, "make itself the request's owner", bot, own, "item", "update", a_open, "--set", "f_owner=agent:prep-bot")
    k_try(rec, "make itself the system's owner", bot, own, "item", "update", payroll, "--set", "f_owner=agent:prep-bot")
    k_try(rec, "create a system it owns", bot, own, "item", "create", "--type", "system@1", "--set", "f_name=vault", "--set", "f_owner=agent:prep-bot")
    k_try(rec, "raise the role asked for after submission", bot, own, "item", "update", a_open, "--set", "f_role=admin")
    k_try(rec, "change the justification after submission", bot, own, "item", "update", a_open, "--set", "f_justification=Approved by the CISO")
    k_try(rec, "move the request to a system fay owns... to one it could reach", bot, own, "item", "update", a_open, "--move-to", ledger)
    k_try(rec, "hide the request from its owner (narrow readers to itself)", bot, own, "item", "update", a_open, "--readers", "agent:prep-bot")
    k_try(rec, "raise the role after approval", bot, own, "item", "update", a_done, "--set", "f_role=admin")
    k_try(rec, "rewrite its summary after approval", bot, own, "item", "update", a_done, "--set", "f_summary=nothing to see")
    if k_try(rec, "raise the role on ana's draft before she submits it", bot, own, "item", "update", a_draft, "--set", "f_role=admin"):
        _, it = pli("ana", "item", "transition", a_draft, "--to", "owner_review")
        rec.add("kernel", "access", "  ... ana submits, and what is frozen for approval is the agent's version", True,
                f"state={it['item']['state']} f_role={it['item']['fields']['f_role']}")
    if k_try(rec, "plant a rejection reason while the owner reviews", bot, own, "item", "update", a_open2, "--set", "f_reason=planted by the agent"):
        ok, out = pli("olaf", "item", "transition", a_open2, "--to", "rejected", check=False)
        rec.add("kernel", "access", "  ... so the owner can reject without giving a reason (the Type requires one)", ok,
                f"state={out['item']['state']} f_reason={out['item']['fields'].get('f_reason')}" if ok else out)
    ok, out = pli(bot, "item", "create", "--type", "system_access@1", "--in", payroll, "--set", "f_role=admin", "--set", "f_justification=x", session=own, check=False)
    if ok:
        mine = short(out["item"]["name"])
        k_try(rec, "ask for access in its own name and submit it", bot, own, "item", "transition", mine, "--to", "owner_review",
              verdict="ALLOWED (an agent may ask)")
        k_try(rec, "approve its own request", bot, own, "item", "transition", mine, "--to", "approved")
    else:
        rec.add("kernel", "access", "ask for access in its own name", False, out)
    as_olaf = first.k_session("user:olaf")
    k_try(rec, "approve acting for the owner (olaf), with his grant", bot, as_olaf, "item", "transition", a_open, "--to", "approved")
    as_ana = first.k_session("user:ana")
    k_try(rec, "raise the role acting for the requester (ana), after submission", bot, as_ana, "item", "update", a_open, "--set", "f_role=admin")
    # Not the agent: the owner asks for access to her own system and decides it.
    mine = access_at(ledger, "owner_review", who="fay")
    ok, out = pli("fay", "item", "transition", mine, "--to", "approved", check=False)
    rec.add("kernel", "access", "the owner approves her own request (fay asks for the ledger, which she owns)", ok,
            f"state={out['item']['state']}" if ok else out)
    ok, out = pli("ana", "item", "create", "--type", "system_access@1", "--set", "f_role=viewer", "--set", "f_justification=x", check=False)
    if ok:
        i = short(out["item"]["name"])
        ok, out = pli("ana", "item", "transition", i, "--to", "owner_review", check=False)
    rec.add("kernel", "access", "submit a request for no system (nobody would own the decision)", ok, out if not ok else f"state={out['item']['state']}")
    time.sleep(1)
    for name, i, want, role in [("open", a_open, "owner_review", "viewer"), ("done", a_done, "approved", "viewer")]:
        _, it = pli("olaf", "item", "get", i)
        f = it["item"]["fields"]
        rec.add("kernel", "access", f"final state of the '{name}' request is unchanged ({want}, role {role}, owner olaf)",
                not (it["item"]["state"] == want and f.get("f_role") == role and f.get("f_owner") == "user:olaf"),
                f"state={it['item']['state']} role={f.get('f_role')} owner={f.get('f_owner')}")
