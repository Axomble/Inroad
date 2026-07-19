# Inroad

Self-hostable cold email sequencing + mailbox warmup platform (open-core alternative to Instantly/Smartlead). Go backend + workers, React SPA. Single monorepo, single Go module.

## Architecture

- **Control plane:** API server (`cmd/inroad`) + Postgres + Redis.
- **Execution plane:** worker (`cmd/worker`) — reaches relational data & decrypted credentials ONLY through `internal/coreapi` (in-process now, HTTP later), never Postgres directly.
- **Stack:** Go 1.25 · chi · pgx/v5 · sqlc · golang-migrate · asynq · JWT · AES-GCM. Frontend: React 19 · Vite · Tailwind v4 · Redux Toolkit / RTK Query / redux-persist · TanStack Router · shadcn/Radix.

## Layout

- `cmd/` — thin binary entrypoints (`inroad`, `worker`, `migrate`, `seed`)
- `internal/app/<domain>/` — feature slices (auth, workspace, …); each owns its data access (`store.go`)
- `internal/platform/` — cross-cutting infra (config, log, db, httpx, queue, crypto)
- `internal/worker/` — execution-plane engines
- `internal/coreapi/` — control⇄execution seam
- `web/` — React SPA; `web/src/features/<domain>/` mirrors backend domains
- `db` layer at `internal/platform/db/` (migrations + queries + generated `gen/`)
- `api/openapi.yaml` — REST contract; frontend types are generated from it

## Conventions

- **File names — kebab-case on the frontend, lowercase on the Go backend. No camelCase/PascalCase file names anywhere.**
  - **Frontend (TS/TSX):** kebab-case, e.g. `login-form.tsx`, `empty-api.ts`, `openapi-codegen.ts`.
  - **Go backend:** Go-idiomatic lowercase — single word (`store.go`, `password.go`); underscore ONLY where the language forces it (`_test.go`, build-constraint suffixes like `_linux.go`). Hyphens are not used in Go files (the toolchain reserves underscores for build constraints).
  - Identifiers always follow the language: Go exported = PascalCase, React components = PascalCase (`export function LoginForm`), TS vars = camelCase. Only the *file name* changes.
  - Tool-mandated exceptions (leave as-is): `__root.tsx` (router), `docker-compose*.yml`, `*.sql.go` (sqlc), `go.mod`, `tsconfig*.json`, `vite.config.ts`.
- **Identifiers:** language-idiomatic. Go = `MixedCaps` (exported `PascalCase`, local `camelCase`). TS/React = `camelCase` vars/functions, `PascalCase` components & types. snake_case is used ONLY at boundaries — JSON API fields, DB columns, env vars. Never snake_case Go/TS identifiers.
- **Architecture: SOLID + pragmatic Clean.** Each domain defines its own repository interface (e.g. `mailbox.Store`); services depend on the interface, not the concrete sqlc-backed struct (dependency inversion, trivially unit-testable). Keep interfaces small and at seams (`coreapi.Client`, `mail.ConnectionTester`). No full entity/DTO duplication — sqlc models are the persistence type; the interface boundary is where the decoupling lives.
- **Backend layering:** `app/*` may import `platform/*`, never the reverse; `app/*` packages don't import each other; workers use `coreapi` only; routes registered per-domain via `Routes() http.Handler`.
- **Frontend:** `routes/*` compose from `features/*`; `features/*` never import each other; redux-persist whitelists UI slices only (never the RTK Query `api` reducer); `store/api.ts` is generated, never hand-edited.
- **Secrets:** never commit; `.env` is gitignored, `.env.example` holds placeholders.
- **Commits:** conventional (`feat:`, `chore:`, `test:`, `docs:`).

## Dev

    cp .env.example .env
    make db-up && make migrate-up
    make run-api        # :8080
    make run-worker
    cd web && npm install && npm run dev

Tests: `make test` (unit) · `make test-integration` (needs `make db-up`).

## More docs
- `docs/security.md` — security invariants that must never be broken (read before touching creds, outbound dials, or tenant queries).
- `docs/architecture.md` — architecture notes. `docs/self-hosting.md` — deploy guide.
- `docs/superpowers/specs/` and `docs/superpowers/plans/` — design specs and implementation plans.

## Environment note (this machine)

Go/sqlc/migrate are installed but NOT on the default shell PATH, and shell state doesn't persist between commands. Prefix Go commands with:

    export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
