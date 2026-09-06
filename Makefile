.PHONY: help dev db-wait run-web seed db-up db-down migrate-up migrate-down sqlc sqlc-diff sqlc-vet run-api run-worker build test test-integration tidy lint lint-go lint-web

# .env is loaded here rather than by the binaries: nothing in the Go code reads a
# dotenv file, so the documented `cp .env.example .env && make run-api` failed with
# "INROAD_JWT_SECRET must be set". -include keeps every target usable before the
# file exists (make db-up on a fresh clone).
-include .env
export

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

sqlc-diff: ## Fail if the generated sqlc code is stale (no writes)
	sqlc diff

sqlc-vet: ## PREPARE every query against the TEST database (needs make db-up + migrate)
	sqlc vet

run-api: ## Run the API server
	go run ./cmd/inroad

run-worker: ## Run the worker
	go run ./cmd/worker

run-web: ## Run the SPA dev server
	cd web && npm run dev

seed: ## Create the demo workspace, user, and sample data
	go run ./cmd/seed

build: ## Build all binaries into ./bin
	go build -o bin/inroad ./cmd/inroad
	go build -o bin/worker ./cmd/worker
	go build -o bin/migrate ./cmd/migrate
	go build -o bin/seed ./cmd/seed

test: ## Run unit tests
	go test ./...

# Integration tests run against a SEPARATE database (inroad_test), created on
# demand — see internal/platform/db/dbtest. They used to default to the dev
# database and bury the demo data under thousands of fixture rows.
test-integration: ## Run integration tests against inroad_test (needs make db-up)
	# -p 4 bounds how many packages run at once, and dbtest caps each package's pool
	# at 8 connections (db.Connect's 25-connection floor is for a server, not a test
	# process). Both matter: at the floor, four packages reached a stock
	# max_connections=100 exactly and the suite failed at random with "sorry, too
	# many clients already" in whichever package asked last.
	go test -p 4 -tags=integration ./...

tidy: ## Tidy go.mod
	go mod tidy

lint: lint-go lint-web ## Run all linters (Go + web)

# The ONE place the linter version is written down. CI reads this same line (see
# .github/workflows/ci.yml), because the two drifting apart is not hypothetical: CI
# pinned v2.1.6 while CLAUDE.md told developers to install @latest, so `make lint`
# and the CI gate ran different rule sets for months. main went red locally while
# every PR was green, and 10 noctx findings merged unnoticed.
GOLANGCI_VERSION := 2.12.2

lint-go: ## Run golangci-lint on the Go backend (version-checked)
	@have=$$(golangci-lint version 2>/dev/null | sed -n 's/.*version \([0-9.]*\).*/\1/p'); \
	if [ "$$have" != "$(GOLANGCI_VERSION)" ]; then \
		echo "golangci-lint $(GOLANGCI_VERSION) required, found $${have:-none}."; \
		echo "A different version is a different rule set — the gate would not match CI."; \
		echo "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v$(GOLANGCI_VERSION)"; \
		exit 1; \
	fi
	golangci-lint run ./...

lint-web: ## Run oxlint + strict typecheck on the SPA
	cd web && npm run lint && npm run typecheck

dev-docker: ## Start the WHOLE stack in Docker (no local Go/Node/make needed)
	docker compose -f deploy/compose/docker-compose.dev.yml up

dev-docker-down: ## Stop the Docker dev stack (add ARGS=-v to wipe its data)
	docker compose -f deploy/compose/docker-compose.dev.yml down $(ARGS)

dev: db-up db-wait migrate-up ## Start everything natively: services, migrations, api + worker + web
	@echo ""
	@echo "  api  -> http://localhost:8080"
	@echo "  web  -> http://localhost:5173"
	@echo "  db   -> localhost:5433 (inroad/inroad/inroad)"
	@echo "  Ctrl+C stops all three."
	@echo ""
	@trap 'kill 0' INT TERM; 		go run ./cmd/inroad & 		go run ./cmd/worker & 		(cd web && npm run dev) & 		wait

db-wait: ## Block until Postgres accepts connections
	@echo "waiting for postgres..."
	@for i in $$(seq 1 30); do 		docker compose -f deploy/compose/docker-compose.dev.yml exec -T postgres pg_isready -U inroad -q && exit 0; 		sleep 1; 	done; 	echo "postgres did not become ready" >&2; exit 1
