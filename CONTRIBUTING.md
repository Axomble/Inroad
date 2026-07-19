# Contributing to Inroad

## Prerequisites
- Go 1.25+, Docker, Node 22+ (for `web/`), and `sqlc` (`go install github.com/sqlc-dev/sqlc/cmd/sqlc@latest`).
- Read `CLAUDE.md` (conventions) and `docs/security.md` (invariants) first.

## Dev loop
`make` is optional (raw commands shown on the right):
```
cp .env.example .env            # fill in secrets (openssl rand -base64 32)
make db-up        # docker compose -f deploy/compose/docker-compose.dev.yml up -d   (Postgres :5433 + Redis)
make migrate-up   # go run ./cmd/migrate up
make run-api      # go run ./cmd/inroad          (API on :8080)
make run-worker   # go run ./cmd/worker          (separate shell)
```
Frontend: `cd web && npm install && npm run dev`.

## Tests
```
make test                       # unit tests (no external services)
make test-integration           # integration tests (needs make db-up)
```
Equivalent raw commands if you don't use make: `go test ./...` and
`go test -tags=integration ./...`. Frontend: `cd web && npx vitest run`.

> If `go`/`sqlc` aren't on PATH (Windows), prefix commands with:
> `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`.

## Conventions (summary — full list in CLAUDE.md)
- File names: kebab-case (frontend), lowercase (Go). Identifiers: language-idiomatic
  (Go `MixedCaps`, TS `camelCase`/`PascalCase`). snake_case only at boundaries
  (JSON, DB columns, env vars).
- Layering: `app/*` → `platform/*`, never reverse; `app/*` packages don't import
  each other; workers reach data only via `coreapi`.
- Commits: conventional (`feat:`, `fix:`, `chore:`, `docs:`, `test:`).

## Recipe: add a new domain

The `internal/app/mailbox/` domain is the reference implementation. To add a
domain `X` (e.g. `contact`), follow the same shape:

1. **Migration** — `internal/platform/db/migrations/0000N_x.up.sql` (+ `.down.sql`).
   Every tenant table carries `workspace_id UUID NOT NULL REFERENCES workspaces(id)`.
2. **Queries** — `internal/platform/db/queries/x.sql`. Scope reads/writes by
   `workspace_id`. Run `sqlc generate` (or `make sqlc`) to regenerate `gen/`.
3. **Store (DIP)** — `internal/app/x/store.go`: define a `Store` *interface* the
   domain owns (clean arg lists), plus a `PgStore` that implements it by wrapping
   `*gen.Queries`. The service depends on the interface, never on `gen` directly.
4. **Service** — `internal/app/x/service.go`: `Service` depends on `Store` and any
   platform interfaces it needs (`mail.ConnectionTester`, `*crypto.Sealer`, …).
   Define sentinel errors (`ErrNotFound`, `ErrValidation`, …). Seal any secrets;
   never store or return them in plaintext (see `docs/security.md`).
5. **Handler + routes** — `handler.go` + `routes.go`: a chi router with
   `auth.RequireAuth(jwtSecret)` on all routes. Get `workspaceID` from
   `auth.UserFromContext` (never the request body). Response DTOs must omit any
   secret field by construction. Map sentinels to status codes (404/409/422/400).
6. **Tests** — unit tests with a fake `Store` + fake platform interfaces (no DB/net);
   an integration test tagged `//go:build integration` for the real-DB path.
7. **Wire** — in `cmd/inroad/main.go`: construct the service/handler and
   `router.Mount("/api/v1/x", handler.Routes())`.
8. **Contract** — add the endpoints + schemas to `api/openapi.yaml`; regenerate the
   frontend client with `cd web && npm run gen:api`.

### Definition of done for a domain
- [ ] `go build ./...` and `go vet ./...` clean; `gofmt -l` empty.
- [ ] Unit tests pass; integration test passes against `make db-up`.
- [ ] No secret fields in any response DTO; outbound dials use the SSRF guard.
- [ ] All queries scoped by `workspace_id`.
- [ ] OpenAPI updated; `npm run gen:api` regenerates cleanly.
