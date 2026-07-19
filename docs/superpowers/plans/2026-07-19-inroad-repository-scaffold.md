# Inroad Repository Scaffold Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up the Inroad monorepo skeleton — Go backend + worker, Postgres/sqlc, Redis/asynq, React web app — with one thin vertical slice (auth + workspace) that proves every architectural layer works end-to-end.

**Architecture:** Single Go module monorepo. `cmd/` thin entrypoints wire dependencies from `internal/`. `internal/app/<domain>` feature slices own their business logic and data access; `internal/platform/*` holds cross-cutting infra (config, db, queue, crypto, http, log); `internal/worker` is the execution plane, reaching relational data only through the `internal/coreapi` seam (in-process impl now, HTTP later). A single React 19 SPA under `web/` talks to the API through an RTK Query slice generated from an OpenAPI contract.

**Tech Stack:** Go 1.25 · chi router · pgx/v5 · sqlc · golang-migrate · asynq · golang-jwt/v5 · AES-GCM envelope encryption · React 19 · Vite 7 · Tailwind v4 · Redux Toolkit 2 / RTK Query / redux-persist · TanStack Router · shadcn/Radix · Postgres 16 · Redis 7.

## Global Constraints

- **Go module path:** `github.com/inroad/inroad` — replace `github.com/inroad` with the real GitHub org before the first push (single find-replace across `go.mod` + all imports).
- **Go version floor:** `go 1.25`.
- **Backend layering rules (enforced):** (1) `app/*` may import `platform/*`, never the reverse; (2) `app/*` packages do not import each other; (3) `internal/worker/*` reaches relational data + decrypted credentials only through `internal/coreapi`, never `platform/db`; (4) each domain owns its data access (`app/<domain>/store.go`), no central repository package; (5) routes are registered per-domain via a `Routes() http.Handler` method, no monolithic route file; (6) migrations, sqlc queries, and generated code live together under `internal/platform/db/` (Go `//go:embed` cannot reference parent dirs).
- **Frontend rules (enforced):** (1) `routes/*` files are thin, composing from `features/*`; (2) `features/*` may import `components/`, `store/`, `lib/` — never each other; (3) redux-persist whitelists UI slices only (`ui`, `drafts`, `preferences`); the RTK Query `api` reducer is blacklisted; (4) `store/api.ts` is generated from `api/openapi.yaml`, never hand-edited; (5) `components/ui/` is shadcn-CLI-managed.
- **Tailwind v4:** CSS-first config in `web/src/styles/globals.css` via `@theme` — there is no `tailwind.config.ts`.
- **Secrets:** never commit real secrets. `.env` is gitignored; `.env.example` holds placeholder values only.
- **Commit style:** conventional commits (`feat:`, `chore:`, `test:`, `docs:`). Commit at the end of every task.
- **Integration tests:** guarded by `//go:build integration`; require dev Postgres + Redis (`make db-up`). Unit tests run with no external services.

---

## File Structure

Files created by this plan, grouped by responsibility:

**Repo root / tooling**
- `go.mod`, `go.sum` — module definition
- `Makefile` — dev/build/test/sqlc/migrate targets
- `.gitignore`, `.env.example`, `README.md`, `sqlc.yaml` (points into `internal/platform/db`)
- `docker-compose.yml` — root wrapper → `deploy/compose/docker-compose.yml`

**Platform (`internal/platform/`)**
- `config/config.go` — env → typed `Config`
- `log/log.go` — slog JSON logger
- `db/db.go` — pgx pool; `db/migrate.go` — embedded migrate; `db/migrations/*.sql`; `db/queries/*.sql`; `db/gen/*` (sqlc output)
- `httpx/router.go` — chi router + middleware + `/healthz`; `httpx/server.go` — http.Server lifecycle; `httpx/respond.go` — JSON + error helpers
- `queue/queue.go` — asynq client/server wrappers + task-type constants
- `crypto/sealer.go` — AES-GCM envelope sealer

**Domains (`internal/app/`)**
- `auth/` — `password.go`, `jwt.go`, `service.go`, `handler.go`, `routes.go`, tests
- `workspace/` — `store.go`, `service.go`, `handler.go`, `routes.go`, tests

**Execution plane**
- `internal/coreapi/coreapi.go` — interface; `internal/coreapi/inprocess/inprocess.go` — v1 impl
- `internal/worker/handlers.go` — asynq handler registration; `internal/worker/warmup/warmup.go` — no-op ramp task

**Entrypoints (`cmd/`)**
- `cmd/inroad/main.go` — API server; `cmd/worker/main.go` — worker; `cmd/migrate/main.go` — migrations; `cmd/seed/main.go` — dev seed

**API contract**
- `api/openapi.yaml` — REST contract (auth + workspace endpoints)

**Web (`web/`)**
- Vite/React/Tailwind/Redux/Router scaffold (see Task 12)

**Deploy**
- `deploy/compose/docker-compose.yml` (+ `.dev.yml`), `deploy/docker/Dockerfile.api`, `deploy/docker/Dockerfile.worker`

**Docs**
- `docs/architecture.md`, `docs/self-hosting.md`

---

## Task 1: Repo skeleton, module, tooling

**Files:**
- Create: `go.mod`, `.gitignore`, `.env.example`, `README.md`, `Makefile`
- Create: `internal/platform/.gitkeep`, `internal/app/.gitkeep`, `cmd/.gitkeep`, `web/.gitkeep`, `deploy/.gitkeep`, `docs/architecture.md`

**Interfaces:**
- Consumes: nothing (first task).
- Produces: buildable empty module at `github.com/inroad/inroad`; `make` targets other tasks extend.

- [ ] **Step 1: Initialize the Go module**

Run:
```bash
go mod init github.com/inroad/inroad
go mod edit -go=1.25
```
Expected: `go.mod` created containing `module github.com/inroad/inroad` and `go 1.25`.

- [ ] **Step 2: Create `.gitignore`**

```gitignore
# Go
/bin/
*.test
*.out

# Env / secrets
.env
*.local

# Node
web/node_modules/
web/dist/

# sqlc / build artifacts
/tmp/

# OS
.DS_Store
Thumbs.db
```

- [ ] **Step 3: Create `.env.example`**

```dotenv
# Server
INROAD_ENV=development
INROAD_HTTP_ADDR=:8080

# Postgres
INROAD_DATABASE_URL=postgres://inroad:inroad@localhost:5432/inroad?sslmode=disable

# Redis
INROAD_REDIS_ADDR=localhost:6379

# Auth — generate with: openssl rand -base64 32
INROAD_JWT_SECRET=replace-me-with-32-byte-secret

# Envelope encryption master key (base64 of 32 raw bytes) — openssl rand -base64 32
INROAD_MASTER_KEY=replace-me-with-base64-32-bytes
```

- [ ] **Step 4: Create `README.md`**

```markdown
# Inroad

Self-hostable cold email sequencing + mailbox warmup platform (open-core).

## Quick start (dev)

    cp .env.example .env        # then fill in secrets
    make db-up                  # start Postgres + Redis
    make migrate-up             # apply migrations
    make run-api                # start the API server on :8080
    make run-worker             # (separate shell) start the worker

Web app:

    cd web && npm install && npm run dev

## Layout

- `cmd/`        thin binary entrypoints
- `internal/app/`       feature-sliced domains
- `internal/platform/`  cross-cutting infra
- `internal/worker/`    execution plane
- `internal/coreapi/`   control⇄execution seam
- `web/`        React SPA
- `deploy/`     Docker + compose + systemd
- `docs/`       architecture + self-hosting guides
```

- [ ] **Step 5: Create `Makefile`**

```makefile
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
```

- [ ] **Step 6: Create placeholder dirs + first doc**

Create empty `.gitkeep` files at `internal/platform/.gitkeep`, `internal/app/.gitkeep`, `cmd/.gitkeep`, `web/.gitkeep`, `deploy/.gitkeep`, and `docs/architecture.md`:

`docs/architecture.md`:
```markdown
# Inroad Architecture

See `docs/superpowers/specs/2026-07-19-outpost-repo-architecture-design.md` for the
full layout rationale. This document tracks decisions as they evolve during build.

## Planes
- **Control plane:** API server + Postgres + Redis.
- **Execution plane:** worker(s), reaching data only through `internal/coreapi`.
```

- [ ] **Step 7: Verify the module builds**

Run: `go build ./...`
Expected: exits 0, no output (nothing to build yet, but the module resolves).

- [ ] **Step 8: Commit**

```bash
git add .
git commit -m "chore: scaffold module, tooling, and repo skeleton"
```

---

## Task 2: Config loader + structured logger

