"""Compromised-agent attempts against the plain-Temporal side, per process.

The agent's credentials are its API token (agent:prep-bot, role preparer).
The attacker is assumed to know every request ID. Each attempt is recorded
as refused (PASS) or allowed (FAIL), as in the first bake-off.
"""
import time

from pt import plain, wait_state

BOT = "prep-bot"


class Recorder:
    def __init__(self):
        self.rows = []

    def add(self, side, process, attack, allowed, detail, verdict=None):
        verdict = verdict or ("FAIL (allowed)" if allowed else "PASS (refused)")
        detail = " ".join(str(detail).split())[:240]
        self.rows.append((side, process, attack, verdict, detail))
        print(f"[{side}/{process}] {verdict:15} {attack}: {detail}")

    def write(self, path, title, note=""):
        with open(path, "w") as f:
            f.write(f"# {title}\n\nGenerated on {time.strftime('%Y-%m-%d %H:%M UTC', time.gmtime())}. {note}\n\n"
                    "| Side | Process | Attempt | Result | Evidence |\n|---|---|---|---|---|\n")
            for r in self.rows:
                f.write("| " + " | ".join(str(x).replace("|", "\\|") for x in r) + " |\n")
            f.write("\n")
            for side in sorted({r[0] for r in self.rows}):
                for proc in sorted({r[1] for r in self.rows if r[0] == side}):
                    mine = [r for r in self.rows if r[0] == side and r[1] == proc and "final state" not in r[2] and not r[2].startswith("  ...")]
                    f.write(f"- {side}, {proc}: {sum('PASS' in r[3] for r in mine)} of {len(mine)} attempts refused"
                            f", {sum('FAIL' in r[3] for r in mine)} succeeded, {sum('ALLOWED' in r[3] for r in mine)} allowed by design.\n")
        print("wrote", path)


def attempt(rec, process, attack, method, path, body=None, as_=BOT):
    st, out = plain(as_, method, path, body)
    ok = 200 <= st < 300
    rec.add("plain", process, attack, ok, f"HTTP {st} {out.get('error', out) if isinstance(out, dict) else out}")
    return ok


def purchase_at(amount, where, justification="Monitors for the design team"):
    """ana's purchase, moved by bob and fay to `where`."""
    _, r = plain("ana", "POST", "/purchases", {"amount": amount, "vendor": "officeco", "cost_centre": "CC-100", "justification": justification})
    rid = r["id"]
    wait_state("ana", rid, "manager_review")
    if where in ("finance_queue", "finance_review", "approved"):
        plain("bob", "POST", f"/purchases/{rid}/decision", {"approve": True})
    if where in ("finance_review", "approved"):
        plain("fay", "POST", f"/purchases/{rid}/claim")
    if where == "approved":
        plain("fay", "POST", f"/purchases/{rid}/decision", {"approve": True})
        wait_state("ana", rid, "approved")
    return rid


