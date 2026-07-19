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

- **File names: snake_case everywhere** — Go (`user_store.go`) and TS/TSX (`login_form.tsx`, `empty_api.ts`). No camelCase/PascalCase/kebab file names. Identifiers still follow each language (Go exported = PascalCase, React components = PascalCase, TS vars = camelCase) — only the *file name* is snake_case. Tool-mandated exceptions: `__root.tsx` (router), `docker-compose*.yml`, `*.sql.go` (sqlc), `go.mod`.
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

## Environment note (this machine)

Go/sqlc/migrate are installed but NOT on the default shell PATH, and shell state doesn't persist between commands. Prefix Go commands with:

    export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
