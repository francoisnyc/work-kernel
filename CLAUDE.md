# CLAUDE.md — Work kernel

Read this file and `docs/design.md` at the start of every session.
`docs/design.md` holds the settled design. Don't change it without asking me.

## What this repo is

A two-week **spike** of the Work kernel: one throwaway Type running end to end
(Go + Postgres + Temporal). The goal is to learn whether the design holds and
whether Go suits me — not to ship production code.

## About me

- I know Java and Python well; I'm learning Go.
- When you write Go, briefly explain idioms that differ from Java
  (errors as values, interfaces, contexts, goroutines, package layout).
  One or two sentences, not tutorials.
- Prefer simple, readable code over clever code. No frameworks beyond the stack below.

## Stack

- Go (latest stable), standard library first
- Postgres 17+, `pgx` v5, `goose` migrations (`sqlc` optional)
- `buf` + `connect-go` for the API contract in `proto/kernel/v1/`
- Temporal Go SDK; Temporal dev server via `temporal server start-dev`
- Tests: Go `testing` + `testcontainers-go` for Postgres; Temporal's `testsuite` for workflows

## How we work

1. **Questions first.** At the start of a task, read the docs and the task, then
   list your questions and any gaps or contradictions you see. Wait for my answers
   before writing code.
2. **Tests first.** Write the failing tests for the task, show them to me, and
   wait for my OK. The tests are the spec.
3. **Tests are protected.** Never delete, skip, weaken or rewrite a test to make it
   pass. If you think a test is wrong, stop and tell me why.
4. **Small commits.** One logical change per commit, with a clear message. Run
   `go vet ./...` and `go test ./...` before each commit.
5. **Stay in scope.** Do only the current step's task (see the build order in
   `docs/design.md` and the prompts in `docs/step-prompts.md`). Note other ideas in `docs/journal.md`
   under "Later" instead of building them.
6. **Ask before** adding a dependency, changing the schema in a way that differs
   from `docs/design.md`, or changing the API contract.
7. **End of session.** Append a short entry to `docs/journal.md`: what was built,
   what surprised you, open questions, and where the next session should start.

## Kernel rules that code must never break

Words: the **kernel** is the whole core layer (item service, lifecycle, relay,
worker); the **item service** is the one program that reads and writes Items.


- Every write goes through the item service's operations (create, update,
  transition, link, migrate, archive). Nothing writes Item state from the side.
- A write is one Postgres transaction: Item row + event + outbox row. The Item
  row is locked during the write, so `item_seq` has no gaps.
- Every write takes an idempotency key and returns `commit_seq`.
- Lifecycle logic lives in one pure function in `internal/lifecycle/`, used by
  both the item service and the Temporal workflow. No lifecycle logic anywhere else.
- No personal data (names, emails, free text) in `item_events.payload` —
  opaque IDs only.
- Every outbound path (API responses, search results, webhooks, agent context,
  workflow payloads) goes through `policy.Project`. No exceptions.
- Workflow code must be deterministic: no `time.Now()`, random numbers, goroutines,
  maps iterated for ordering, or I/O inside workflows. Use the Temporal SDK
  equivalents.

## Commands

- `make up` — start Postgres and the Temporal dev server
- `make migrate` — apply migrations
- `make gen` — regenerate Protobuf code
- `make test` — run all tests
- `make down` — stop everything