**Files:**
- Create: `internal/platform/config/config.go`, `internal/platform/config/config_test.go`
- Create: `internal/platform/log/log.go`

**Interfaces:**
- Consumes: nothing.
- Produces:
  - `config.Load() (*config.Config, error)` where `Config` has fields `Env, HTTPAddr, DatabaseURL, RedisAddr string`, `JWTSecret []byte`, `MasterKey []byte`.
  - `log.New(env string) *slog.Logger`.

- [ ] **Step 1: Write the failing config test**

`internal/platform/config/config_test.go`:
```go
package config

import (
	"os"
	"testing"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	t.Setenv("INROAD_ENV", "production")
	t.Setenv("INROAD_JWT_SECRET", "0123456789abcdef0123456789abcdef")
	// 32 raw bytes, base64-encoded:
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	os.Unsetenv("INROAD_HTTP_ADDR")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.Env != "production" {
		t.Errorf("Env = %q, want production", cfg.Env)
	}
	if cfg.HTTPAddr != ":8080" {
		t.Errorf("HTTPAddr = %q, want default :8080", cfg.HTTPAddr)
	}
	if len(cfg.MasterKey) != 32 {
		t.Errorf("MasterKey len = %d, want 32", len(cfg.MasterKey))
	}
}

func TestLoadRejectsMissingSecret(t *testing.T) {
	t.Setenv("INROAD_JWT_SECRET", "")
	t.Setenv("INROAD_MASTER_KEY", "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for empty JWT secret, got nil")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/config/`
Expected: FAIL (`undefined: Load`).

- [ ] **Step 3: Implement the config loader**

`internal/platform/config/config.go`:
```go
// Package config loads runtime configuration from environment variables.
package config

import (
	"encoding/base64"
	"fmt"
	"os"
)

type Config struct {
	Env         string
	HTTPAddr    string
	DatabaseURL string
	RedisAddr   string
	JWTSecret   []byte
	MasterKey   []byte
}

func Load() (*Config, error) {
	cfg := &Config{
		Env:         getenv("INROAD_ENV", "development"),
		HTTPAddr:    getenv("INROAD_HTTP_ADDR", ":8080"),
		DatabaseURL: getenv("INROAD_DATABASE_URL", "postgres://inroad:inroad@localhost:5432/inroad?sslmode=disable"),
		RedisAddr:   getenv("INROAD_REDIS_ADDR", "localhost:6379"),
	}

	secret := os.Getenv("INROAD_JWT_SECRET")
	if len(secret) < 16 {
		return nil, fmt.Errorf("INROAD_JWT_SECRET must be set and at least 16 bytes")
	}
	cfg.JWTSecret = []byte(secret)

	rawKey, err := base64.StdEncoding.DecodeString(os.Getenv("INROAD_MASTER_KEY"))
	if err != nil {
		return nil, fmt.Errorf("INROAD_MASTER_KEY must be valid base64: %w", err)
	}
	if len(rawKey) != 32 {
		return nil, fmt.Errorf("INROAD_MASTER_KEY must decode to 32 bytes, got %d", len(rawKey))
	}
	cfg.MasterKey = rawKey

	return cfg, nil
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 4: Implement the logger**

`internal/platform/log/log.go`:
```go
// Package log builds the application's structured logger.
package log

import (
	"log/slog"
	"os"
)

