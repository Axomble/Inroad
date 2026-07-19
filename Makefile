.PHONY: help db-up db-down migrate-up migrate-down sqlc run-api run-worker build test test-integration tidy

help:
	@grep -E '^[a-zA-Z_-]+:.*?##' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "%-18s %s\n", $$1, $$2}'

db-up: ## Start dev Postgres + Redis
	docker compose -f deploy/compose/docker-compose.dev.yml up -d

db-down: ## Stop dev Postgres + Redis
	docker compose -f deploy/compose/docker-compose.dev.yml down

migrate-up: ## Apply all migrations
	go run ./cmd/migrate up

migrate-down: ## Roll back one migration
	go run ./cmd/migrate down

sqlc: ## Regenerate sqlc code
	sqlc generate

run-api: ## Run the API server
	go run ./cmd/inroad

run-worker: ## Run the worker
	go run ./cmd/worker

build: ## Build all binaries into ./bin
	go build -o bin/inroad ./cmd/inroad
	go build -o bin/worker ./cmd/worker
	go build -o bin/migrate ./cmd/migrate
	go build -o bin/seed ./cmd/seed

test: ## Run unit tests
	go test ./...

test-integration: ## Run integration tests (needs make db-up)
	go test -tags=integration ./...

tidy: ## Tidy go.mod
	go mod tidy
