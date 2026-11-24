#!/usr/bin/env bash
# Deploy BPMN/DMN files to the Camunda side (as the admin "demo"):
#   scripts/camunda-deploy.sh camunda/purchase.bpmn ...   (default: the v1 set)
set -euo pipefail
cd "$(dirname "$0")/.."
C=${CAMUNDA:-http://127.0.0.1:8088}
files=("$@")
[ ${#files[@]} -gt 0 ] || files=(camunda/prepare-decision.bpmn camunda/system-owner.dmn camunda/purchase.bpmn camunda/access-request.bpmn)
args=()
for f in "${files[@]}"; do args+=(-F "resources=@$f"); done
curl -s -u demo:demo -X POST "$C/v2/deployments" "${args[@]}" | python3 -c '
import json,sys
d=json.load(sys.stdin)
if "deployments" not in d: print(json.dumps(d, indent=1)); sys.exit(1)
for x in d["deployments"]:
    p=x.get("processDefinition") or x.get("decisionDefinition") or x.get("decisionRequirements") or {}
    print("deployed", p.get("processDefinitionId") or p.get("decisionDefinitionId") or p.get("decisionRequirementsId"), "version", p.get("processDefinitionVersion") or p.get("version"))'
