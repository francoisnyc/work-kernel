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
- **Node 24**, only to work on the web UI (the stack builds it in Docker).
- **curl**, for the scenario script (it reads the emails the stack sent).

## Start the stack

```sh
docker compose up -d --build --wait     # or: make up
```

This builds the images and starts Postgres, the Temporal dev server, a one-shot
migration, the item service (the API), the relay, the worker, stub CRM,
ERP, HR directory and email servers (`connstub`) for the ticket's and the
vacation request's hooks, and the web UI. `--wait` returns once every
service is healthy. Four ports are published:

- `http://localhost:8080`: the public API (Connect, gRPC and gRPC-Web),
  **on 127.0.0.1 only**.
- `http://localhost:8090`: [the web UI](#the-web-ui), **on 127.0.0.1 only**.
- `http://localhost:8233`: the Temporal UI.
- `http://localhost:8085`: dev only, the emails the stack sent, read-only
  (`GET /messages?item=<id>`), for the scenario. The email stub itself
  (8084) is reachable inside the stack only.

The stack is for development. Its worker runs `vacation_request` on a **dev
clock**: a day of that Type's declared timers (reminders, expiry) lasts 2 s,
and the worker warns so on every start. Every other Type, hook budgets and
other timeouts keep real time. A production worker never gets these flags.

## How it fits together

- **migrate** applies the migrations, then exits; the services below start
  only once it has succeeded.
- **itemservice** is the only program that reads and writes Items, and serves
  the public API on 8080. Its health check asks `/healthz`, which answers 200
  while the database answers (it is outside the API contract).
- **relay** delivers the outbox (what changed) to Temporal; **worker** runs the
  Item workflows and their hooks, which call **connstub**'s CRM, ERP, HR
  directory (`deploy/hr-directory.json`) and email service, and
  write back through the item service. The relay and the worker have no health
  check: they serve no port and nothing waits on them; they start once
  Postgres, Temporal (and, for the worker, connstub and the item service,
  whose loaded Types its dev clock checks) are healthy.
- **web** serves the web UI's built site with nginx and proxies `/api` to
  the item service, so the browser talks to one origin. It is a client of
  the public API like `pli`: nothing else.
- **postgres** holds everything; **temporal** is the dev server, in memory.

## Build pli

```sh
go build -o bin/pli ./cmd/pli
export PATH="$PWD/bin:$PATH"
```

## Dev tokens (read this)

There is no real sign-on yet. [`dev-tokens.example.yaml`](dev-tokens.example.yaml)
maps fake bearer tokens to principals: `ana` and `bob` (who holds the
`approver` role), `lea` and `sam` (who work in the PLAT project below) and the
agent `triage-bot` in namespace `acme`, among others, and `carol` in
namespace `globex`. It also says what ana lets `triage-bot` do. The stack
mounts the file read-only into the item service, which refuses to start
without it and prints a **WARNING on every start**: anyone holding the file
can act as all of them. Never expose a server that runs with it. For that
reason the stack publishes the API and the web UI on `127.0.0.1` only:
Compose otherwise publishes a port on every network interface, which would
let anyone on your network call the API with a dev token.

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

### A project: order, rank, progress

The PLAT Types (`space`, `story`, `bug`, `task`, `epic`, `sprint`, `comment`,
`subscription`) make a small Jira-like project. The kernel sorts by any
declared field; ranking by hand is the app's business: `pli rank` writes an
ordinary string field, `f_rank`.

```sh
PLAT=$(pli --as lea item create --type space@1 --set f_key=PLAT --set f_name=Platform | jq -r .item.name)
EPIC=$(pli --as lea item create --type epic@1 --in "$PLAT" --set f_summary="Single sign-on" | jq -r .item.name)
SPRINT=$(pli --as lea item create --type sprint@1 --in "$PLAT" --set f_name="Sprint 12" \
           --set f_end=2026-12-04 | jq -r .item.name)
A=$(pli --as lea item create --type story@1 --in "$PLAT" --set f_summary="Log in with SSO" \
      --set f_priority=P2 --set f_assignee=user:sam | jq -r .item.name)
B=$(pli --as lea item create --type bug@1 --in "$PLAT" --set f_summary="Button misaligned" \
      --set f_priority=P1 --set f_assignee=user:sam | jq -r .item.name)
for I in "$A" "$B"; do
  pli --as lea item link "$I" epic "$EPIC"; pli --as lea item link "$I" sprint "$SPRINT"
  pli --as lea rank "$I" bottom            # one write: an update of f_rank
done

# The board: what is linked to the sprint, in rank order. Then move a card.
pli --as lea item search --linked "out:sprint=$SPRINT" --order-by f_rank
pli --as lea rank "$B" before "$A"

# sam's open work across the three Types, most urgent first.
pli --as sam item search --type story --type bug --type task --where f_assignee=me \
    --category todo --category active --order-by f_priority

pli --as lea item get "$EPIC"                             # rollups.progress: what lea can see
pli --as sam item transition "$A" --to in_progress
pli --as sam item transition "$A" --to done               # FAILED_PRECONDITION MISSING_FIELDS (f_resolution)
pli --as sam item transition "$A" --to done --set f_resolution=fixed
pli --as sam item transition "$A" --to todo               # reopening clears the resolution
```

`--order-by 'FIELD [asc|desc]'` works on `item search` and `item list`, on
string, int, date and enum fields; Items without a value come last. Strings
sort byte by byte.

A move is always one write. Rank keys grow as cards are squeezed into the
same gap; past 32 bytes `pli rank` says the container needs a rebalance and
still succeeds. `pli rank rebalance --in "$PLAT"` rewrites the container's
keys, short and even. **Run it when nobody is dragging cards there:** a card
moved while it runs can land out of place (measured: two in three).

`pli` keeps agent sessions under `$XDG_CONFIG_HOME/pli/sessions` when
`XDG_CONFIG_HOME` is set (on every OS), else in the OS's config directory.
`pli` with no arguments prints its usage.

## The web UI

```sh
docker compose up -d --build --wait     # then open http://localhost:8090
```

One generic UI for playing with the proof of concept. No screen knows a
Type: every form, button and list is built from what `GetType` and
`ListTypes` say, so the ticket, the vacation request and the PLAT project
all work with the same pages. It calls the public API only.

- **Act as.** Choose a dev user in the yellow banner (`ana`, `bob`, `lea`,
  `carol` in the other namespace...). The choice is kept per browser tab, so
  two tabs can be two users.
- **Items** lists the top level; **Search** takes words, Types, states,
  categories, field conditions (`f_assignee eq me`) and an order. Both page
  with "Load more".
- **New Item**: choose a Type (the latest version unless you pick another),
  optionally a container and readers, and fill the form built from its
  fields.
- **An Item's page** shows its fields as a form (fields its state freezes
  and fields only the kernel writes are read-only), the transitions its
  Type declares from its state, its links both ways (add and remove where
  the link is not the kernel's to set), rollups, the Items it contains, and
  its history.
- **Transitions.** Every move the Type declares is offered, with its rules
  (`allowed`, `except`, `guard`, `required`, `clears`) shown as text. The UI
  evaluates none of them: it asks, and shows the server's answer, either an
  outcome (`APPLIED`, `DENIED`, `PENDING`, `ERROR`) or a refusal with its
  code, reason and metadata. A move only the system can make (a timer's) is
  greyed out.
- **Inbox**: the Items waiting for you, found from the Types: for every
  transition whose `allowed` names a principal field, the Items in its
  from-state where that field is you (today: vacation requests to approve).
- **Board**: from an Item's page, "Board: sprint_issues" shows the Items
  linked to it, ordered by `f_rank`. Drag a card onto the top or bottom
  half of another, or use its arrows. A drop is one write, ranked against
  the card's container as `pli rank` does (`web/src/rank` is a port of
  `internal/rank`, held to it by shared test vectors). Past 32 bytes the
  board says the container needs a rebalance; rebalancing stays with
  `pli rank rebalance`.
- Nothing updates by itself (the API has no Watch yet): pages reload after
  an action, and each has a Refresh button.

### What is dev-only

- **The "act as" picker and the dev-token file.** The stack mounts
  `dev-tokens.example.yaml` read-only into the web container, which serves
  it at `/dev-tokens.yaml` so the picker can list the users. **Every dev
  token is therefore readable by anyone who can reach port 8090**, which is
  why the stack publishes 8090 (and the API's 8080) on `127.0.0.1` only. Do
  not change those bindings, and never put this stack on a network.
- The picker is compiled in only when the site is built with
  `VITE_DEV_USERS=1`, which only `docker-compose.yml` sets. A build without
  it contains no picker and no dev-user code, the image never contains the
  token file, and such a build says it has no sign-on: there is no other
  sign-on yet.
- Agents are not in the picker: an agent needs a session, which the UI does
  not start. Use `pli` for those.

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
unable to approve) that deducts from a leave balance HR owns (refunded on
withdrawal, never overdrawn, even by two approvals at once), and the PLAT
software project: a sprint's board ordered by rank, rank moves, an epic's
progress, a resolution on done, 47 Items paged 20 at a time, watching. It drives only `pli`, keeps its saved sessions in a temporary
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
- The web UI is in `web/` (TypeScript, Vite, React; Node 24). In `web/`:
  `npm ci`, then `npm test` (unit tests), `npm run typecheck`, and
  `npm run dev` (a dev server on 5173 against the running stack's API).
  `npm run e2e` runs the browser smoke test against the running stack
  (once: `npx playwright install chromium`). `make gen-web` regenerates the
  TypeScript client in `web/src/gen/` (committed; CI checks it is current;
  it needs `npm ci` first).
- `internal/rank/testdata/vectors.json` holds the rank package's answers,
  which the TypeScript port must match. After a change to `internal/rank`:
  `go test ./internal/rank -run TestVectors -update`, then make
  `web/src/rank` follow.
- To run a service from the host against the stack, publish Postgres (5432)
  and Temporal's gRPC port (7233) too:
  `docker compose -f docker-compose.yml -f compose.dev.yaml up -d --build --wait`.
- `CLAUDE.md` says how this repository is worked on; `docs/step-prompts.md`
  holds the build steps.
