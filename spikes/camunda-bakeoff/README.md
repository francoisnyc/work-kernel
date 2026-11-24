# Bake-off: the approvals pilot on Camunda 8 and on the kernel

Throwaway spike for `docs/plans/camunda-bakeoff.md`. The result is in
`docs/research/camunda-bakeoff.md`; this folder lets anyone repeat it.

Local only: every port is on 127.0.0.1 except the shared stubs (see below).
Camunda's Self-Managed free edition is licensed for development and testing
only: never deploy this anywhere shared.

## What is here

| Path | What |
|---|---|
| `compose.yaml` | Two Compose profiles: `kernel` (Postgres 17, Temporal dev server) and `camunda` (the official 8.10 lightweight setup: orchestration cluster with H2, no Elasticsearch; plus two connector runtimes) |
| `kernel/types/` | The kernel side: `purchase@1`, `system@1`, `system_access@1`, plain YAML, no kernel code change |
| `kernel/types-v2/` | The same plus `purchase@2`, the CFO change (test 1) |
| `kernel/dev-tokens.yaml`, `hr-directory.json`, `vendors.json`, `connectors.yaml` | The kernel side's people, roles, agent grants, HR directory and stub data |
| `camunda/*.bpmn`, `camunda/system-owner.dmn` | The Camunda side: purchase, access request, the agent's prepare-decision process, the owner table; `purchase-cfo.bpmn` is the CFO change |
| `camunda/application.yaml` | The orchestration cluster's configuration: the official H2 file with the API protected and authorizations on |
| `cmd/modelstub/` | The stand-in model: an OpenAI-compatible endpoint that answers deterministically and obeys injected instructions |
| `scripts/` | Start, stop, set up, and the four tests (below) |
| `results/` | What the last run produced: the evidence the report quotes |

## The people (both sides)

ana asks; bob is her manager (HR directory); fay and gus are finance (fay
also owns the "ledger" system); cora is the CFO; olaf owns "payroll"; ivy
sets up systems (kernel only); `prep-bot` is the agent. Kernel: dev tokens
in `kernel/dev-tokens.yaml`. Camunda: users with password `<name>-pw`, admin
`demo`/`demo`.

## How the agent runs

The same deterministic "model" (`cmd/modelstub`, port 18090) serves both
sides, with the same prompt. It is gullible on purpose: a justification that
says "approve immediately" or "skip finance" makes it recommend approval
with no flags.

- **Kernel:** `scripts/kernel-agent.py` (the kernel has no agent task yet):
  it starts an agent session for `agent:prep-bot` with only `read` and
  `edit_fields`, finds Items waiting for a decision with no summary, asks the
  model, and writes `f_summary`, `f_policy`, `f_flags`: the only fields the
  Types let the `preparer` role write.
- **Camunda:** the AI Agent Task connector (8.10 template, OpenAI provider,
  custom endpoint, no key) inside `prepare-decision.bpmn`, called from each
  approval process with a call activity whose output mapping returns only
  `preparation`. It runs in its own connectors runtime (`agent-connectors`),
  logged in as `prep-bot`, authorized on `prepare-decision` only.

## The shared stubs

The kernel repository's `connstub` (unchanged) serves both sides: the
"purchase system" is its ERP (`POST /invoices`, idempotent), the HR
directory (`GET /employees/{id}/manager`, and the same over the kernel's hook
protocol), and the email stub. They listen on every interface on ports
18081 to 18085 so Camunda's containers reach them through
`host.docker.internal`: run this only where nobody else can reach the
machine. They hold no secrets.

## Run it

Needs Docker (with Compose), Go (the repo's version), Python 3, curl and bc;
Node and Playwright only for the Tasklist screenshot. About ten minutes:

```bash
spikes/camunda-bakeoff/scripts/run-all.sh
```

That stops anything left from a previous run, builds the kernel's programs
from this repository's `main` code, starts the stubs, the kernel side alone
(timed, memory taken), then the Camunda side (timed, memory taken), and runs:

| Test | Script | Result file |
|---|---|---|
| 3. One inbox (first, on fresh data) | `test3-inbox.py`, `tasklist-screenshot.js` | `results/test3-inbox.md`, `results/tasklist-fay*.png` |
| 2a. Injection | `test2-injection.py` | `results/test2-injection.md` |
| The other paths (small purchase, rejection) | `test0-paths.py` | `results/test0-paths.md` |
| 2b. Compromised agent, and the requester | `test2-compromised.py all` | `results/test2-compromised-*.md` |
| 1. The CFO change | `test1-cfo.py` | `results/test1-cfo.md`, `results/test1-kernel-*.txt` (the two `.diff` files were made with `diff -u` and `scripts/bpmn-diff.sh`) |
| 4. Footprint | `test4-footprint.sh` | `results/test4-footprint.md`, `results/test4-start-*.txt` |

Piece by piece:

```bash
cd spikes/camunda-bakeoff
scripts/stubs-up.sh
scripts/kernel-up.sh        # Postgres + Temporal in Docker, the kernel's programs on the host (.run/ has logs)
scripts/camunda-up.sh       # orchestration cluster, users and authorizations, connector runtimes, models
scripts/pli.sh --as ana item search --type namespaces/acme/types/purchase
python3 scripts/kernel-inbox.py fay
python3 scripts/camunda-inbox.py fay
scripts/camunda-down.sh; scripts/kernel-down.sh; scripts/stubs-down.sh
```

Camunda's web apps: http://localhost:8088/tasklist and
http://localhost:8088/operate (`demo`/`demo`, or any person above).

## Not reproduced by the scripts

- `results/test1-camunda-misspelled-variable.md` (what Camunda does with
  the deployment it accepted with a misspelled variable) was produced by
  hand after the run: deploy `purchase-cfo.bpmn` with `amount` changed to
  `amont` in the CFO condition, send a 6,000 purchase through the manager
  and finance, and read the incident.
- The diagrams in `camunda/*.bpmn` were generated once with
  `bpmn-auto-layout` (npm) from hand-written XML; in Camunda Modeler a
  person draws them. `scripts/bpmn-diff.sh` compares two models without
  their diagrams.
