# Tools run through `go run` at pinned versions, so nothing needs installing.
GOOSE := go run github.com/pressly/goose/v3/cmd/goose@v3.30.0
BUF   := go run github.com/bufbuild/buf/cmd/buf@v1.75.0

DATABASE_URL ?= postgres://kernel:kernel@localhost:5432/kernel?sslmode=disable

.PHONY: up down migrate gen lint test vet bench

up: ## start Postgres and the Temporal dev server
	docker compose up -d --wait

down: ## stop everything
	docker compose down

migrate: ## apply migrations
	$(GOOSE) -dir migrations postgres "$(DATABASE_URL)" up

gen: ## regenerate Protobuf code into gen/ (committed; CI checks it is current)
	$(BUF) generate

lint: ## lint the proto
	$(BUF) lint

vet:
	go vet ./...

test: ## run all tests (needs Docker for testcontainers)
	go test ./...

bench: ## measure the spike (needs Docker; takes a few minutes)
	go run ./cmd/bench
