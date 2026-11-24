#!/usr/bin/env bash
# Diff two BPMN files without their diagram (the bpmndi section): what changed in the process.
#   scripts/bpmn-diff.sh camunda/purchase.bpmn camunda/purchase-cfo.bpmn
strip() { sed '/<bpmndi:BPMNDiagram/,/<\/bpmndi:BPMNDiagram>/d' "$1"; }
diff -u --label "$1" --label "$2" <(strip "$1") <(strip "$2")
