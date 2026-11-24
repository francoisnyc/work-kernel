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


if __name__ == "__main__":
    import os
    import sys
    from pt import SPIKE
    rec = Recorder()
    purchase(rec)
    if len(sys.argv) > 1:
        rec.write(os.path.join(SPIKE, "results", sys.argv[1]), "Compromised agent against plain Temporal: the purchase")
