SQLC_VERSION := v1.31.1
SQLC := go run github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

# Load .env if present so `make run` / `make test` pick up local settings.
ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: help run build test lint generate check-generated migrate-up migrate-down migrate-status db-up db-down

help: ## List targets
	@grep -E '^[a-z-]+:.*## ' Makefile | awk -F':.*## ' '{printf "  %-18s %s\n", $$1, $$2}'

run: ## Run the API locally
	go run ./cmd/makonbook serve

build: ## Build the binary into bin/
	CGO_ENABLED=0 go build -trimpath -o bin/makonbook ./cmd/makonbook

test: ## Run all tests (database tests need TEST_DATABASE_URL)
	go test -race ./...

lint: ## Run golangci-lint
	golangci-lint run ./...

generate: ## Regenerate sqlc code from db/migrations and */queries.sql
	$(SQLC) generate

check-generated: ## Fail if generated code is out of date
	$(SQLC) diff

migrate-up: ## Apply pending migrations
	go run ./cmd/makonbook migrate up

migrate-down: ## Roll back the last migration
	go run ./cmd/makonbook migrate down

migrate-status: ## Show migration state
	go run ./cmd/makonbook migrate status

db-up: ## Start the local Postgres in Docker
	docker compose up -d --wait postgres

db-down: ## Stop the local Postgres (data is kept)
	docker compose down
