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
- **Frontend:** `routes/*` compose from `features/*`; `features/*` never import each other's UI. One deliberate exception: a feature may import *read-only RTK Query hooks* from another feature's `api.ts` (hooks only, never components/state) — mark the import with a comment. redux-persist whitelists UI slices only (never the RTK Query `api` reducer); `store/api.ts` is generated, never hand-edited.
- **Sharing frontend UI — two homes, and neither is another feature.** When a second feature needs the same UI, hoist it by what the thing *is*:
  - **No domain knowledge and no fetching → `components/shared/`.** The record-page shell (`components/shared/record-page.tsx`) is the reference: panels, field rows, loading/empty lines, truncation notices, the inline retry alert. It takes a finished `message` string rather than an RTK error precisely so it needs no feature's error copy. `components/shared/no-feature-imports.test.ts` enforces the rule — nothing in that directory may import `@/features/*`.
  - **Fetches, but belongs to no one domain → a neutral feature.** `features/records/` holds what every *record type* shares: notes, tasks, the activity feed, `useOpenTasks`, the actor badge, `recordErrorMessage`, and the notes/tasks/activity cache tags. The API models these polymorphically (`note_targets`/`task_targets` carry a nullable contact/company/deal id), so one implementation serves contacts, companies and deals. `contacts`, `crm` and any future record-owning domain may import it; it imports none of them.
  - **Genuinely one domain's concept → leave it there** and restructure the caller. A deal row lives in `features/crm/` because a deal is a CRM record type (stages, pipelines, the `/app/deals` route); the contact page renders its own row over the contacts API's own shape rather than dragging "deal" into a neutral module. A little duplication beats a shared module that knows about everything.
  - Error copy stays per-feature (`crmErrorMessage`, `recordErrorMessage`, `agentErrorMessage`), because the scope a 403 names differs by domain. A domain-specific mapper should handle only the statuses where naming its domain helps, and delegate the rest.
- **Migrations are TIMESTAMPED, not sequential.** New files are
  `YYYYMMDDHHMMSS_snake_name.{up,down}.sql` — e.g. `20260827143000_add_thing.up.sql`.
  Get the version with `date -u +%Y%m%d%H%M%S`.
  Sequential numbering (`000001`–`000071`) is frozen: those are recorded by version in
  deployed `schema_migrations`, so renaming one strands every installation that ran it.
  Never add a new `NNNNNN_` file — a test refuses it.
  The reason is not style. Every branch picked `max+1` at branch time and merge order
  decided who was right, so two valid PRs collided in the union and golang-migrate then
  refused to initialise AT ALL — taking down every migration, every database-backed
  test and every fresh deploy. That happened five times, twice in one day, and once to
  a renumbering fix that collided in turn. A guard running on a branch cannot see what
  another open PR is about to claim; a timestamp does not need to.
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
Lint: `make lint` (Go + web) · `make lint-go` · `make lint-web`. Needs the PINNED `golangci-lint` on PATH — `make lint-go` refuses a different one, because a
different version is a different rule set and would not match CI. The version lives in the
Makefile (`GOLANGCI_VERSION`), which CI reads too:
`go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2`.

## More docs

The `docs/` directory is an Astro/Starlight docs site; content lives under `docs/src/content/docs/`.

- `docs/src/content/docs/security.md` — security invariants that must never be broken (read before touching creds, outbound dials, or tenant queries).
- `docs/src/content/docs/architecture.md` — architecture notes. `docs/src/content/docs/deploy/` — self-hosting/deploy guides (compose, Terraform, Helm, env vars).
- `docs/superpowers/specs/` and `docs/superpowers/plans/` — design specs and implementation plans.

## Environment note (this machine)

Go/sqlc/migrate are installed but NOT on the default shell PATH, and shell state doesn't persist between commands. Prefix Go commands with:

    export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
