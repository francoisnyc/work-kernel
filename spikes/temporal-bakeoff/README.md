# Bake-off 2: the kernel, or each process written on Temporal

Throwaway spike for `docs/plans/temporal-bakeoff.md`. The result is in
`docs/research/temporal-bakeoff.md`; this folder lets anyone repeat it.

It reuses the first bake-off (`spikes/camunda-bakeoff/`) by path: its kernel
side (Types, dev tokens, scripts), its stubs and seed data, its stand-in
model, and its compromised-agent script. Nothing there is copied or changed.

Local only. The stubs listen on every interface, as in the first bake-off:
run this where nobody else can reach the machine.

## The plain-Temporal side

The same purchase approval and access request, written directly on Temporal
in Go, as a team would: no kernel code is used.

| Path | What | Lines |
|---|---|---|
| `auth/` | Sign-in stand-in: the same dev tokens as the kernel side | 72 |
| `store/` | Postgres: the table the inbox reads (written by the workflows) and the audit trail | 160 |
| `acts/` | The activity library: HR lookup, order, notice, the write to Postgres | 107 |
| `httpapi/` | What every route shares; the inbox, reading a request and its history, the agent's to-do list | 172 |
| `purchase/` | **Step 1.** The purchase workflow (updates with validators, a query), its routes, its tests; `change.go` is the bake-off's switch for the CFO change | 329 + 115 + 177 + 29 |
| `access/` | **Step 2.** The access request, written by a separate builder from the step-1 code and the business's request only | 217 + 100 + 194 |
| `cmd/ptapp/` | The one program: HTTP API and Temporal worker | 98 |
| `cmd/ptreplay/` | Replays recorded workflow histories against the code: Temporal's check before deploying a change | 88 |
| `compose.yaml` | Its own Postgres and Temporal development server (ports 25432, 27233) | |
| `kernel-blind/` | The kernel side's counterpart to step 2: the access request as Types, written by a separate builder from the purchase Type and the same request | |

The API (port 28080, bearer dev tokens `dev-<name>-not-a-secret`):
`POST /purchases`, `POST /purchases/{id}/preparation|claim|release|decision`,
`POST /access-requests`, `POST /access-requests/{id}/preparation|decision`,
`GET /inbox`, `GET /requests/{id}`, `GET /requests/{id}/history`,
`GET /agent/todo`.

## Run it

Needs Docker (with Compose), Go, Python 3, curl, psql and bc. About five
minutes:

```bash
spikes/temporal-bakeoff/scripts/run-all.sh
```

It stops anything left over, builds both sides' programs, starts the stubs,
the kernel side (the first spike's script) and the plain side, and runs:

| Measure | Script | Result file |
|---|---|---|
| 3 (part): the inbox and the claim, on fresh data | `test_inbox.py` | `results/inbox.md` |
| 4: the compromised agent, per process, both sides | `test_attacks.py` (`attacks_plain.py`, `attacks_kernel.py`) | `results/attacks-plain.md`, `results/attacks-kernel.md` |
| 4: side doors (no credentials) | `test_sidedoors.py` | `results/sidedoors.md` |
| Footprint and start time | `footprint.sh` | `results/footprint.md`, `results/start-*.txt` |
| 2: the CFO change with requests in flight | `test_change_plain.py`, `test_change_kernel.py` | `results/change-plain.md`, `results/change-kernel.md` |

Made during the build, not by `run-all.sh`:

- `results/timing.txt`: when each step started and ended.
- `results/attacks-plain-access-before-hardening.md`: the access request's
  attack list, run on the code exactly as the second builder handed it back.
- `results/notes.md`: things noticed along the way.
- `results/attacks-kernel-blind.md` and `kernel-blind/`: the kernel-side
  blind build and its attack list (`scripts/test_kernel_blind.py`, which
  restarts the kernel side on that folder).

Piece by piece:

```bash
cd spikes/temporal-bakeoff
scripts/stubs-up.sh
scripts/plain-up.sh                  # Postgres + Temporal in Docker, ptapp on the host (.run/ has logs)
../camunda-bakeoff/scripts/kernel-up.sh
python3 scripts/plain-agent.py       # the agent prepares what is waiting
curl -s -H 'Authorization: Bearer dev-fay-not-a-secret' http://127.0.0.1:28080/inbox
scripts/plain-restart.sh patched     # a deploy: restart the program only, requests stay in flight
.bin/ptreplay --purchase-code naive  # what Temporal's replay check says about a careless change
scripts/plain-down.sh; ../camunda-bakeoff/scripts/kernel-down.sh; scripts/stubs-down.sh
```

`go test ./spikes/temporal-bakeoff/...` (from the repository root) runs the
two workflows' tests in Temporal's test environment; no Docker needed.
