# Tools run through `go run` at pinned versions, so nothing needs installing.
BUF := go run github.com/bufbuild/buf/cmd/buf@v1.75.0

.PHONY: up down reset migrate scenario gen lint breaking test vet bench

up: ## build and start the whole stack, and wait until it is healthy
	docker compose up -d --build --wait

down: ## stop the stack (keeps nothing worth keeping: Temporal's state is in memory)
	docker compose down

reset: ## stop the stack and delete its data
	docker compose down -v

migrate: ## apply migrations with the stack's one-shot migration service
	docker compose run --rm migrate

scenario: up ## run the Step 0 scenario against the stack (needs Go and jq)
	./scripts/scenario.sh

gen: ## regenerate Protobuf code into gen/ (committed; CI checks it is current)
	$(BUF) generate

lint: ## lint the proto
	$(BUF) lint

# What breaking compares against: origin's main (git fetch origin main
# first; a local main can be stale); CI passes the pull request's base.
BREAKING_AGAINST ?= .git\#ref=refs/remotes/origin/main

breaking: ## check the proto against main (WIRE_JSON: wire numbers, JSON names, enum names)
	$(BUF) breaking --against '$(BREAKING_AGAINST)'

vet:
	go vet ./...

test: ## run all tests (needs Docker for testcontainers)
	go test ./...

bench: ## measure the spike (needs Docker; takes a few minutes)
	go run ./cmd/bench
