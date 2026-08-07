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

### Reading transactional email locally
The full dev stack (`docker compose -f deploy/compose/docker-compose.dev.yml up`)
runs Mailpit and points the API at it, so verification, password-reset, login-code,
and invite emails are delivered and readable at **http://localhost:8025** — click
the link straight out of the message. Nothing leaves the machine.

Running the API natively instead? The default `console` driver only logs the
recipient and subject; message bodies are never logged, because they carry
single-use links and login codes. To see a real one, point the API at a catcher:
```
INROAD_TRANSACTIONAL_DRIVER=smtp INROAD_SYSTEM_SMTP_HOST=localhost \
INROAD_SYSTEM_SMTP_PORT=1025 INROAD_SYSTEM_SMTP_ALLOW_PLAINTEXT=true \
INROAD_SYSTEM_EMAIL_FROM=no-reply@inroad.test go run ./cmd/inroad
```
`INROAD_SYSTEM_SMTP_ALLOW_PLAINTEXT` is dev-only and defaults to false — TLS is
mandatory unless it is explicitly set (see invariant 6). Never set it in production.

## Tests
```
make test                       # unit tests (no external services)
make test-integration           # integration tests (needs make db-up)
```
Equivalent raw commands if you don't use make: `go test ./...` and
`go test -tags=integration ./...`. Frontend: `cd web && npx vitest run`.

> If `go`/`sqlc` aren't on PATH (Windows), prefix commands with:
> `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`.

### Tests that pass but assert nothing

**The tell: a test that passes when you expected it to fail.** That is the only
reliable signal, so when you add a check, first break it on purpose and watch the
test go red. A test that was already green cannot tell you whether it covers the
thing you just wrote — and review will not catch this, because the test reads
correctly. All three examples below were found this way and none was visible on the
page.

Three shapes recur:

**A fixture that builds rows by hand skips the state machine, and hides every check
on the state it skipped.** Several fixtures created `sequence_enrollments` with a
direct `INSERT` (or `q.EnrollListMembers`), neither of which sets
`campaigns.status = 'running'` — only the campaign store's `EnrollTx` does, in the
same transaction as the enrollment. So the whole suite exercised the send path
against a `draft` campaign, a state production cannot produce, and a missing
campaign-status gate stayed invisible in four tests across three packages. If a
fixture writes a table that a service method normally owns, it has opted out of
that method's invariants: either go through the service, or set the state by hand
*and say why in a comment*.

**A concrete dependency that tests satisfy with `nil` makes every branch behind it
unreachable.** `sender.Handler` took a `*queue.Client`; every unit test passed
`nil`, so both of its deferral branches were untestable and one had been that way
since it was written. Depend on a small consumer-defined interface (`Enqueuer`,
`Store`, `Sender`) so a fake can assert the branch — the same dependency-inversion
rule the domains follow, applied to workers.

**A fixture and its assertion reading two different clocks makes the result depend
on how fast the machine ran.** A perf fixture laid 220,000 rows out relative to an
instant captured before seeding, then evaluated through a service calling
`time.Now()`; the ~35s of seeding drifted a 7-day window boundary across rows, and
the test failed once and passed twice. Inject the clock (`Service.now`) and pin it
to the instant the fixture seeded against, then assert the sample exactly rather
than approximately, so drift fails loudly instead of intermittently. Anywhere a
time-windowed query is tested, the fixture and the code under test have to agree on
what "now" is.

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
- [ ] Every new check was watched failing before it was watched passing (see
      "Tests that pass but assert nothing").
- [ ] No secret fields in any response DTO; outbound dials use the SSRF guard.
- [ ] All queries scoped by `workspace_id`.
- [ ] OpenAPI updated; `npm run gen:api` regenerates cleanly.
