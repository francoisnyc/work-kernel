#!/usr/bin/env bash
# Users, groups and authorizations for the Camunda side, through the
# Orchestration Cluster REST API (v2), as the admin user "demo".
# Least privilege throughout (the steelman):
#   - people: the default task-worker role (read, claim, complete a task
#     they are assignee, candidate user or in a candidate group of), the
#     Tasklist component, and starting the two approval processes;
#   - "connectors" (the runtime for REST calls): what Camunda's default
#     connectors role gives, but only on the two approval processes;
#   - "prep-bot" (the agent's runtime): only on prepare-decision.
set -euo pipefail
C=${CAMUNDA:-http://127.0.0.1:8088}
api() { # method path [json]; 2xx and 409 (already there) are fine
  local body code
  body=$(curl -s -w '\n%{http_code}' -u demo:demo -X "$1" "$C$2" -H 'Content-Type: application/json' ${3:+-d "$3"})
  code=${body##*$'\n'}
  case $code in 2??|409) ;; *) echo "FAILED $1 $2 -> $code ${body%$'\n'*}" >&2; exit 1;; esac
}
user() { api POST /v2/users "{\"username\":\"$1\",\"password\":\"$1-pw\",\"name\":\"$1\",\"email\":\"$1@example.com\"}"; }
auth() { # owner ownerType resourceType resourceId perms(json array)
  api POST /v2/authorizations "{\"ownerId\":\"$1\",\"ownerType\":\"$2\",\"resourceType\":\"$3\",\"resourceId\":\"$4\",\"permissionTypes\":$5}"
}

PEOPLE="ana bob fay gus cora olaf ivy"
for u in $PEOPLE connectors prep-bot; do user "$u"; done
api POST /v2/groups '{"groupId":"finance","name":"Finance team"}'
api POST /v2/groups '{"groupId":"cfo","name":"CFO"}'
for u in fay gus; do api PUT /v2/groups/finance/users/$u; done
api PUT /v2/groups/cfo/users/cora

for u in $PEOPLE; do
  api PUT /v2/roles/task-worker/users/$u
  auth "$u" USER COMPONENT tasklist '["ACCESS"]'
  # Requests start by message (see the processes' start events): people may
  # publish the two start messages, and may not create process instances.
  auth "$u" USER MESSAGE purchase-submitted '["CREATE"]'
  auth "$u" USER MESSAGE access-requested '["CREATE"]'
  auth "$u" USER PROCESS_DEFINITION purchase '["READ_PROCESS_DEFINITION"]'
  auth "$u" USER PROCESS_DEFINITION access-request '["READ_PROCESS_DEFINITION"]' 
done

for p in purchase access-request; do
  auth connectors USER PROCESS_DEFINITION "$p" '["READ_PROCESS_DEFINITION","READ_PROCESS_INSTANCE","UPDATE_PROCESS_INSTANCE"]'
done
# The processes' first step asks Camunda's audit log who really created the
# instance; it logs in as this user, who may read the audit log and nothing else.
user audit-reader
auth audit-reader USER AUDIT_LOG '*' '["READ"]'
auth prep-bot USER PROCESS_DEFINITION prepare-decision '["READ_PROCESS_DEFINITION","READ_PROCESS_INSTANCE","UPDATE_PROCESS_INSTANCE"]'
echo "camunda users, groups and authorizations set"

# Tighten the default task-worker role (steelman): out of the box, everyone
# in a candidate group may COMPLETE a task another member has claimed.
# Here candidates may only read and claim; only the assignee completes.
for prop in candidateUsers candidateGroups; do
  key=$(curl -s -u demo:demo -X POST "$C/v2/authorizations/search" -H 'Content-Type: application/json' \
    -d "{\"filter\":{\"ownerId\":\"task-worker\",\"resourceType\":\"USER_TASK\"}}" |
    python3 -c "import json,sys; print(next(a['authorizationKey'] for a in json.load(sys.stdin)['items'] if a.get('resourcePropertyName')=='$prop'))")
  api PUT "/v2/authorizations/$key" "{\"ownerId\":\"task-worker\",\"ownerType\":\"ROLE\",\"resourceType\":\"USER_TASK\",\"resourcePropertyName\":\"$prop\",\"permissionTypes\":[\"READ\",\"CLAIM\"]}"
done
echo "task-worker: candidates read and claim; only the assignee completes"
