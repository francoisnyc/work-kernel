"""The kernel side's one inbox: everything waiting on a person or their team,
across every Type, with the agent's summary. Built here, in the spike,
because the kernel has none (see the report, test 3).

How: for every Type version (ListTypes, GetType), every move out of a
waiting or active state that a person could take is read from `allowed`:
  field:<f>  -> Items in that state whose field f holds me (a search filter)
  role:<r>   -> Items in that state, if I hold role r (from the dev-token
                file: the API cannot say which roles the caller holds)
  creator / empty / nobody -> not an approval, left out.
Moves whose guard fails, or whose `except` excludes me, still show: the
kernel judges those only when the move is asked for.

Usage: python3 scripts/kernel-inbox.py fay
"""
import os
import re
import sys

from bakeoff import SPIKE, pli, short

me = sys.argv[1] if len(sys.argv) > 1 else "fay"
# The caller's roles: from the dev-token file, as the PoC apps do (no "who am I" in the API).
roles = set()
for line in open(os.path.join(SPIKE, "kernel/dev-tokens.yaml")):
    m = re.match(r"\s*- \{name: (\S+),.*roles: \[([^\]]*)\]", line)
    if m and m.group(1) == me:
        roles = {r.strip() for r in m.group(2).split(",")}

_, types = pli(me, "type", "list")
rows = {}
for t in types["types"]:
    _, d = pli(me, "type", "get", f"{t['type_id']}@{t['version']}")
    d = d["type"]
    cat = {s["id"]: s.get("category", "") for s in d["states"]}
    for tr in d.get("transitions", []):
        src = tr["from_state"]
        if not any(c in cat.get(src, "") for c in ("WAITING", "ACTIVE", "waiting", "active")):
            continue
        for who in tr.get("allowed", []):
            filters = ["--type", f"namespaces/acme/types/{t['type_id']}", "--state", src]
            if who.startswith("field:"):
                filters += ["--where", f"{who[6:]}=me"]
                why = f"you ({who[6:]})"
            elif who.startswith("role:") and who[5:] in roles:
                why = f"your team ({who[5:]})"
            else:
                continue
            _, res = pli(me, "item", "search", *filters, "--page-size", "500")
            for it in res.get("items", []):
                if not it["type"].endswith(f"/versions/{t['version']}"):
                    continue
                key = short(it["name"])
                moves = rows.setdefault(key, (it, why, set()))[2]
                moves.add(tr["to_state"])

print(f"Inbox for {me} (roles: {', '.join(sorted(roles)) or 'none'}): {len(rows)} waiting")
for key, (it, why, moves) in sorted(rows.items(), key=lambda r: r[1][0].get("create_time", "")):
    f = it["fields"]
    kind = it["type"].split("/types/")[1].split("/")[0]
    what = f"{f.get('f_amount')} to {f.get('f_vendor')}" if kind == "purchase" else f"{f.get('f_role')} on {f.get('f_system_name')}"
    claimed = f", claimed by {f['f_claimed_by']}" if f.get("f_claimed_by") else ""
    print(f"- [{kind}] {what} — {it['state']}{claimed} — for {why}; moves: {', '.join(sorted(moves))}")
    print(f"    agent: {f.get('f_summary', '(not prepared yet)')} [policy {f.get('f_policy', '-')}, flags {f.get('f_flags', '-')}]")