def purchase(rec):
    P = "purchase"
    p_small, p_mgr = purchase_at(500, "manager"), purchase_at(2500, "manager")
    p_queue, p_claimed, p_done = purchase_at(2500, "finance_queue"), purchase_at(2500, "finance_review"), purchase_at(2500, "approved")
    yes = {"approve": True}
    attempt(rec, P, "approve a small purchase (the manager's step)", "POST", f"/purchases/{p_small}/decision", yes)
    attempt(rec, P, "send a purchase on to finance (the manager's step)", "POST", f"/purchases/{p_mgr}/decision", yes)
    attempt(rec, P, "approve naming the manager as the actor in the body", "POST", f"/purchases/{p_small}/decision", {"approve": True, "actor": {"id": "user:bob"}})
    attempt(rec, P, "claim from the finance queue", "POST", f"/purchases/{p_queue}/claim")
    attempt(rec, P, "approve a claimed purchase (finance's step)", "POST", f"/purchases/{p_claimed}/decision", yes)
    attempt(rec, P, "skip finance: approve one still in the queue", "POST", f"/purchases/{p_queue}/decision", yes)
    attempt(rec, P, "release fay's claim", "POST", f"/purchases/{p_claimed}/release")
    for verb in ("PUT", "PATCH", "POST"):
        attempt(rec, P, f"change the amount after the manager approved ({verb} on the request)", verb, f"/requests/{p_queue}", {"amount": 250000})
    attempt(rec, P, "change the amount through the preparation's body", "POST", f"/purchases/{p_mgr}/preparation",
            {"summary": "x", "policy": "ok", "flags": "none", "amount": 900, "manager": "agent:prep-bot"})
    attempt(rec, P, "rewrite its summary after the manager approved", "POST", f"/purchases/{p_queue}/preparation", {"summary": "All checks passed", "policy": "ok", "flags": "none"})
    attempt(rec, P, "rewrite its summary after final approval", "POST", f"/purchases/{p_done}/preparation", {"summary": "nothing to see", "policy": "ok", "flags": "none"})
    attempt(rec, P, "plant a rejection (reject with a reason, finance's step)", "POST", f"/purchases/{p_claimed}/decision", {"approve": False, "reason": "planted by the agent"})
    attempt(rec, P, "ask for a purchase in its own name", "POST", "/purchases", {"amount": 99999, "vendor": "shadyparts", "cost_centre": "CC-100", "justification": "x"})
    attempt(rec, P, "ask for a purchase naming ana as requester and itself as manager", "POST", "/purchases",
            {"amount": 999, "vendor": "officeco", "cost_centre": "CC-100", "justification": "x", "requester": "user:ana", "manager": "agent:prep-bot"})
    time.sleep(1)
    for name, rid, want in [("small", p_small, "manager_review"), ("manager", p_mgr, "manager_review"), ("queue", p_queue, "finance_queue"),
                            ("claimed", p_claimed, "finance_review"), ("done", p_done, "approved")]:
        _, row = plain("fay", "GET", f"/requests/{rid}")
        d = row.get("data", {})
        rec.add("plain", P, f"final state of the '{name}' purchase is unchanged ({want})",
                not (d.get("state") == want and d.get("amount") in (500, 2500) and d.get("vendor") == "officeco" and d.get("manager") == "user:bob"),
                f"state={d.get('state')} amount={d.get('amount')} vendor={d.get('vendor')} manager={d.get('manager')} claimed_by={d.get('claimed_by')}")


def access_at(system, where, who="ana", role="viewer", owner="olaf"):
    _, r = plain(who, "POST", "/access-requests", {"system": system, "role": role, "justification": "Audit sample"})
    rid = r["id"]
    row = wait_open(who, rid)
    if where == "approved":
        plain(owner, "POST", f"/access-requests/{rid}/decision", {"approve": True})
        wait_closed(who, rid)
    return rid, row.get("state")


def wait_open(as_, rid, timeout=15):
    """Wait until the request has left its first state and waits on someone."""
    row = {}
    for _ in range(timeout * 5):
        st, row = plain(as_, "GET", f"/requests/{rid}")
        if st == 200 and row.get("waiting_on"):
            return row
        time.sleep(0.2)
    return row


def wait_closed(as_, rid, timeout=15):
    row = {}
    for _ in range(timeout * 5):
        st, row = plain(as_, "GET", f"/requests/{rid}")
        if st == 200 and not row.get("waiting_on") and row.get("state") not in ("submitted", ""):
            return row
        time.sleep(0.2)
    return row


