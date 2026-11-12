# Library research (Step 1)

Checked 2026-11-12. Versions come from the Go module proxy (`proxy.golang.org/<module>/@latest`).
"Needs Go" is the `go` line in each library's own `go.mod`.

## Go toolchain

- Latest stable: **Go 1.27.4**. Most libraries below need **Go ≥ 1.26**.
- With `GOTOOLCHAIN=auto` (the default), a `go 1.26`+ line in our `go.mod` makes an
  older local `go` download the right toolchain by itself.
- https://go.dev/dl/ · https://go.dev/doc/toolchain

## Temporal Go SDK — `go.temporal.io/sdk`

- Latest: **v1.51.0** (2026-11-10). Needs Go 1.26. API module `go.temporal.io/api` v1.64.3.
- Releases: https://github.com/temporalio/sdk-go/releases
- Temporal CLI (dev server): **v1.10.0**, bundling **Server 1.32.2**, UI 2.56.1
  (read from the `temporalio/temporal:latest` image). https://github.com/temporalio/cli

### Is Update-With-Start GA?

**Yes, as far as I can verify.**

- Server side: announced GA with Temporal Server v1.28.0 (2025).
  https://community.temporal.io/t/update-with-start-is-now-in-ga-make-your-workflow-updates-even-cleaner/17897
- Go SDK: `client.UpdateWithStartWorkflow`, `client.NewWithStartWorkflowOperation` and
  `client.UpdateWithStartWorkflowOptions` carry **no "Experimental" note** in the v1.51.0
  source (other APIs in the same file still do). History: private preview in v1.29.0,
  current API shape since v1.31.0.
- Not verified: the official docs page and the community post. Both hosts
  (`docs.temporal.io`, `community.temporal.io`) are blocked in the session I checked from;
  the GA statement above comes from the search-result title only.
  https://docs.temporal.io/develop/go/message-passing

What the design needs from it (Step 3), all present in v1.51.0:

- `WorkflowIDConflictPolicy` **must** be set on the start operation; `USE_EXISTING`
  sends the update to the running workflow.
- `UpdateWorkflowOptions.UpdateID` gives us the `item:seq` dedup key.
- The call blocks until the update reaches `WaitForStage`, so **a worker must be
  running** for the relay to make progress; "accepted" is the stage the design asks for.
- A `WithStartWorkflowOperation` value is single-use: build a new one per call.

## connect-go — `connectrpc.com/connect`

- Latest: **v1.21.1** (2026-10-14). Needs Go 1.26.
- Code generation needs `buf` (**v1.75.0**), `protoc-gen-go` (`google.golang.org/protobuf`
  v1.36.12) and `protoc-gen-connect-go` (ships in the connect module).
- https://github.com/connectrpc/connect-go/releases · https://connectrpc.com/docs/go/getting-started
  · https://github.com/bufbuild/buf/releases

## pgx — `github.com/jackc/pgx/v5`

- Latest: **v5.13.0** (2026-11-11 — one day old). Needs Go 1.25.
- Use `pgxpool` for the service; `pgx.BeginFunc` wraps a transaction with
  commit/rollback handled.
- https://github.com/jackc/pgx/releases · https://pkg.go.dev/github.com/jackc/pgx/v5

## goose — `github.com/pressly/goose/v3`

- Latest: **v3.30.0** (2026-11-10). Needs Go 1.26.
- Works as a CLI and as a library (`goose.NewProvider` over an `fs.FS`), so tests can
  apply the same migration files that `make migrate` uses. Needs a `database/sql`
  handle; pgx provides one via `github.com/jackc/pgx/v5/stdlib`.
- https://github.com/pressly/goose/releases · https://pressly.github.io/goose/

## Also relevant

- **testcontainers-go** v0.45.0 (2026-11-05), with `modules/postgres` at the same
  version. Needs Go 1.26. https://github.com/testcontainers/testcontainers-go/releases
- **Postgres**: `postgres:17-alpine` is currently 17.11. https://hub.docker.com/_/postgres
- **sqlc** v1.31.1 (optional; not planned for Step 1). https://github.com/sqlc-dev/sqlc/releases
