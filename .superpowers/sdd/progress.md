# Inroad Scaffold — Progress Ledger

Branch: `scaffold`
Plan: `docs/superpowers/plans/2026-07-19-inroad-repository-scaffold.md`

## Approach
Parallel-agent execution (user-directed). Controller lays shared foundation
(go.mod frozen via tools_anchor.go, DB layer, sqlc gen, api/openapi.yaml);
agents implement disjoint dirs, do NOT touch go.mod or git; controller commits.

## Toolchain
Go/sqlc/migrate installed but NOT on default PATH. Every Bash go command must
prefix: `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`.

## Naming convention (user-set)
Frontend files = kebab-case (login-form.tsx, empty-api.ts). Go backend = lowercase
Go convention (underscore only for _test.go / build tags). Recorded in CLAUDE.md.

## Status
- Foundation: complete (commit b6fa13d) — skeleton, go.mod+go.sum frozen, DB layer, sqlc gen, api spec
- Task 1 (skeleton/tooling): complete (controller, b6fa13d)
- Task 3 (db layer + migrations + sqlc): complete (controller, b6fa13d)
- Task 12 (openapi.yaml): complete (controller, b6fa13d)
- CLAUDE.md: complete (commit 9184844)
- Wave 1 Go (config/log, httpx, crypto, queue, auth): complete (commit 115189c, all tests pass)
- Task 13 web: built & passing (codegen/vitest/build all PASS); kebab-case rename in progress
- Wave 2: complete (commit a788482) — workspace, coreapi, worker; all tests pass
- Task 11 (cmd entrypoints): complete (commit 683c6ab, controller)
- Task 14 (deploy+docs): complete (commit bf486ac)
- Integration gate: PASS — migrate + workspace integration test vs real Postgres (host 5433);
  e2e API smoke test (healthz/register/login 200, wrong-pw 401) through running server
- Module: anchor removed, `go mod tidy` clean; full build + `go test ./...` + vet + gofmt all clean
- SCAFFOLD COMPLETE on branch `scaffold` (8 commits b6fa13d..bf486ac). Pending: merge to main.

## Known minor items (for a future pass, non-blocking)
- cmd/migrate + cmd/seed call config.Load() which requires JWT_SECRET + MASTER_KEY
  even though they only need DATABASE_URL. Consider a lighter config for those.
- cmd/worker/main.go builds a throwaway logger then rebuilds it after config load.
- Generated files committed (web/src/store/api.ts, routeTree.gen.ts) — intentional for
  buildability; revisit if noisy in diffs.
- Machine note: native postgresql-x64-18 holds host :5432; dev DB uses :5433.

## Core workflow — mailbox connect (branch core-workflow)
- Apache 2.0 LICENSE+NOTICE; scaffold merged to main.
- Standard: idiomatic identifiers, kebab files, snake_case at boundaries, domain repo interfaces (DIP). In CLAUDE.md.
- mailboxes schema + sqlc; platform/mail (SMTP/IMAP tester) with SSRF guard.
- coreapi now DB-backed (real MailboxExists); worker still imports no db.
- mailbox domain (Store iface, connect+test+seal, auth-scoped routes); OpenAPI + bearer.
- SSRF guard: block loopback/link-local(metadata)/multicast always; private via INROAD_MAIL_ALLOW_PRIVATE_HOSTS (default true); mail-port allowlist; dial resolved IP.
- .gitattributes LF; line endings normalized; gofmt clean.
- VERIFIED e2e: 401 no-token, 200 [] scoped list, 422 SSRF metadata block, 422 conn-test reject.
- Next: connect SUCCESS path (real mail server / greenmail), Gmail/M365 OAuth, then contacts.
