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
- **Branches:** prefix by type — `feature/…`, `fix/…`, `chore/…`. Never commit feature work directly to `main`; branch, then merge.

## Code quality

Language-agnostic rules for writing code in this repo — apply to every language, not just Go/TS. Enforced by `make lint` (golangci-lint · oxlint · strict `tsc`); keep it green.

- **Strict by default.** Type-check in strict mode (TS `strict` + `noUncheckedIndexedAccess`; Go `go vet` + golangci-lint). No new lint suppressions without a specific, explained directive (`//nolint:rule // why`, `// oxlint-disable-next-line rule -- why`) — never a blanket disable.
- **Never swallow errors.** Handle a returned error / rejected promise or propagate it with context — never discard it. Inspect errors through the one shared seam per stack (Go `errors.Is/As` + `%w` wrapping; web `@/lib/rtk-error`), never ad-hoc casts or `catch {}`. Fail loud over failing silent.
- **No duplication (DRY).** `dupl` flags copy-paste. Give shared logic one home once a pattern recurs — but prefer a little duplication over the *wrong* abstraction; don't abstract on the second occurrence.
- **One source of truth for types.** Derive from the generated/owning definition; never hand-copy a shape that already exists (re-export a generated type, don't re-declare it). Make illegal states unrepresentable; no `any`/`interface{}` escape hatches.
- **Small, single-purpose units.** One responsibility per function/file; guard-clause early returns over deep nesting; few parameters (pass an options struct/object past ~3). If a file is too big to hold in your head, split it by responsibility.
- **Name for intent.** Intention-revealing, searchable names; one consistent term per concept; no throwaway abbreviations. (Identifier casing stays language-idiomatic — see Conventions.)
- **Delete, don't comment out.** No dead code, unused exports/vars, or commented-out blocks — git remembers. YAGNI: don't build for imagined futures.
- **Comments say why, not what.** Explain intent and tradeoffs the code can't; keep them true when the code changes.
- **Isolate side effects.** Prefer pure functions; inject dependencies rather than reaching for hidden global mutable state (no package-level singletons/module globals holding state).
- **Validate at boundaries.** Trust nothing crossing a boundary (HTTP, DB, env, user input); keep interfaces small and at the seam.
- **Tests assert behavior, not implementation.** Cover the error / empty / edge branches, not just the happy path; a test that mocks everything asserts nothing.

## Dev

**One command, no local toolchain** (no Go, Node, or make needed):

    docker compose -f deploy/compose/docker-compose.dev.yml up

Brings up Postgres, Redis, migrations, api (:8080), worker, and the SPA (:5173).
Go rebuilds via `air` and the SPA hot-reloads via Vite — both watch the
bind-mounted source tree, so edits on the host apply live. Ctrl+C stops
everything; add `-d` to detach. Dev secrets are baked into that compose file, so
`.env` is not required. Integration tests can run against this stack too — the
`inroad_test` database is created on first boot.

Native alternative (needs Go + Node on PATH, and `make`, which is not installed
by default on Windows):

    cp .env.example .env
    make dev            # services + migrations + api + worker + web

Note the Go binaries do **not** read `.env` themselves — running them directly
means exporting it first (`set -a && . ./.env && set +a`).

Tests: `make test` (unit) · `make test-integration` (needs the DB up).
Lint: `make lint` (Go + web) · `make lint-go` · `make lint-web`. Needs `golangci-lint` on PATH (`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest`).

## More docs
- `docs/security.md` — security invariants that must never be broken (read before touching creds, outbound dials, or tenant queries).
- `docs/architecture.md` — architecture notes. `docs/self-hosting.md` — deploy guide.
- `docs/superpowers/specs/` and `docs/superpowers/plans/` — design specs and implementation plans.

## Environment note (this machine)

Go/sqlc/migrate are installed but NOT on the default shell PATH, and shell state doesn't persist between commands. Prefix Go commands with:

    export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
