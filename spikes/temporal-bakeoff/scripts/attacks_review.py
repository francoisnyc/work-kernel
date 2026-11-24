"""Attempts the independent reviewer found, on both sides (measure 4,
second round): blocking a decision, an order that cannot be placed, and
reading someone else's request. Run before and after the plain side's
fixes; results/attacks-review-*.md.

Usage: python3 attacks_review.py plain|kernel|all OUTFILE "Title"
"""
import concurrent.futures
import os
import sys
import time

from attacks_plain import Recorder, purchase_at, wait_open
from pt import SPIKE, plain, pli, short, wait_state

FLOOD = 2050  # Temporal accepts 2,000 updates per workflow by default


def plain_flood(rec, process, rid, prep_path, decide_as, decide_path):
    def one(_):
        return plain("prep-bot", "POST", prep_path, {"summary": "s", "policy": "ok", "flags": ""})[0]
    with concurrent.futures.ThreadPoolExecutor(8) as ex:  # a thread pool: the calls are independent
        codes = list(ex.map(one, range(FLOOD)))
    accepted = sum(c == 204 for c in codes)
    st, out = plain(decide_as, "POST", decide_path, {"approve": True})
    rec.add("plain", process, f"block the decision: the agent rewrites its preparation {FLOOD:,} times, then the decider approves",
            not 200 <= st < 300, f"{accepted} rewrites accepted; the decider's approval then answered HTTP {st} {out}")


def plain_side(rec):
    rid = purchase_at(500, "manager")
    plain_flood(rec, "purchase", rid, f"/purchases/{rid}/preparation", "bob", f"/purchases/{rid}/decision")
    _, r = plain("ana", "POST", "/access-requests", {"system": "payroll", "role": "viewer", "justification": "Audit sample"})
    wait_open("ana", r["id"])
    plain_flood(rec, "access", r["id"], f"/access-requests/{r['id']}/preparation", "olaf", f"/access-requests/{r['id']}/decision")

    # An approved purchase whose order the purchase system refuses (a vendor it does not know).
    _, r = plain("ana", "POST", "/purchases", {"amount": 500, "vendor": "ghost-vendor", "cost_centre": "CC-100", "justification": "x"})
    rid = r["id"]
    wait_state("ana", rid, "manager_review")
    plain("bob", "POST", f"/purchases/{rid}/decision", {"approve": True})
    row = {}
    for _ in range(100):  # the order is retried for up to two and a half minutes
        _, row = plain("ana", "GET", f"/requests/{rid}")
        if row.get("data", {}).get("state") not in ("approved", "manager_review") or row.get("data", {}).get("order_id"):
            break
        time.sleep(2)
    d = row.get("data", {})
    _, hist = plain("ana", "GET", f"/requests/{rid}/history")
    actions = [e["action"] for e in hist.get("events", [])]
    silent = d.get("state") == "approved" and not d.get("order_id")
    rec.add("plain", "purchase", "an approved purchase whose order cannot be placed leaves a trace", silent,
            f"state={d.get('state')} order={d.get('order_id')} failure={d.get('failure')} history={actions}",
            verdict="FAIL (silent)" if silent else "PASS (recorded)")

    # Reading: a stranger, and the agent.
    rid = purchase_at(2500, "manager")
    st, _ = plain("olaf", "GET", f"/requests/{rid}")
    rec.add("plain", "purchase", "a colleague with no part in it reads ana's purchase", st == 200, f"HTTP {st}")
    st, _ = plain("olaf", "GET", f"/requests/{rid}/history")
    rec.add("plain", "purchase", "... and its history", st == 200, f"HTTP {st}")
    st, _ = plain("prep-bot", "GET", f"/requests/{rid}")
    rec.add("plain", "purchase", "the agent reads it (it must, to prepare)", st == 200, f"HTTP {st}", verdict="ALLOWED (its job)")
    st, _ = plain("prep-bot", "GET", "/inbox")
    # Two identical submissions.
    body = {"amount": 700, "vendor": "officeco", "cost_centre": "CC-100", "justification": "sent twice", "request_id": f"twice-{time.time()}"}
    st, first = plain("ana", "POST", "/purchases", body)
    if st == 400:  # before the fix the API had no request ID: send the same body without one
        body.pop("request_id")
        first = plain("ana", "POST", "/purchases", body)[1]
    a, b = first.get("id"), plain("ana", "POST", "/purchases", body)[1].get("id")
    rec.add("plain", "purchase", "the same request sent twice makes one purchase", a != b,
            f"two purchases: {a != b} ({'with' if 'request_id' in body else 'no'} request ID on submission)",
            verdict="FAIL (two made)" if a != b else "PASS (one)")
    # The claim: another finance member takes over a claimed purchase.
    rid = purchase_at(2500, "finance_review")
    st, out = plain("gus", "POST", f"/purchases/{rid}/release")
    rec.add("plain", "purchase", "a colleague in finance can take over a purchase fay claimed and left", not 200 <= st < 300,
            f"gus's release: HTTP {st} {out}", verdict="PASS (can)" if 200 <= st < 300 else "FAIL (stuck with the claimer)")