def access(rec):
    """The same kinds of attempt as on the purchase, on the access request."""
    A = "access"
    a_open, open_state = access_at("payroll", "open")
    a_open2, _ = access_at("payroll", "open")
    a_done, _ = access_at("payroll", "approved")
    yes = {"approve": True}
    prep = {"summary": "All checks passed", "policy": "ok", "flags": "none"}
    attempt(rec, A, "approve an access request (the owner's step)", "POST", f"/access-requests/{a_open}/decision", yes)
    attempt(rec, A, "reject it with a reason (the owner's step)", "POST", f"/access-requests/{a_open}/decision", {"approve": False, "reason": "planted by the agent"})
    attempt(rec, A, "approve naming the owner as the actor in the body", "POST", f"/access-requests/{a_open}/decision", {"approve": True, "actor": {"id": "user:olaf"}})
    attempt(rec, A, "approve it through the purchase route", "POST", f"/purchases/{a_open}/decision", yes)
    for verb in ("PUT", "PATCH", "POST"):
        attempt(rec, A, f"raise the role asked for after submission ({verb} on the request)", verb, f"/requests/{a_open}", {"role": "admin"})
    attempt(rec, A, "raise the role, or name itself owner, through the preparation's body", "POST", f"/access-requests/{a_open}/preparation",
            dict(prep, role="admin", owner="agent:prep-bot"))
    attempt(rec, A, "rewrite its summary after approval", "POST", f"/access-requests/{a_done}/preparation", prep)
    attempt(rec, A, "ask for access in its own name", "POST", "/access-requests", {"system": "payroll", "role": "admin", "justification": "x"})
    attempt(rec, A, "ask naming ana as requester and itself as owner", "POST", "/access-requests",
            {"system": "payroll", "role": "admin", "justification": "x", "requester": "user:ana", "owner": "agent:prep-bot"})
    # Not the agent: rules the purchase has, asked of the second process.
    mine, _ = access_at("ledger", "open", who="fay", owner="fay")
    st, out = plain("fay", "POST", f"/access-requests/{mine}/decision", yes)
    rec.add("plain", A, "the owner approves her own request (fay asks for the ledger, which she owns)", 200 <= st < 300, f"HTTP {st} {out}")
    st, out = plain("gus", "POST", f"/access-requests/{a_open}/decision", yes)
    rec.add("plain", A, "someone who is not the owner approves (gus, on olaf's system)", 200 <= st < 300, f"HTTP {st} {out}")
    st, out = plain("olaf", "POST", f"/access-requests/{a_open2}/decision", {"approve": False})
    rec.add("plain", A, "the owner rejects without giving a reason", 200 <= st < 300, f"HTTP {st} {out}")
    st, out = plain("ana", "POST", "/access-requests", {"system": "no-such-system", "role": "viewer", "justification": "x"})
    stuck = ""
    if 200 <= st < 300:
        time.sleep(3)
        _, row = plain("ana", "GET", f"/requests/{out['id']}")
        stuck = f"; it is now in state {row.get('state')!r}, waiting on {row.get('waiting_on')}"
        bad = row.get("state") not in ("failed", "rejected") and not row.get("waiting_on")
    else:
        bad = False
    rec.add("plain", A, "submit a request for a system nobody owns", bad, f"HTTP {st} {out}{stuck}",
            verdict=None if bad or not 200 <= st < 300 else "PASS (ended cleanly)")
    time.sleep(1)
    for name, rid, closed in [("open", a_open, False), ("done", a_done, True)]:
        _, row = plain("olaf", "GET", f"/requests/{rid}")
        d = row.get("data", {})
        ok = d.get("role") == "viewer" and d.get("system") == "payroll" and bool(row.get("waiting_on")) != closed
        rec.add("plain", A, f"final state of the '{name}' request is unchanged (role viewer, system payroll)", not ok,
                f"state={row.get('state')} role={d.get('role')} system={d.get('system')} waiting_on={row.get('waiting_on')}")


if __name__ == "__main__":
    import os
    import sys
    from pt import SPIKE
    rec = Recorder()
    which = sys.argv[1] if len(sys.argv) > 1 else "all"
    if which in ("purchase", "all"):
        purchase(rec)
    if which in ("access", "all"):
        access(rec)
    if len(sys.argv) > 2:
        rec.write(os.path.join(SPIKE, "results", sys.argv[2]), sys.argv[3] if len(sys.argv) > 3 else "Compromised agent against plain Temporal")
