# Pli: the Work kernel, proof of concept

Pli is a small kernel for tracking work. Everything is an **Item**, of a **Type**
loaded from YAML (fields, states, transitions, guards, hooks). Every change is
one Postgres transaction: the Item, an event and an outbox row. Durable
behaviour (timers, checks against other systems) runs in Temporal. People,
agents and other programs reach Items only through one public API, which `pli`,
the command-line client, uses like any other client.

This repository is a spike: the design is in [`docs/design.md`](docs/design.md)
and what was learned is in [`docs/journal.md`](docs/journal.md).

## Prerequisites

- **Docker** with **Compose v2** (`docker compose`), to run the stack.
- **Go 1.26** or newer, to build `pli` (and to run the tests).
- **jq**, for the walkthrough below and the scenario script.

## Start the stack

```sh
docker compose up -d --build --wait     # or: make up
```

This builds the images and starts Postgres, the Temporal dev server, a one-shot
migration, the item service (the API), the relay, the worker, and stub CRM,
ERP and HR directory servers (`connstub`) for the ticket's and the vacation
request's hooks. `--wait` returns once every
service is healthy. Two ports are published:

- `http://localhost:8080`: the public API (Connect, gRPC and gRPC-Web).
- `http://localhost:8233`: the Temporal UI.

## How it fits together

- **migrate** applies the migrations, then exits; the services below start
  only once it has succeeded.
- **itemservice** is the only program that reads and writes Items, and serves
  the public API on 8080. Its health check asks `/healthz`, which answers 200
  while the database answers (it is outside the API contract).
- **relay** delivers the outbox (what changed) to Temporal; **worker** runs the
  Item workflows and their hooks, which call **connstub**'s CRM, ERP and HR
  directory (`deploy/hr-directory.json`) and
  write back through the item service. The relay and the worker have no health
  check: they serve no port and nothing waits on them; they start once
  Postgres, Temporal (and, for the worker, connstub) are healthy.
- **postgres** holds everything; **temporal** is the dev server, in memory.

## Build pli

```sh
go build -o bin/pli ./cmd/pli
export PATH="$PWD/bin:$PATH"
```

## Dev tokens (read this)

There is no real sign-on yet. [`dev-tokens.example.yaml`](dev-tokens.example.yaml)
maps fake bearer tokens to principals: `ana` and `bob` (who holds the
`approver` role) and the agent `triage-bot` in namespace `acme`, and `carol` in
namespace `globex`. It also says what ana lets `triage-bot` do. The stack
mounts the file read-only into the item service, which refuses to start
without it and prints a **WARNING on every start**: anyone holding the file
can act as all of them. Never expose a server that runs with it.

`pli` reads the same file and picks a token with `--as NAME`:

```sh
export PLI_TOKENS=dev-tokens.example.yaml
pli --as ana type list
```

## A two-minute walkthrough

Two users, a held approval, refusals, reads, another namespace, and an agent
working in one container. Every answer is JSON; errors go to stderr as
`error: CODE REASON: message`, with details below.

```sh
# ana creates a ticket; bob reads it and moves it on.
T=$(pli --as ana item create --type ticket@4 --set f_title="printer on fire" \
      --set f_customer_id=cust-good --set f_amount=100 | jq -r .item.name)
pli --as bob item get "$T"
pli --as bob item update "$T" --set f_assignee=bob
pli --as bob item transition "$T" --to open
pli --as bob item transition "$T" --to in_progress

# Approving runs a check against the stub ERP (1 s): the ticket is held
# meanwhile, and --wait waits for the answer. Outcome: APPLIED.
pli --as bob item transition "$T" --to approved --wait 5s

# Refusals come back with a code and a reason.
pli --as ana item transition "$T" --to resolved   # FAILED_PRECONDITION MISSING_FIELDS (f_resolution)
pli --as carol item get "$T"                      # NOT_FOUND: carol is in another namespace

# Search with filters, and history.
pli --as ana item search printer --state approved
pli --as bob item history "$T"

# An agent, scoped to one room. ana makes a room (ana, bob and the agent
# read it), a narrower room inside it, and a ticket in each.
ROOM=$(pli --as ana item create --type channel@1 --set f_name=project \
         --narrow user:ana,user:bob,agent:triage-bot | jq -r .item.name)
INNER=$(pli --as ana item create --type channel@1 --in "$ROOM" --set f_name=secrets \
          --narrow user:ana,agent:triage-bot | jq -r .item.name)
NOTE=$(pli --as ana item create --type ticket@4 --in "$INNER" --set f_title="secret note" | jq -r .item.name)
TASK=$(pli --as ana item create --type ticket@4 --in "$ROOM" --set f_title="room task" | jq -r .item.name)

# The agent starts a session for ana, on the room only (ana's grant covers
# all of acme). pli saves the session for --as triage-bot.
S=$(pli --as triage-bot agent start --for user:ana --scopes "$ROOM" \
      --powers read,edit_fields | jq -r .session.id)
pli --as triage-bot item get "$NOTE"                        # it reads the inner room
pli --as triage-bot item get "$T"                           # NOT_FOUND: outside its scope
pli --as triage-bot item update "$TASK" --set f_priority=1  # PERMISSION_DENIED NEEDS_APPROVAL:
                                                            # it read the inner room, and the
                                                            # room is seen by more people
pli --as ana agent approve "$S" "$TASK"                     # says who will see it, asks y/N
                                                            # (from a script: --yes)
pli --as triage-bot item update "$TASK" --set f_priority=1  # now it works
```

`pli` keeps agent sessions under `$XDG_CONFIG_HOME/pli/sessions` when
`XDG_CONFIG_HOME` is set (on every OS), else in the OS's config directory.
`pli` with no arguments prints its usage.

## The scenario

```sh
make scenario      # starts the stack if needed, then runs scripts/scenario.sh
```

[`scripts/scenario.sh`](scripts/scenario.sh) is the walkthrough made thorough,
and the proof of concept's end-to-end test (CI runs it on every pull request):
users, every refusal of the error table a client can cause (each code and
reason checked), an agent in capability mode, every operation across
namespaces, and a vacation request from draft to approved or rejected (the
approver set by the HR check, the terms frozen, no self-approval, agents
unable to approve). It drives only `pli`, keeps its saved sessions in a temporary
`XDG_CONFIG_HOME`, cleans up after itself, and stops at the first wrong
outcome, naming the step. It can run any number of times on the same stack.

## Reset

```sh
docker compose down -v    # or: make reset
```

This stops everything and deletes the data. The Temporal dev server keeps its
state in memory, so always reset or restart the stack as a whole: restarting
only Postgres or only Temporal leaves the two out of step.

## Working on the code

- `make test` runs every test. It needs Docker: the tests start their own
  Postgres with testcontainers, and the workflow tests their own Temporal dev
  server; they do not use the compose stack.
- `make gen` regenerates `gen/` from `proto/` (committed; CI checks it is
  current); `make lint` and `make breaking` run `buf lint` and `buf breaking`
  (WIRE_JSON, against `origin/main`).
- To run a service from the host against the stack, publish Postgres (5432)
  and Temporal's gRPC port (7233) too:
  `docker compose -f docker-compose.yml -f compose.dev.yaml up -d --build --wait`.
- `CLAUDE.md` says how this repository is worked on; `docs/step-prompts.md`
  holds the build steps.