// New returns a JSON slog logger. In development it also lowers the level to Debug.
func New(env string) *slog.Logger {
	level := slog.LevelInfo
	if env == "development" {
		level = slog.LevelDebug
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/platform/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/platform/config internal/platform/log
git commit -m "feat: add config loader and structured logger"
```

---

## Task 3: Database layer — pgx pool, migrations, sqlc, dev compose

**Files:**
- Create: `deploy/compose/docker-compose.dev.yml`
- Create: `internal/platform/db/db.go`, `internal/platform/db/migrate.go`
- Create: `internal/platform/db/migrations/000001_init.up.sql`, `.../000001_init.down.sql`
- Create: `internal/platform/db/queries/workspace.sql`, `internal/platform/db/queries/user.sql`
- Create: `sqlc.yaml`
- Create: `internal/platform/db/db_integration_test.go`
- Generated: `internal/platform/db/gen/*` (via `sqlc generate`)

**Interfaces:**
- Consumes: `config.Config.DatabaseURL`.
- Produces:
  - `db.Connect(ctx context.Context, url string) (*pgxpool.Pool, error)`
  - `db.Migrate(url string) error`
  - sqlc package `gen` with `gen.New(db gen.DBTX) *gen.Queries` and generated methods `CreateWorkspace`, `GetWorkspace`, `CreateUser`, `GetUserByEmail`.

- [ ] **Step 1: Create the dev database compose file**

`deploy/compose/docker-compose.dev.yml`:
```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: inroad
      POSTGRES_PASSWORD: inroad
      POSTGRES_DB: inroad
    ports: ["5432:5432"]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U inroad"]
      interval: 3s
      timeout: 3s
      retries: 10
  redis:
    image: redis:7-alpine
    ports: ["6379:6379"]
```

- [ ] **Step 2: Write the first migration**

`internal/platform/db/migrations/000001_init.up.sql`:
```sql
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE workspaces (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE users (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email         TEXT NOT NULL,
    password_hash TEXT NOT NULL,
    role          TEXT NOT NULL DEFAULT 'owner',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (workspace_id, email)
);

CREATE INDEX idx_users_email ON users (email);
```

`internal/platform/db/migrations/000001_init.down.sql`:
```sql
DROP TABLE IF EXISTS users;
DROP TABLE IF EXISTS workspaces;
```

- [ ] **Step 3: Write the sqlc query files**

`internal/platform/db/queries/workspace.sql`:
```sql
-- name: CreateWorkspace :one
INSERT INTO workspaces (name) VALUES ($1) RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1;
```

`internal/platform/db/queries/user.sql`:
```sql
-- name: CreateUser :one
INSERT INTO users (workspace_id, email, password_hash, role)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = $1 LIMIT 1;
```

- [ ] **Step 4: Create `sqlc.yaml`**

`sqlc.yaml` (repo root):
```yaml
version: "2"
sql:
  - engine: "postgresql"
    schema: "internal/platform/db/migrations"
    queries: "internal/platform/db/queries"
    gen:
      go:
        package: "gen"
        out: "internal/platform/db/gen"
        sql_package: "pgx/v5"
        emit_json_tags: true
        emit_pointers_for_null_types: true
        overrides:
          - db_type: "uuid"
            go_type: "github.com/google/uuid.UUID"
```

> The `uuid` override makes sqlc emit `google/uuid.UUID` (with a `.String()` method) instead of the default `pgtype.UUID`. The pool in `db.go` registers the matching pgx codec so scanning works.

- [ ] **Step 5: Generate sqlc code**

Run: `sqlc generate`
Expected: creates `internal/platform/db/gen/db.go`, `models.go`, `workspace.sql.go`, `user.sql.go`. Exit 0.

(If `sqlc` is not installed: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`.)

- [ ] **Step 6: Implement the pool + migrate helpers**

`internal/platform/db/db.go`:
```go
// Package db owns the Postgres connection pool, schema migrations, and sqlc output.
package db

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	pgxuuid "github.com/vgarvardt/pgx-google-uuid/v5"
)

// Connect opens a pgx connection pool, registers the google/uuid codec so
// sqlc's uuid.UUID columns scan correctly, and verifies connectivity.
func Connect(ctx context.Context, url string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		return nil, err
	}
	cfg.AfterConnect = func(_ context.Context, conn *pgx.Conn) error {
		pgxuuid.Register(conn.TypeMap())
		return nil
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	return pool, nil
}
```

`internal/platform/db/migrate.go`:
```go
package db

import (
	"embed"
	"errors"

	"github.com/golang-migrate/migrate/v4"
	"github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies all up migrations. It is a no-op if the schema is current.
func Migrate(url string) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, "pgx5://"+trimScheme(url))
	if err != nil {
		return err
	}
	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// MigrateDown rolls back a single migration.
func MigrateDown(url string) error {
	src, err := iofs.New(migrationsFS, "migrations")
	if err != nil {
		return err
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, "pgx5://"+trimScheme(url))
	if err != nil {
		return err
	}
	if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// trimScheme converts a postgres:// URL into the driver-prefixed form migrate expects.
func trimScheme(url string) string {
	for _, p := range []string{"postgres://", "postgresql://"} {
		if len(url) >= len(p) && url[:len(p)] == p {
			return url[len(p):]
		}
	}
	return url
}
```

- [ ] **Step 7: Fetch dependencies**

Run:
```bash
go get github.com/jackc/pgx/v5@latest
go get github.com/golang-migrate/migrate/v4@latest
go mod tidy
```
Expected: modules added to `go.mod`, exit 0.

- [ ] **Step 8: Write the integration test**

`internal/platform/db/db_integration_test.go`:
```go
//go:build integration

package db

import (
	"context"
	"os"
	"testing"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5432/inroad?sslmode=disable"
}

func TestMigrateAndConnect(t *testing.T) {
	if err := Migrate(dsn()); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	pool, err := Connect(context.Background(), dsn())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer pool.Close()

	var n int
	err = pool.QueryRow(context.Background(),
		`SELECT count(*) FROM information_schema.tables WHERE table_name IN ('users','workspaces')`).Scan(&n)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if n != 2 {
		t.Fatalf("expected 2 tables, got %d", n)
	}
}
```

- [ ] **Step 9: Run the integration test**

Run:
```bash
make db-up
go test -tags=integration ./internal/platform/db/
```
Expected: PASS (2 tables found). Also confirm `go build ./...` succeeds.

- [ ] **Step 10: Commit**

```bash
git add sqlc.yaml internal/platform/db deploy/compose/docker-compose.dev.yml go.mod go.sum
git commit -m "feat: add db pool, embedded migrations, and sqlc for workspaces/users"
```

---

## Task 4: HTTP foundation — chi router, middleware, health, JSON helpers

**Files:**
- Create: `internal/platform/httpx/router.go`, `internal/platform/httpx/server.go`, `internal/platform/httpx/respond.go`
- Create: `internal/platform/httpx/router_test.go`

**Interfaces:**
- Consumes: `*slog.Logger`.
- Produces:
  - `httpx.NewRouter(logger *slog.Logger) *chi.Mux` — pre-wired with request-id, recovery, logging middleware and `GET /healthz`.
  - `httpx.NewServer(addr string, h http.Handler) *http.Server`; `httpx.Run(ctx, srv) error` (graceful shutdown).
  - `httpx.JSON(w, status, v)`; `httpx.Error(w, status, msg)`.

- [ ] **Step 1: Write the failing router test**

`internal/platform/httpx/router_test.go`:
```go
package httpx

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthz(t *testing.T) {
	r := NewRouter(slog.New(slog.NewJSONHandler(io.Discard, nil)))
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad json: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("status field = %q, want ok", body["status"])
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/httpx/`
Expected: FAIL (`undefined: NewRouter`).

- [ ] **Step 3: Implement JSON helpers**

`internal/platform/httpx/respond.go`:
```go
package httpx

import (
	"encoding/json"
	"net/http"
)

// JSON writes v as a JSON response with the given status code.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes a JSON error envelope: {"error": msg}.
func Error(w http.ResponseWriter, status int, msg string) {
	JSON(w, status, map[string]string{"error": msg})
}
```

- [ ] **Step 4: Implement the router**

`internal/platform/httpx/router.go`:
```go
// Package httpx holds HTTP server bootstrap, routing, and response helpers.
package httpx

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// NewRouter returns a chi mux pre-wired with standard middleware and a health check.
func NewRouter(logger *slog.Logger) *chi.Mux {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(slogRequestLogger(logger))

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return r
}

func slogRequestLogger(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			logger.Info("http_request",
				"method", r.Method,
				"path", r.URL.Path,
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}
```

- [ ] **Step 5: Implement server lifecycle**

`internal/platform/httpx/server.go`:
```go
package httpx

import (
	"context"
	"net/http"
	"time"
)

// NewServer builds an http.Server with sane timeouts.
func NewServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
}

// Run starts srv and shuts it down gracefully when ctx is cancelled.
func Run(ctx context.Context, srv *http.Server) error {
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
```

- [ ] **Step 6: Fetch chi + run tests**

Run:
```bash
go get github.com/go-chi/chi/v5@latest
go mod tidy
go test ./internal/platform/httpx/
```
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/platform/httpx go.mod go.sum
git commit -m "feat: add chi router, middleware, health check, and http helpers"
```

---

## Task 5: Envelope encryption sealer

**Files:**
- Create: `internal/platform/crypto/sealer.go`, `internal/platform/crypto/sealer_test.go`

**Interfaces:**
- Consumes: 32-byte master key (`config.Config.MasterKey`).
- Produces:
  - `crypto.NewSealer(masterKey []byte) (*crypto.Sealer, error)`
  - `(*Sealer) Seal(plaintext []byte) (string, error)` — returns base64(nonce||ciphertext)
  - `(*Sealer) Open(token string) ([]byte, error)`

- [ ] **Step 1: Write the failing round-trip test**

`internal/platform/crypto/sealer_test.go`:
```go
package crypto

import (
	"bytes"
	"testing"
)

func key32() []byte { return bytes.Repeat([]byte{0x11}, 32) }

func TestSealOpenRoundTrip(t *testing.T) {
	s, err := NewSealer(key32())
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	secret := []byte("smtp-app-password")
	token, err := s.Seal(secret)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	if bytes.Contains([]byte(token), secret) {
		t.Fatal("ciphertext leaked plaintext")
	}
	got, err := s.Open(token)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if !bytes.Equal(got, secret) {
		t.Fatalf("round-trip mismatch: got %q", got)
	}
}

func TestNewSealerRejectsBadKey(t *testing.T) {
	if _, err := NewSealer([]byte("short")); err == nil {
		t.Fatal("expected error for short key")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/crypto/`
Expected: FAIL (`undefined: NewSealer`).

- [ ] **Step 3: Implement the sealer**

`internal/platform/crypto/sealer.go`:
```go
// Package crypto provides authenticated envelope encryption for stored credentials.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
)

// Sealer encrypts and decrypts small secrets (OAuth tokens, SMTP passwords)
// using AES-256-GCM under a single master key. Per-tenant data keys are a
// future extension; the interface stays the same.
type Sealer struct {
	aead cipher.AEAD
}

func NewSealer(masterKey []byte) (*Sealer, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes, got %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Sealer{aead: aead}, nil
}

// Seal returns base64(nonce || ciphertext).
func (s *Sealer) Seal(plaintext []byte) (string, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, plaintext, nil)
	return base64.StdEncoding.EncodeToString(ct), nil
}

// Open reverses Seal.
func (s *Sealer) Open(token string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(token)
	if err != nil {
		return nil, err
	}
	ns := s.aead.NonceSize()
	if len(raw) < ns {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := raw[:ns], raw[ns:]
	return s.aead.Open(nil, nonce, ct, nil)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/platform/crypto/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/crypto
git commit -m "feat: add AES-GCM envelope encryption sealer"
```

---

## Task 6: Queue wrappers (asynq) + task registry

**Files:**
- Create: `internal/platform/queue/queue.go`, `internal/platform/queue/queue_test.go`

**Interfaces:**
- Consumes: `config.Config.RedisAddr`.
- Produces:
  - `queue.NewClient(redisAddr string) *queue.Client` with `(*Client) EnqueueWarmupTick(mailboxID string) error` and `(*Client) Close() error`.
  - `queue.NewServer(redisAddr string, logger *slog.Logger) *asynq.Server`
  - `queue.NewMux() *asynq.ServeMux`
  - Constant `queue.TaskWarmupTick = "warmup:tick"` and helper `queue.WarmupTickPayload` with `MailboxID string`.

- [ ] **Step 1: Write the failing payload test**

`internal/platform/queue/queue_test.go`:
```go
package queue

import (
	"encoding/json"
	"testing"
)

func TestWarmupTickPayloadRoundTrip(t *testing.T) {
	p := WarmupTickPayload{MailboxID: "mb-123"}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got WarmupTickPayload
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.MailboxID != "mb-123" {
		t.Errorf("MailboxID = %q, want mb-123", got.MailboxID)
	}
	if TaskWarmupTick != "warmup:tick" {
		t.Errorf("TaskWarmupTick = %q", TaskWarmupTick)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/platform/queue/`
Expected: FAIL (`undefined: WarmupTickPayload`).

- [ ] **Step 3: Implement the queue package**

`internal/platform/queue/queue.go`:
```go
// Package queue wraps asynq: task-type constants, typed enqueue helpers,
// and server/mux constructors. This is the only place asynq is imported.
package queue

import (
	"encoding/json"
	"log/slog"

	"github.com/hibiken/asynq"
)

const TaskWarmupTick = "warmup:tick"

// WarmupTickPayload is the body of a warmup:tick task.
type WarmupTickPayload struct {
	MailboxID string `json:"mailbox_id"`
}

// Client enqueues tasks onto Redis.
type Client struct {
	inner *asynq.Client
}

func NewClient(redisAddr string) *Client {
	return &Client{inner: asynq.NewClient(asynq.RedisClientOpt{Addr: redisAddr})}
}

func (c *Client) EnqueueWarmupTick(mailboxID string) error {
	b, err := json.Marshal(WarmupTickPayload{MailboxID: mailboxID})
	if err != nil {
		return err
	}
	_, err = c.inner.Enqueue(asynq.NewTask(TaskWarmupTick, b))
	return err
}

func (c *Client) Close() error { return c.inner.Close() }

// NewServer builds an asynq processing server.
func NewServer(redisAddr string, logger *slog.Logger) *asynq.Server {
	return asynq.NewServer(
		asynq.RedisClientOpt{Addr: redisAddr},
		asynq.Config{Concurrency: 10},
	)
}

// NewMux returns an empty task router for worker handlers to register on.
func NewMux() *asynq.ServeMux { return asynq.NewServeMux() }
```

- [ ] **Step 4: Fetch asynq + run tests**

Run:
```bash
go get github.com/hibiken/asynq@latest
go mod tidy
go test ./internal/platform/queue/
```
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/platform/queue go.mod go.sum
git commit -m "feat: add asynq queue wrappers and warmup task registry"
```

---

## Task 7: Auth domain — password hashing + JWT

**Files:**
- Create: `internal/app/auth/password.go`, `internal/app/auth/jwt.go`
- Create: `internal/app/auth/password_test.go`, `internal/app/auth/jwt_test.go`

**Interfaces:**
- Consumes: `config.Config.JWTSecret`.
- Produces:
  - `auth.HashPassword(pw string) (string, error)`; `auth.CheckPassword(hash, pw string) bool`
  - `auth.IssueToken(secret []byte, userID, workspaceID string, ttl time.Duration) (string, error)`
  - `auth.ParseToken(secret []byte, token string) (auth.Claims, error)` where `Claims{ UserID, WorkspaceID string }`.

- [ ] **Step 1: Write failing password + jwt tests**

`internal/app/auth/password_test.go`:
```go
package auth

import "testing"

func TestHashAndCheckPassword(t *testing.T) {
	hash, err := HashPassword("s3cret-pw")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if hash == "s3cret-pw" {
		t.Fatal("hash equals plaintext")
	}
	if !CheckPassword(hash, "s3cret-pw") {
		t.Error("CheckPassword returned false for correct password")
	}
	if CheckPassword(hash, "wrong") {
		t.Error("CheckPassword returned true for wrong password")
	}
}
```

`internal/app/auth/jwt_test.go`:
```go
package auth

import (
	"testing"
	"time"
)

func TestIssueAndParseToken(t *testing.T) {
	secret := []byte("0123456789abcdef0123456789abcdef")
	tok, err := IssueToken(secret, "user-1", "ws-1", time.Hour)
	if err != nil {
		t.Fatalf("IssueToken: %v", err)
	}
	claims, err := ParseToken(secret, tok)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if claims.UserID != "user-1" || claims.WorkspaceID != "ws-1" {
		t.Fatalf("claims = %+v", claims)
	}
}

func TestParseTokenRejectsWrongSecret(t *testing.T) {
	tok, _ := IssueToken([]byte("0123456789abcdef0123456789abcdef"), "u", "w", time.Hour)
	if _, err := ParseToken([]byte("different-secret-different-secret"), tok); err == nil {
		t.Fatal("expected error for wrong secret")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/app/auth/`
Expected: FAIL (`undefined: HashPassword`).

- [ ] **Step 3: Implement password hashing**

`internal/app/auth/password.go`:
```go
// Package auth handles registration, login, password hashing, and JWT sessions.
package auth

import "golang.org/x/crypto/bcrypt"

func HashPassword(pw string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

func CheckPassword(hash, pw string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
```

- [ ] **Step 4: Implement JWT**

`internal/app/auth/jwt.go`:
```go
package auth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims are the application claims embedded in a session token.
type Claims struct {
	UserID      string
	WorkspaceID string
}

type jwtClaims struct {
	WorkspaceID string `json:"wid"`
	jwt.RegisteredClaims
}

func IssueToken(secret []byte, userID, workspaceID string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := jwtClaims{
		WorkspaceID: workspaceID,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   userID,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func ParseToken(secret []byte, token string) (Claims, error) {
	var c jwtClaims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return secret, nil
	})
	if err != nil {
		return Claims{}, err
	}
	return Claims{UserID: c.Subject, WorkspaceID: c.WorkspaceID}, nil
}
```

- [ ] **Step 5: Fetch deps + run tests**

Run:
```bash
go get github.com/golang-jwt/jwt/v5@latest golang.org/x/crypto@latest
go mod tidy
go test ./internal/app/auth/
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/auth go.mod go.sum
git commit -m "feat: add password hashing and JWT session tokens"
```

---

## Task 8: Workspace domain — store + service (the tenant root)

**Files:**
- Create: `internal/app/workspace/store.go`, `internal/app/workspace/service.go`
- Create: `internal/app/workspace/service_integration_test.go`

**Interfaces:**
- Consumes: `gen.Queries` (Task 3), `auth.HashPassword` (Task 7).
- Produces:
  - `workspace.NewStore(q *gen.Queries) *workspace.Store`
  - `workspace.NewService(store *workspace.Store) *workspace.Service`
  - `(*Service) Register(ctx, RegisterInput) (RegisterResult, error)` where `RegisterInput{ WorkspaceName, Email, Password string }` and `RegisterResult{ WorkspaceID, UserID string }`.
  - `(*Service) Authenticate(ctx, email, password string) (userID, workspaceID string, err error)`; sentinel `workspace.ErrInvalidCredentials`.

- [ ] **Step 1: Write the failing integration test**

`internal/app/workspace/service_integration_test.go`:
```go
//go:build integration

package workspace

import (
	"context"
	"os"
	"testing"

	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func dsn() string {
	if v := os.Getenv("INROAD_DATABASE_URL"); v != "" {
		return v
	}
	return "postgres://inroad:inroad@localhost:5432/inroad?sslmode=disable"
}

func newService(t *testing.T) *Service {
	t.Helper()
	if err := db.Migrate(dsn()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := db.Connect(context.Background(), dsn())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewService(NewStore(gen.New(pool)))
}

func TestRegisterThenAuthenticate(t *testing.T) {
	svc := newService(t)
	ctx := context.Background()

	res, err := svc.Register(ctx, RegisterInput{
		WorkspaceName: "Acme",
		Email:         "founder@acme.test",
		Password:      "hunter2hunter2",
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}
	if res.WorkspaceID == "" || res.UserID == "" {
		t.Fatalf("empty ids: %+v", res)
	}

	uid, wid, err := svc.Authenticate(ctx, "founder@acme.test", "hunter2hunter2")
	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if uid != res.UserID || wid != res.WorkspaceID {
		t.Errorf("auth ids mismatch: got (%s,%s) want (%s,%s)", uid, wid, res.UserID, res.WorkspaceID)
	}

	if _, _, err := svc.Authenticate(ctx, "founder@acme.test", "wrong"); err != ErrInvalidCredentials {
		t.Errorf("expected ErrInvalidCredentials, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -tags=integration ./internal/app/workspace/`
Expected: FAIL (`undefined: Service`).

- [ ] **Step 3: Implement the store**

`internal/app/workspace/store.go`:
```go
// Package workspace is the tenant root: workspaces and their member users.
package workspace

import (
	"context"

	"github.com/inroad/inroad/internal/platform/db/gen"
)

// Store wraps sqlc queries this domain needs. Data access lives inside the domain.
type Store struct {
	q *gen.Queries
}

func NewStore(q *gen.Queries) *Store { return &Store{q: q} }

func (s *Store) CreateWorkspace(ctx context.Context, name string) (gen.Workspace, error) {
	return s.q.CreateWorkspace(ctx, name)
}

func (s *Store) CreateUser(ctx context.Context, arg gen.CreateUserParams) (gen.User, error) {
	return s.q.CreateUser(ctx, arg)
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (gen.User, error) {
	return s.q.GetUserByEmail(ctx, email)
}
```

> Note: `gen.CreateUserParams` fields are `WorkspaceID uuid.UUID`, `Email string`, `PasswordHash string`, `Role string` (sqlc-generated in Task 3). Use `github.com/google/uuid` for the ID type sqlc emits with pgx/v5.

- [ ] **Step 4: Implement the service**

`internal/app/workspace/service.go`:
```go
package workspace

import (
	"context"
	"errors"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

// ErrInvalidCredentials is returned when email/password authentication fails.
var ErrInvalidCredentials = errors.New("invalid credentials")

type Service struct {
	store *Store
}

func NewService(store *Store) *Service { return &Service{store: store} }

type RegisterInput struct {
	WorkspaceName string
	Email         string
	Password      string
}

type RegisterResult struct {
	WorkspaceID string
	UserID      string
}

// Register creates a workspace and its first (owner) user atomically enough for v1.
func (s *Service) Register(ctx context.Context, in RegisterInput) (RegisterResult, error) {
	ws, err := s.store.CreateWorkspace(ctx, in.WorkspaceName)
	if err != nil {
		return RegisterResult{}, err
	}
	hash, err := auth.HashPassword(in.Password)
	if err != nil {
		return RegisterResult{}, err
	}
	user, err := s.store.CreateUser(ctx, gen.CreateUserParams{
		WorkspaceID:  ws.ID,
		Email:        in.Email,
		PasswordHash: hash,
		Role:         "owner",
	})
	if err != nil {
		return RegisterResult{}, err
	}
	return RegisterResult{
		WorkspaceID: ws.ID.String(),
		UserID:      user.ID.String(),
	}, nil
}

// Authenticate verifies credentials and returns the user and workspace ids.
func (s *Service) Authenticate(ctx context.Context, email, password string) (string, string, error) {
	user, err := s.store.GetUserByEmail(ctx, email)
	if err != nil {
		return "", "", ErrInvalidCredentials
	}
	if !auth.CheckPassword(user.PasswordHash, password) {
		return "", "", ErrInvalidCredentials
	}
	return user.ID.String(), user.WorkspaceID.String(), nil
}
```

- [ ] **Step 5: Fetch uuid + run the test**

Run:
```bash
go get github.com/google/uuid@latest
go mod tidy
make db-up
go test -tags=integration ./internal/app/workspace/
```
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/app/workspace go.mod go.sum
git commit -m "feat: add workspace domain with register/authenticate"
```

---

## Task 9: Auth HTTP handlers + per-domain routes + JWT middleware

**Files:**
- Create: `internal/app/auth/middleware.go`
- Create: `internal/app/workspace/handler.go`, `internal/app/workspace/routes.go`
- Create: `internal/app/workspace/handler_test.go`

**Interfaces:**
- Consumes: `workspace.Service` (Task 8), `auth.IssueToken`/`ParseToken` (Task 7), `httpx.JSON`/`Error` (Task 4).
- Produces:
  - `workspace.NewHandler(svc *Service, jwtSecret []byte) *Handler`
  - `(*Handler) Routes() http.Handler` mounting `POST /register`, `POST /login`.
  - `auth.RequireAuth(secret []byte) func(http.Handler) http.Handler`; `auth.UserFromContext(ctx) (auth.Claims, bool)`.

- [ ] **Step 1: Write the failing handler test**

`internal/app/workspace/handler_test.go`:
```go
package workspace

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeService lets us test HTTP wiring without a database.
type registerFunc func(RegisterInput) (RegisterResult, error)

func TestRegisterHandlerValidation(t *testing.T) {
	h := NewHandler(nil, []byte("0123456789abcdef0123456789abcdef"))
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	h.Routes().ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty body", w.Code)
	}
}
```

> This test exercises input validation before any service call, so a nil service is safe. The register/login success paths are covered by Task 8's integration test plus the end-to-end check in Task 13.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/app/workspace/ -run TestRegisterHandlerValidation`
Expected: FAIL (`undefined: NewHandler`).

- [ ] **Step 3: Implement the handler**

`internal/app/workspace/handler.go`:
```go
package workspace

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/inroad/inroad/internal/app/auth"
	"github.com/inroad/inroad/internal/platform/httpx"
)

type Handler struct {
	svc       *Service
	jwtSecret []byte
}

func NewHandler(svc *Service, jwtSecret []byte) *Handler {
	return &Handler{svc: svc, jwtSecret: jwtSecret}
}

type registerRequest struct {
	WorkspaceName string `json:"workspace_name"`
	Email         string `json:"email"`
	Password      string `json:"password"`
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type tokenResponse struct {
	Token       string `json:"token"`
	WorkspaceID string `json:"workspace_id"`
	UserID      string `json:"user_id"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	if req.WorkspaceName == "" || req.Email == "" || len(req.Password) < 8 {
		httpx.Error(w, http.StatusBadRequest, "workspace_name, email, and 8+ char password required")
		return
	}
	res, err := h.svc.Register(r.Context(), RegisterInput{
		WorkspaceName: req.WorkspaceName, Email: req.Email, Password: req.Password,
	})
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not register")
		return
	}
	h.issue(w, res.UserID, res.WorkspaceID)
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpx.Error(w, http.StatusBadRequest, "invalid json")
		return
	}
	uid, wid, err := h.svc.Authenticate(r.Context(), req.Email, req.Password)
	if err != nil {
		httpx.Error(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	h.issue(w, uid, wid)
}

func (h *Handler) issue(w http.ResponseWriter, userID, workspaceID string) {
	tok, err := auth.IssueToken(h.jwtSecret, userID, workspaceID, 24*time.Hour)
	if err != nil {
		httpx.Error(w, http.StatusInternalServerError, "could not issue token")
		return
	}
	httpx.JSON(w, http.StatusOK, tokenResponse{Token: tok, WorkspaceID: workspaceID, UserID: userID})
}
```

- [ ] **Step 4: Implement the routes**

`internal/app/workspace/routes.go`:
```go
package workspace

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// Routes returns this domain's HTTP surface, mounted by the server under /api/v1.
func (h *Handler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Post("/register", h.register)
	r.Post("/login", h.login)
	return r
}
```

- [ ] **Step 5: Implement auth middleware**

`internal/app/auth/middleware.go`:
```go
package auth

import (
	"context"
	"net/http"
	"strings"
)

type ctxKey struct{}

// RequireAuth rejects requests without a valid Bearer token and stores the
// resulting Claims in the request context.
func RequireAuth(secret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := r.Header.Get("Authorization")
			token, ok := strings.CutPrefix(h, "Bearer ")
			if !ok {
				http.Error(w, `{"error":"missing bearer token"}`, http.StatusUnauthorized)
				return
			}
			claims, err := ParseToken(secret, token)
			if err != nil {
				http.Error(w, `{"error":"invalid token"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), ctxKey{}, claims)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// UserFromContext returns the authenticated claims, if present.
func UserFromContext(ctx context.Context) (Claims, bool) {
	c, ok := ctx.Value(ctxKey{}).(Claims)
	return c, ok
}
```

- [ ] **Step 6: Run tests + build**

Run:
```bash
go test ./internal/app/...
go build ./...
```
Expected: unit tests PASS; build succeeds.

- [ ] **Step 7: Commit**

```bash
git add internal/app/auth internal/app/workspace
git commit -m "feat: add auth handlers, per-domain routes, and JWT middleware"
```

---

## Task 10: coreapi seam + worker skeleton

**Files:**
- Create: `internal/coreapi/coreapi.go`
- Create: `internal/coreapi/inprocess/inprocess.go`
- Create: `internal/worker/warmup/warmup.go`, `internal/worker/handlers.go`
- Create: `internal/worker/warmup/warmup_test.go`

**Interfaces:**
- Consumes: `queue.WarmupTickPayload` + `queue.TaskWarmupTick` (Task 6).
- Produces:
  - `coreapi.Client` interface with `MailboxExists(ctx, id string) (bool, error)` (minimal seam; grows later).
  - `inprocess.New() coreapi.Client` — v1 stub returning `true` (real store lookups added when the mailbox domain lands).
  - `worker.WarmupHandler(core coreapi.Client) func(ctx, *asynq.Task) error`
  - `worker.Register(mux *asynq.ServeMux, core coreapi.Client)`

- [ ] **Step 1: Write the failing warmup handler test**

`internal/worker/warmup/warmup_test.go`:
```go
package warmup

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hibiken/asynq"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

type fakeCore struct{ exists bool }

func (f fakeCore) MailboxExists(context.Context, string) (bool, error) { return f.exists, nil }

var _ coreapi.Client = fakeCore{}

func TestWarmupHandlerSkipsUnknownMailbox(t *testing.T) {
	h := Handler(fakeCore{exists: false})
	payload, _ := json.Marshal(queue.WarmupTickPayload{MailboxID: "missing"})
	task := asynq.NewTask(queue.TaskWarmupTick, payload)

	if err := h(context.Background(), task); err != nil {
		t.Fatalf("handler returned error for unknown mailbox: %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/worker/warmup/`
Expected: FAIL (`undefined: Handler` / `undefined: coreapi`).

- [ ] **Step 3: Define the coreapi seam**

`internal/coreapi/coreapi.go`:
```go
// Package coreapi is the control⇄execution boundary. Workers depend on this
// interface, never on platform/db directly. v1 satisfies it in-process; a
// future HTTP implementation swaps in without changing worker code.
package coreapi

import "context"

type Client interface {
	// MailboxExists reports whether a mailbox is present and active.
	MailboxExists(ctx context.Context, id string) (bool, error)
}
```

`internal/coreapi/inprocess/inprocess.go`:
```go
// Package inprocess is the v1 coreapi implementation: direct in-process access.
package inprocess

import (
	"context"

	"github.com/inroad/inroad/internal/coreapi"
)

type client struct{}

// New returns the in-process coreapi client. It is intentionally a stub until
// the mailbox domain exists; the interface it satisfies will not change.
func New() coreapi.Client { return client{} }

func (client) MailboxExists(_ context.Context, _ string) (bool, error) {
	return true, nil
}
```

- [ ] **Step 4: Implement the warmup handler + registration**

`internal/worker/warmup/warmup.go`:
```go
// Package warmup is the execution-plane ramp engine (v1: no-op pacing tick).
package warmup

import (
	"context"
	"encoding/json"

	"github.com/hibiken/asynq"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
)

// Handler returns an asynq handler for warmup:tick tasks.
func Handler(core coreapi.Client) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, t *asynq.Task) error {
		var p queue.WarmupTickPayload
		if err := json.Unmarshal(t.Payload(), &p); err != nil {
			return err
		}
		ok, err := core.MailboxExists(ctx, p.MailboxID)
		if err != nil {
			return err
		}
		if !ok {
			return nil // mailbox gone; nothing to pace
		}
		// v1: ramp logic is a no-op placeholder. Real pacing lands with the mailbox domain.
		return nil
	}
}
```

`internal/worker/handlers.go`:
```go
// Package worker wires execution-plane task handlers onto an asynq mux.
package worker

import (
	"github.com/hibiken/asynq"
	"github.com/inroad/inroad/internal/coreapi"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker/warmup"
)

// Register attaches all execution-plane handlers to the mux.
func Register(mux *asynq.ServeMux, core coreapi.Client) {
	mux.HandleFunc(queue.TaskWarmupTick, warmup.Handler(core))
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `go test ./internal/worker/... ./internal/coreapi/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/coreapi internal/worker
git commit -m "feat: add coreapi seam and worker warmup handler skeleton"
```

---

## Task 11: Entrypoints — API server, worker, migrate, seed binaries

**Files:**
- Create: `cmd/inroad/main.go`, `cmd/worker/main.go`, `cmd/migrate/main.go`, `cmd/seed/main.go`
- Delete: `cmd/.gitkeep`

**Interfaces:**
- Consumes: everything above (`config`, `log`, `db`, `httpx`, `queue`, `crypto`, `workspace`, `worker`, `coreapi/inprocess`).
- Produces: four runnable binaries. `cmd/inroad` mounts `workspace.Handler.Routes()` under `/api/v1`.

- [ ] **Step 1: Implement the migrate binary**

`cmd/migrate/main.go`:
```go
package main

import (
	"fmt"
	"os"

	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	cmd := "up"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	switch cmd {
	case "up":
		err = db.Migrate(cfg.DatabaseURL)
	case "down":
		err = db.MigrateDown(cfg.DatabaseURL)
	default:
		fmt.Fprintln(os.Stderr, "usage: migrate [up|down]")
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	fmt.Println("migrate", cmd, "ok")
}
```

- [ ] **Step 2: Implement the API server binary**

`cmd/inroad/main.go`:
```go
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/inroad/inroad/internal/app/workspace"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
	"github.com/inroad/inroad/internal/platform/httpx"
	"github.com/inroad/inroad/internal/platform/log"
)

func main() {
	cfg, err := config.Load()
	logger := log.New(cfgEnv(cfg))
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("db connect failed", "err", err)
		os.Exit(1)
	}
	defer pool.Close()

	queries := gen.New(pool)
	wsHandler := workspace.NewHandler(workspace.NewService(workspace.NewStore(queries)), cfg.JWTSecret)

	router := httpx.NewRouter(logger)
	router.Mount("/api/v1/workspaces", wsHandler.Routes())

	srv := httpx.NewServer(cfg.HTTPAddr, router)
	logger.Info("api listening", "addr", cfg.HTTPAddr)
	if err := httpx.Run(ctx, srv); err != nil {
		logger.Error("server error", "err", err)
		os.Exit(1)
	}
}

// cfgEnv tolerates a nil config so we can still build a logger for the error path.
func cfgEnv(cfg *config.Config) string {
	if cfg == nil {
		return "development"
	}
	return cfg.Env
}
```

- [ ] **Step 3: Implement the worker binary**

`cmd/worker/main.go`:
```go
package main

import (
	"os"

	"github.com/inroad/inroad/internal/coreapi/inprocess"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/log"
	"github.com/inroad/inroad/internal/platform/queue"
	"github.com/inroad/inroad/internal/worker"
)

func main() {
	cfg, err := config.Load()
	logger := log.New("development")
	if err != nil {
		logger.Error("config load failed", "err", err)
		os.Exit(1)
	}
	logger = log.New(cfg.Env)

	srv := queue.NewServer(cfg.RedisAddr, logger)
	mux := queue.NewMux()
	worker.Register(mux, inprocess.New())

	logger.Info("worker starting", "redis", cfg.RedisAddr)
	if err := srv.Run(mux); err != nil {
		logger.Error("worker error", "err", err)
		os.Exit(1)
	}
}
```

- [ ] **Step 4: Implement the seed binary**

`cmd/seed/main.go`:
```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/inroad/inroad/internal/app/workspace"
	"github.com/inroad/inroad/internal/platform/config"
	"github.com/inroad/inroad/internal/platform/db"
	"github.com/inroad/inroad/internal/platform/db/gen"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, "db:", err)
		os.Exit(1)
	}
	defer pool.Close()

	svc := workspace.NewService(workspace.NewStore(gen.New(pool)))
	res, err := svc.Register(ctx, workspace.RegisterInput{
		WorkspaceName: "Demo Workspace",
		Email:         "demo@inroad.test",
		Password:      "demodemo",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	fmt.Printf("seeded workspace=%s user=%s (login demo@inroad.test / demodemo)\n", res.WorkspaceID, res.UserID)
}
```

- [ ] **Step 5: Remove the placeholder + build all**

Run:
```bash
rm cmd/.gitkeep
go build ./...
```
Expected: all four binaries compile, exit 0.

- [ ] **Step 6: Smoke-test the API end to end**

Run (with `make db-up` already up):
```bash
make migrate-up
make run-api &     # starts on :8080
sleep 2
curl -s localhost:8080/healthz
curl -s -X POST localhost:8080/api/v1/workspaces/register \
  -H 'Content-Type: application/json' \
  -d '{"workspace_name":"Acme","email":"a@acme.test","password":"password1"}'
curl -s -X POST localhost:8080/api/v1/workspaces/login \
  -H 'Content-Type: application/json' \
  -d '{"email":"a@acme.test","password":"password1"}'
kill %1
```
Expected: `{"status":"ok"}`, then two `{"token":"...","workspace_id":"...","user_id":"..."}` responses.

- [ ] **Step 7: Commit**

```bash
git add cmd
git commit -m "feat: add api, worker, migrate, and seed entrypoints"
```

---

## Task 12: OpenAPI contract for the vertical slice

**Files:**
- Create: `api/openapi.yaml`

**Interfaces:**
- Consumes: the shape of Task 9's register/login endpoints.
- Produces: `api/openapi.yaml` — the source consumed by the frontend codegen in Task 13.

- [ ] **Step 1: Write the OpenAPI document**

`api/openapi.yaml`:
```yaml
openapi: 3.0.3
info:
  title: Inroad API
  version: 0.1.0
servers:
  - url: /api/v1
paths:
  /workspaces/register:
    post:
      operationId: register
      tags: [workspaces]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: '#/components/schemas/RegisterRequest' }
      responses:
        '200':
          description: Session token
          content:
            application/json:
              schema: { $ref: '#/components/schemas/TokenResponse' }
        '400': { description: Validation error }
  /workspaces/login:
    post:
      operationId: login
      tags: [workspaces]
      requestBody:
        required: true
        content:
          application/json:
            schema: { $ref: '#/components/schemas/LoginRequest' }
      responses:
        '200':
          description: Session token
          content:
            application/json:
              schema: { $ref: '#/components/schemas/TokenResponse' }
        '401': { description: Invalid credentials }
components:
  schemas:
    RegisterRequest:
      type: object
      required: [workspace_name, email, password]
      properties:
        workspace_name: { type: string }
        email: { type: string, format: email }
        password: { type: string, minLength: 8 }
    LoginRequest:
      type: object
      required: [email, password]
      properties:
        email: { type: string, format: email }
        password: { type: string }
    TokenResponse:
      type: object
      required: [token, workspace_id, user_id]
      properties:
        token: { type: string }
        workspace_id: { type: string }
        user_id: { type: string }
```

- [ ] **Step 2: Validate the document**

Run: `npx --yes @redocly/cli lint api/openapi.yaml`
Expected: no errors (warnings acceptable).

- [ ] **Step 3: Commit**

```bash
git add api/openapi.yaml
git commit -m "feat: add OpenAPI contract for register/login"
```

---

## Task 13: Web app scaffold — Vite, React 19, Tailwind v4, Redux, Router, codegen

**Files:**
- Create: `web/package.json`, `web/vite.config.ts`, `web/tsconfig.json`, `web/index.html`, `web/components.json`
- Create: `web/src/main.tsx`, `web/src/styles/globals.css`
- Create: `web/src/store/index.ts`, `web/src/store/emptyApi.ts`, `web/src/store/slices/ui.ts`
- Create: `web/src/routes/__root.tsx`, `web/src/routes/index.tsx`
- Create: `web/src/features/auth/api.ts`, `web/src/features/auth/LoginForm.tsx`, `web/src/features/auth/LoginForm.test.tsx`
- Create: `web/openapi-codegen.ts`, `web/vitest.config.ts`, `web/src/test/setup.ts`
- Delete: `web/.gitkeep`

**Interfaces:**
- Consumes: `api/openapi.yaml` (Task 12), the running API (Task 11).
- Produces: a `npm run dev` app with a login form calling the generated RTK Query `useLoginMutation`, Redux store with persisted `ui` slice.

- [ ] **Step 1: Scaffold the Vite app + install deps**

Run:
```bash
cd web
npm create vite@latest . -- --template react-ts   # accept overwrite of scaffold files
npm install
npm install @reduxjs/toolkit react-redux redux-persist \
  @tanstack/react-router \
  react-hook-form zod @hookform/resolvers
npm install -D tailwindcss @tailwindcss/vite \
  @tanstack/router-plugin \
  @rtk-query/codegen-openapi @redocly/cli \
  vitest @testing-library/react @testing-library/jest-dom jsdom
```
Expected: `web/package.json` and `web/node_modules` populated. React 19 is Vite's default.

- [ ] **Step 2: Configure Vite (Tailwind v4 + Router plugin)**

`web/vite.config.ts`:
```ts
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { TanStackRouterVite } from '@tanstack/router-plugin/vite'

export default defineConfig({
  plugins: [TanStackRouterVite(), react(), tailwindcss()],
  server: {
    proxy: { '/api': 'http://localhost:8080' },
  },
})
```

- [ ] **Step 3: Tailwind v4 CSS-first tokens**

`web/src/styles/globals.css`:
```css
@import "tailwindcss";

@theme {
  --color-brand-500: oklch(0.62 0.19 255);
  --font-sans: "Inter", ui-sans-serif, system-ui, sans-serif;
}

:root { color-scheme: light dark; }
body { @apply bg-white text-neutral-900 antialiased; }
```

- [ ] **Step 4: RTK Query codegen config + empty base API**

`web/openapi-codegen.ts`:
```ts
import type { ConfigFile } from '@rtk-query/codegen-openapi'

const config: ConfigFile = {
  schemaFile: '../api/openapi.yaml',
  apiFile: './src/store/emptyApi.ts',
  apiImport: 'emptyApi',
  outputFile: './src/store/api.ts',
  exportName: 'api',
  hooks: true,
}
export default config
```

`web/src/store/emptyApi.ts`:
```ts
import { createApi, fetchBaseQuery } from '@reduxjs/toolkit/query/react'

// The generated api.ts injects endpoints into this base. Never hand-edit api.ts.
export const emptyApi = createApi({
  reducerPath: 'api',
  baseQuery: fetchBaseQuery({ baseUrl: '/api/v1' }),
  endpoints: () => ({}),
})
```

- [ ] **Step 5: Generate the API slice**

Add to `web/package.json` scripts: `"gen:api": "rtk-query-codegen-openapi openapi-codegen.ts"`, then run:
```bash
npm run gen:api
```
Expected: creates `web/src/store/api.ts` exporting `api`, `useLoginMutation`, `useRegisterMutation`.

- [ ] **Step 6: UI slice + store with persist**

`web/src/store/slices/ui.ts`:
```ts
import { createSlice } from '@reduxjs/toolkit'

interface UiState { sidebarOpen: boolean }
const initialState: UiState = { sidebarOpen: true }

const uiSlice = createSlice({
  name: 'ui',
  initialState,
  reducers: {
    toggleSidebar: (s) => { s.sidebarOpen = !s.sidebarOpen },
  },
})

export const { toggleSidebar } = uiSlice.actions
export default uiSlice.reducer
```

`web/src/store/index.ts`:
```ts
import { configureStore, combineReducers } from '@reduxjs/toolkit'
import { persistReducer, persistStore } from 'redux-persist'
import storage from 'redux-persist/lib/storage'
import { api } from './api'
import ui from './slices/ui'

const rootReducer = combineReducers({
  [api.reducerPath]: api.reducer,
  ui,
})

// Persist UI slices ONLY. The RTK Query `api` cache must never be persisted.
const persistConfig = { key: 'inroad', storage, whitelist: ['ui'] }
const persisted = persistReducer(persistConfig, rootReducer)

export const store = configureStore({
  reducer: persisted,
  middleware: (getDefault) =>
    getDefault({ serializableCheck: { ignoredActions: ['persist/PERSIST', 'persist/REHYDRATE'] } })
      .concat(api.middleware),
})
export const persistor = persistStore(store)

export type RootState = ReturnType<typeof store.getState>
export type AppDispatch = typeof store.dispatch
```

- [ ] **Step 7: Auth feature endpoint tags + login form**

`web/src/features/auth/api.ts`:
```ts
// Re-export generated auth hooks so features import from their own folder.
export { useLoginMutation, useRegisterMutation } from '../../store/api'
```

`web/src/features/auth/LoginForm.tsx`:
```tsx
import { useForm } from 'react-hook-form'
import { zodResolver } from '@hookform/resolvers/zod'
import { z } from 'zod'
import { useLoginMutation } from './api'

const schema = z.object({
  email: z.string().email(),
  password: z.string().min(1),
})
type FormValues = z.infer<typeof schema>

export function LoginForm() {
  const { register, handleSubmit, formState: { errors } } = useForm<FormValues>({
    resolver: zodResolver(schema),
  })
  const [login, { isLoading, data }] = useLoginMutation()

  return (
    <form
      onSubmit={handleSubmit((v) => login({ loginRequest: v }))}
      className="mx-auto flex max-w-sm flex-col gap-3 p-6"
    >
      <input aria-label="email" placeholder="Email" {...register('email')} className="border p-2" />
      {errors.email && <span role="alert">Invalid email</span>}
      <input aria-label="password" type="password" placeholder="Password" {...register('password')} className="border p-2" />
      <button disabled={isLoading} className="bg-brand-500 p-2 text-white">Log in</button>
      {data && <p>Signed in as {data.user_id}</p>}
    </form>
  )
}
```

> Note: the generated mutation argument shape (`{ loginRequest: v }`) follows `@rtk-query/codegen-openapi`'s naming from the `LoginRequest` schema + `login` operationId. If codegen emits a different arg name, match it — the generated `api.ts` is the source of truth.

- [ ] **Step 8: Root route, index route, entrypoint**

`web/src/routes/__root.tsx`:
```tsx
import { createRootRoute, Outlet } from '@tanstack/react-router'

export const Route = createRootRoute({
  component: () => <Outlet />,
})
```

`web/src/routes/index.tsx`:
```tsx
import { createFileRoute } from '@tanstack/react-router'
import { LoginForm } from '../features/auth/LoginForm'

export const Route = createFileRoute('/')({
  component: () => <LoginForm />,
})
```

`web/src/main.tsx`:
```tsx
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { Provider } from 'react-redux'
import { PersistGate } from 'redux-persist/integration/react'
import { RouterProvider, createRouter } from '@tanstack/react-router'
import { store, persistor } from './store'
import { routeTree } from './routeTree.gen'
import './styles/globals.css'

const router = createRouter({ routeTree })
declare module '@tanstack/react-router' {
  interface Register { router: typeof router }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <Provider store={store}>
      <PersistGate loading={null} persistor={persistor}>
        <RouterProvider router={router} />
      </PersistGate>
    </Provider>
  </StrictMode>,
)
```

> `routeTree.gen.ts` is auto-generated by the TanStack Router Vite plugin on first `npm run dev`/build.

- [ ] **Step 9: Vitest config + setup + a component test**

`web/vitest.config.ts`:
```ts
import { defineConfig } from 'vitest/config'
import react from '@vitejs/plugin-react'

export default defineConfig({
  plugins: [react()],
  test: { environment: 'jsdom', setupFiles: ['./src/test/setup.ts'], globals: true },
})
```

`web/src/test/setup.ts`:
```ts
import '@testing-library/jest-dom'
```

`web/src/features/auth/LoginForm.test.tsx`:
```tsx
import { render, screen } from '@testing-library/react'
import { Provider } from 'react-redux'
import { store } from '../../store'
import { LoginForm } from './LoginForm'

test('renders email and password fields', () => {
  render(
    <Provider store={store}>
      <LoginForm />
    </Provider>,
  )
  expect(screen.getByLabelText('email')).toBeInTheDocument()
  expect(screen.getByLabelText('password')).toBeInTheDocument()
})
```

- [ ] **Step 10: Run the web build + tests**

Run:
```bash
cd web
npm run dev -- --host &   # generates routeTree.gen.ts; confirm it boots, then stop
sleep 3 && kill %1
npx vitest run
npm run build
```
Expected: dev server boots without error, Vitest test PASSES, `npm run build` produces `web/dist`.

- [ ] **Step 11: Commit**

```bash
cd ..
git add web
git commit -m "feat: scaffold React 19 web app with Redux, RTK Query codegen, and router"
```

---

## Task 14: Full-stack compose + Dockerfiles + docs

**Files:**
- Create: `deploy/docker/Dockerfile.api`, `deploy/docker/Dockerfile.worker`
- Create: `deploy/compose/docker-compose.yml`
- Create: `docker-compose.yml` (root wrapper)
- Create: `docs/self-hosting.md`

**Interfaces:**
- Consumes: all binaries (Task 11) + web build (Task 13).
- Produces: `docker compose up` bringing up postgres, redis, api, worker.

- [ ] **Step 1: API Dockerfile (multi-stage, builds web too)**

`deploy/docker/Dockerfile.api`:
```dockerfile
# --- web build ---
FROM node:22-alpine AS web
WORKDIR /web
COPY web/package*.json ./
RUN npm ci
COPY web/ ./
COPY api/ ../api/
RUN npm run gen:api && npm run build

# --- go build ---
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/inroad ./cmd/inroad
RUN CGO_ENABLED=0 go build -o /out/migrate ./cmd/migrate

# --- runtime ---
FROM alpine:3.20
RUN adduser -D -u 10001 inroad
COPY --from=build /out/inroad /usr/local/bin/inroad
COPY --from=build /out/migrate /usr/local/bin/migrate
COPY --from=web /web/dist /srv/web
USER inroad
EXPOSE 8080
ENTRYPOINT ["inroad"]
```

`deploy/docker/Dockerfile.worker`:
```dockerfile
FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/worker ./cmd/worker

FROM alpine:3.20
RUN adduser -D -u 10001 inroad
COPY --from=build /out/worker /usr/local/bin/worker
USER inroad
ENTRYPOINT ["worker"]
```

- [ ] **Step 2: Full-stack compose**

`deploy/compose/docker-compose.yml`:
```yaml
services:
  postgres:
    image: postgres:16-alpine
    environment:
      POSTGRES_USER: inroad
      POSTGRES_PASSWORD: inroad
      POSTGRES_DB: inroad
    volumes: ["pgdata:/var/lib/postgresql/data"]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U inroad"]
      interval: 3s
      timeout: 3s
      retries: 10
  redis:
    image: redis:7-alpine
  api:
    build:
      context: ../..
      dockerfile: deploy/docker/Dockerfile.api
    environment:
      INROAD_DATABASE_URL: postgres://inroad:inroad@postgres:5432/inroad?sslmode=disable
      INROAD_REDIS_ADDR: redis:6379
      INROAD_JWT_SECRET: dev-jwt-secret-dev-jwt-secret-32b
      INROAD_MASTER_KEY: MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=
    command: ["sh", "-c", "migrate up && inroad"]
    ports: ["8080:8080"]
    depends_on:
      postgres: { condition: service_healthy }
      redis: { condition: service_started }
  worker:
    build:
      context: ../..
      dockerfile: deploy/docker/Dockerfile.worker
    environment:
      INROAD_DATABASE_URL: postgres://inroad:inroad@postgres:5432/inroad?sslmode=disable
      INROAD_REDIS_ADDR: redis:6379
      INROAD_JWT_SECRET: dev-jwt-secret-dev-jwt-secret-32b
      INROAD_MASTER_KEY: MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=
    depends_on:
      redis: { condition: service_started }
volumes:
  pgdata:
```

- [ ] **Step 3: Root compose wrapper**

`docker-compose.yml` (repo root):
```yaml
# Convenience wrapper so `docker compose up` works from the repo root.
include:
  - deploy/compose/docker-compose.yml
```

- [ ] **Step 4: Self-hosting doc**

`docs/self-hosting.md`:
```markdown
# Self-Hosting Inroad

## Requirements
- Docker + Docker Compose

## Run
    cp .env.example .env
    docker compose up --build

The API (with the built web UI) serves on http://localhost:8080. Migrations run
automatically on the api container's startup. The worker connects to Redis.

## Production notes
- Set strong INROAD_JWT_SECRET and INROAD_MASTER_KEY (see .env.example for generation).
- Put a TLS-terminating reverse proxy in front of :8080.
- For worker fleets across multiple IPs, run the worker binary under systemd
  (templates in deploy/systemd/) rather than compose.
```

- [ ] **Step 5: Validate compose + build images**

Run:
```bash
docker compose config           # validates the merged root + include file
docker compose build            # builds api + worker images
```
Expected: config prints merged YAML with no error; both images build.

- [ ] **Step 6: Full-stack smoke test**

Run:
```bash
docker compose up -d
sleep 15
curl -s localhost:8080/healthz
curl -s -X POST localhost:8080/api/v1/workspaces/register \
  -H 'Content-Type: application/json' \
  -d '{"workspace_name":"Compose","email":"c@x.test","password":"password1"}'
docker compose down
```
Expected: `{"status":"ok"}` then a token response.

- [ ] **Step 7: Commit**

```bash
git add deploy docker-compose.yml docs/self-hosting.md
git commit -m "feat: add full-stack docker-compose, Dockerfiles, and self-hosting docs"
```

---

## Self-Review Notes (author checklist — completed)

- **Spec coverage:** Every §4 backend layer (cmd, app, platform, worker, coreapi) has a task; §5 frontend stack (React 19, Vite 7, Tailwind v4, RTK/RTK Query/persist, TanStack Router, RHF/zod, Vitest) all appear in Task 13; §6 deployment (compose, Dockerfiles) in Task 14; layering + frontend rules encoded in Global Constraints; the plane seam (coreapi in-process → HTTP-ready) in Task 10; envelope encryption in Task 5. Deferred items (pooled warmup, HTTP coreapi, tracking binary, admin app, Astro site, license) intentionally out of scope per §8 — no tasks, by design.
- **Not yet exercised end-to-end in this scaffold (acknowledged, not gaps):** WebSocket (`platform/ws`) and the tracking package are deferred to their feature plans; they have package slots but no scaffold task, matching the vertical-slice scope.
- **Type consistency:** `Config` fields, `gen.New`, `workspace.New{Store,Service,Handler}`, `coreapi.Client.MailboxExists`, `queue.WarmupTickPayload`/`TaskWarmupTick`, and `auth.IssueToken`/`ParseToken`/`Claims` are used identically across producing and consuming tasks.
- **Placeholder scan:** No TBD/TODO left in steps; every code step shows complete code.

---

## Post-Scaffold: Next Plans (not part of this plan)

Following PRD Phase 0 order, each becomes its own spec→plan cycle on top of this skeleton:
1. **Mailbox domain** — Gmail/M365 OAuth + SMTP/IMAP connect, encrypted creds (uses `crypto.Sealer`), caps/ramp; promotes `coreapi` to real store lookups.
2. **Contact + list + CSV import.**
3. **Campaign + sequence builder** (backend + the `features/sequences` UI).
4. **Send engine + tracking** (open pixel, ticket redirects) + reply/bounce polling.
5. **Analytics dashboards + WebSocket realtime.**
