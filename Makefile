# go-for-java-developer task shortcuts.
# Every target mirrors a command from README.md; CI runs the same commands.
#
#   make test test-race test-integration db-up db-migrate db-stop db-reset
#   make run-example MODULE=internal/concurrency/patterns

DATABASE_URL ?= postgres://app:app@localhost:5432/app?sslmode=disable
export DATABASE_URL

.PHONY: help fmt vet build test test-race test-integration db-up db-migrate db-stop db-reset run-example

help: ## List targets
	@grep -E '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-16s %s\n", $$1, $$2}'

fmt: ## Fail if gofmt would reformat anything
	test -z "$$(gofmt -l .)"

vet: ## Vet default and integration build tags
	go vet ./...
	go vet -tags=integration ./...

build: ## Build all packages
	go build ./...

test: ## Unit tests (no database needed)
	go test ./...

test-race: ## Unit tests with the race detector (mandatory for concurrency code)
	go test -race ./...

test-integration: ## Live-PostgreSQL tests (needs: make db-up db-migrate)
	go test -tags=integration -race ./internal/postgres/integration/

db-up: ## Start PostgreSQL 16 in Docker
	docker compose up -d db

db-migrate: ## Apply testdata/migrations/001_init.sql to the compose DB
	docker compose exec -T db psql -U app -d app -v ON_ERROR_STOP=1 -f /testdata/migrations/001_init.sql

db-stop: ## Stop PostgreSQL, keep data in the pgdata volume
	docker compose stop db

db-reset: ## Stop PostgreSQL and DELETE all data (fresh start next time)
	docker compose down -v

run-example: ## Run one example: make run-example MODULE=internal/concurrency/mutex
	test -n "$(MODULE)" || (echo "usage: make run-example MODULE=<package dir>" && exit 1)
	go run ./$(MODULE)