def kernel_side(rec):
    def new(amount=500, vendor="officeco"):
        _, c = pli("ana", "item", "create", "--type", "purchase@1", "--set", f"f_amount={amount}", "--set", f"f_vendor={vendor}",
                   "--set", "f_cost_centre=CC-100", "--set", "f_justification=x")
        i = short(c["item"]["name"])
        pli("ana", "item", "transition", i, "--to", "manager_review", "--wait", "5s")
        return i
    i = new()
    _, s = pli("prep-bot", "agent", "start", "--scopes", "namespaces/acme", "--powers", "read,edit_fields")
    ses = s["session"]["id"]

    def one(n):
        return pli("prep-bot", "item", "update", i, "--set", f"f_summary=s{n}", "--set", "f_policy=ok", "--set", "f_flags=none", session=ses, check=False)[0]
    with concurrent.futures.ThreadPoolExecutor(8) as ex:
        accepted = sum(ex.map(one, range(FLOOD)))
    ok, out = pli("bob", "item", "transition", i, "--to", "approved", check=False)
    rec.add("kernel", "purchase", f"block the decision: the agent rewrites its preparation {FLOOD:,} times, then the decider approves",
            not ok, f"{accepted} rewrites accepted; the manager's approval then: {'state=' + out['item']['state'] if ok else out}")

    i = new(vendor="ghost-vendor")
    pli("bob", "item", "transition", i, "--to", "approved")
    events = []
    for _ in range(100):
        _, h = pli("ana", "item", "history", i, "--page-size", "50")
        events = [e.get("kind") or e.get("type") or "" for e in h.get("events", [])]
        if any("FAIL" in str(e).upper() for e in events):
            break
        time.sleep(2)
    _, it = pli("ana", "item", "get", i)
    traced = any("FAIL" in str(e).upper() for e in events)
    rec.add("kernel", "purchase", "an approved purchase whose order cannot be placed leaves a trace", not traced,
            f"state={it['item']['state']} order={it['item']['fields'].get('f_order_id')} history={[e for e in events if e][-4:]}",
            verdict="PASS (recorded)" if traced else "FAIL (silent)")

    i = new(2500)
    ok, _ = pli("olaf", "item", "get", i, check=False)
    rec.add("kernel", "purchase", "a colleague with no part in it reads ana's purchase", ok, "every member of staff can read every request: these Types narrow nothing")
    ok, _ = pli("olaf", "item", "history", i, check=False)
    rec.add("kernel", "purchase", "... and its history", ok, "")
    ok, _ = pli("prep-bot", "item", "get", i, session=ses, check=False)
    rec.add("kernel", "purchase", "the agent reads it (it must, to prepare)", ok, "", verdict="ALLOWED (its job)")
    args = ["item", "create", "--type", "purchase@1", "--set", "f_amount=700", "--set", "f_vendor=officeco", "--set", "f_cost_centre=CC-100",
            "--set", "f_justification=sent twice", "--request-id", f"twice-{int(time.time())}"]
    a, b = pli("ana", *args)[1]["item"]["name"], pli("ana", *args)[1]["item"]["name"]
    rec.add("kernel", "purchase", "the same request sent twice makes one purchase", a != b, f"two purchases: {a != b} (the same request ID)",
            verdict="FAIL (two made)" if a != b else "PASS (one)")
    i = new(2500)
    pli("bob", "item", "transition", i, "--to", "finance_queue")
    pli("fay", "item", "transition", i, "--to", "finance_review", "--set", "f_claimed_by=user:fay")
    ok, out = pli("gus", "item", "transition", i, "--to", "finance_queue", check=False)
    rec.add("kernel", "purchase", "a colleague in finance can take over a purchase fay claimed and left", not ok,
            "gus put it back in the queue" if ok else out, verdict="PASS (can)" if ok else "FAIL (stuck with the claimer)")


if __name__ == "__main__":
    which, outfile, title = sys.argv[1], sys.argv[2], sys.argv[3]
    rec = Recorder()
    if which in ("plain", "all"):
        plain_side(rec)
    if which in ("kernel", "all"):
        kernel_side(rec)
    rec.write(os.path.join(SPIKE, "results", outfile), title)
