"""The Camunda side's inbox, through the same API Tasklist uses: every open
user task the person may read (authorizations decide: their own, or their
candidate group's), across every process, with the agent's preparation.

Usage: python3 scripts/camunda-inbox.py fay
"""
import json
import sys

from bakeoff import camunda

me = sys.argv[1] if len(sys.argv) > 1 else "fay"
_, res = camunda(me, "POST", "/v2/user-tasks/search", {"filter": {"state": "CREATED"}, "page": {"limit": 200}})
items = res.get("items", [])
print(f"Inbox for {me}: {len(items)} waiting")
for t in items:
    _, vs = camunda(me, "POST", f"/v2/user-tasks/{t['userTaskKey']}/variables/search", {"page": {"limit": 100}})
    v = {x["name"]: json.loads(x["value"]) for x in vs.get("items", [])}
    what = f"{v.get('amount')} to {v.get('vendor')}" if "amount" in v else f"{v.get('role')} on {v.get('system')}"
    who = f"group {t['candidateGroups']}" if t.get("candidateGroups") else f"users {t.get('candidateUsers')}"
    claimed = f", claimed by {t['assignee']}" if t.get("assignee") else ""
    p = v.get("preparation") or {}
    print(f"- [{t.get('processName') or t['processDefinitionId']}] {what} — {t['name']}{claimed} — for {who}")
    print(f"    agent: {p.get('summary', '(not prepared yet)')} [policy {p.get('policy', '-')}, flags {p.get('flags', '-')}]")
