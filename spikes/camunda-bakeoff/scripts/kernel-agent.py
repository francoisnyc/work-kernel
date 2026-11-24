"""The kernel side's agent: prepares each decision, never decides.

It acts as agent:prep-bot in an agent session with only read and
edit_fields. Each Item waiting for a decision with no summary yet gets
one model call; the answer is written to f_summary, f_policy and f_flags,
the only fields the Types let the preparer role write. The model's
recommendation is not stored: a person decides.

Usage: python3 scripts/kernel-agent.py [--watch]
"""
import sys
import time

from bakeoff import ask_model, pli, short

# Only the first decision is prepared: the Types freeze the preparation after it.
WAITING = {"purchase": ["manager_review"], "system_access": ["owner_review"]}


def once(session):
    done = 0
    for typ, states in WAITING.items():
        args = ["item", "search", "--type", f"namespaces/acme/types/{typ}", "--empty", "f_summary"]
        for s in states:
            args += ["--state", s]
        _, res = pli("prep-bot", *args, session=session)
        for it in res.get("items", []):
            f = it["fields"]
            if typ == "purchase":
                req = {"kind": "purchase", "amount": int(f["f_amount"]), "vendor": f["f_vendor"],
                       "cost_centre": f["f_cost_centre"], "justification": f["f_justification"]}
            else:
                req = {"kind": "access", "system": f.get("f_system_name"), "role": f["f_role"],
                       "justification": f["f_justification"]}
            prep = ask_model(req)
            ok, out = pli("prep-bot", "item", "update", short(it["name"]),
                          "--set", f"f_summary={prep['summary']}", "--set", f"f_policy={prep['policy']}",
                          "--set", f"f_flags={prep['flags']}", session=session, check=False)
            print(f"{typ} {short(it['name'])}: {'prepared' if ok else 'refused: ' + str(out)}"
                  f" (model recommended {prep['recommendation']}, not stored)")
            done += 1
    return done


def main():
    _, s = pli("prep-bot", "agent", "start", "--scopes", "namespaces/acme", "--powers", "read,edit_fields")
    session = s["session"]["id"]
    if "--watch" in sys.argv:
        while True:
            once(session)
            time.sleep(1)
    else:
        once(session)


if __name__ == "__main__":
    main()
