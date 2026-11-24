"""The plain-Temporal side's agent: prepares each decision, never decides.

An outside program with its own API token (agent:prep-bot), like the kernel
side's: it reads its to-do list, asks the model (the same prompt, the same
stub), and posts the preparation. Usage: python3 scripts/plain-agent.py
"""
from pt import ask_model, plain

_, todo = plain("prep-bot", "GET", "/agent/todo")
for row in todo.get("requests", []):
    d = row["data"]
    if row["kind"] == "purchase":
        req = {"kind": "purchase", "amount": d["amount"], "vendor": d["vendor"], "cost_centre": d["cost_centre"], "justification": d["justification"]}
        path = f"/purchases/{row['id']}/preparation"
    else:
        req = {"kind": "access", "system": d.get("system"), "role": d.get("role"), "justification": d.get("justification")}
        path = f"/access-requests/{row['id']}/preparation"
    prep = ask_model(req)
    st, out = plain("prep-bot", "POST", path, {"summary": prep["summary"], "policy": prep["policy"], "flags": prep["flags"]})
    print(f"{row['kind']} {row['id']}: {'prepared' if st == 204 else f'refused: HTTP {st} {out}'} (model recommended {prep['recommendation']}, not stored)")
