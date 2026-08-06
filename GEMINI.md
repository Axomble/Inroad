# Antigravity / Gemini Agent Guidelines: Inroad

Self-hostable cold email sequencing and mailbox warmup platform (open-core alternative to Instantly/Smartlead).
Go 1.25 backend + workers, React 19 SPA. Single monorepo, single Go module.

---

## 1. System Architecture & Seams

- **Control plane:** API server (`cmd/inroad`) + Postgres + Redis.
- **Execution plane:** Worker processes (`cmd/worker`) — reaches relational data & decrypted credentials **ONLY** through `internal/coreapi` (in-process now, HTTP/gRPC later), never Postgres directly.
- **Stack:** 
  - **Backend:** Go 1.25 · Chi router · `pgx/v5` · `sqlc` · `golang-migrate` · `asynq` · JWT · AES-256-GCM envelope encryption.
  - **Frontend:** React 19 · Vite · Tailwind v4 · Redux Toolkit / RTK Query / redux-persist · TanStack Router · Radix / UI primitives.

---

## 2. Directory Layout & Module Structure

```
Inroad (Monorepo Root)
├── cmd/                          --> Thin binary entrypoints (inroad, worker, migrate, seed)
├── internal/
│   ├── app/                      --> Feature domain slices (auth, campaign, contact, warmup, etc.)
│   ├── platform/                 --> Cross-cutting infrastructure (crypto, db, mail, queue, bus, etc.)
│   ├── worker/                   --> Execution-plane handlers (sender, sequence, inbox, warmup)
│   └── coreapi/                  --> Control <-> Execution seam interface
├── api/openapi.yaml              --> REST API OpenAPI contract (single source of truth for frontend types)
└── web/                          --> React SPA (`web/src/features/<domain>/` mirrors backend domains)
```

---

## 3. Core Security & Architectural Invariants

Always observe these mandatory security invariants (detailed in `docs/security.md`):

1. **Context-Derived Multi-Tenancy:**
   - Every tenant-scoped DB query MUST filter by `workspace_id` from the verified JWT context (`auth.UserFromContext`), NEVER trusted from request params or body.
   - Composite DB foreign keys (e.g. `campaign_senders(campaign_id, workspace_id)`) enforce isolation at the DB schema level.
2. **Two-Level Envelope Cryptography (DEK / KEK):**
   - Mailbox passwords & OAuth tokens are encrypted using AES-256-GCM under per-workspace random 32-byte DEKs (`crypto.Sealer`).
   - DEKs are wrapped using `crypto.KeyProvider` (`LocalKeyProvider` using HKDF-SHA256 of `INROAD_MASTER_KEY`).
   - AES-GCM AAD (`ws:<uuid>`) binds both the ciphertext and wrapped DEK to the workspace ID.
   - Workspace deletion cascades `workspace_deks`, providing instant crypto-shredding for GDPR compliance.
3. **Outbound SSRF Protection (`mail.vetAddr`):**
   - User-supplied mail server hosts dial through `mail.vetAddr` (blocks loopback, link-local `169.254.169.254`, multicast, and private RFC1918 ranges unless `INROAD_MAIL_ALLOW_PRIVATE_HOSTS=true`).
   - Dials the resolved IP directly while retaining host string for TLS ServerName verification to eliminate DNS rebinding risks.
4. **Claim-Before-Send Idempotency:**
   - Worker send steps execute an atomic DB claim (`queued` → `sending` with lease timestamp) BEFORE network SMTP calls to eliminate duplicate delivery on retries.
5. **Pure Offline Reply Classification:**
   - Reply classifier (`internal/platform/replyclassify`) runs deterministic offline header/keyword checks. Compliance unsubscribes trigger workspace-scoped suppression (`ON CONFLICT DO NOTHING`), while OOO auto-replies keep sequence enrollments active.
6. **Isolated Warmup Engine:**
   - Warmup mail uses constant-time HMAC header verification (`X-Inroad-Warmup`). Inbox poller intercepts and isolates warmup messages before campaign processing.

---

## 4. Coding & Naming Conventions

- **File Naming:**
  - **Frontend (TS/TSX):** `kebab-case` only (e.g. `login-form.tsx`, `use-auth.ts`).
  - **Go Backend:** Go-idiomatic lowercase single words (`store.go`, `password.go`). Underscores ONLY for test/build suffixes (`_test.go`, `_linux.go`). No hyphens in Go filenames.
  - No PascalCase or camelCase filenames anywhere in the repository.
- **Identifiers:**
  - Go: `MixedCaps` (exported `PascalCase`, unexported `camelCase`).
  - TS/React: `camelCase` variables/functions, `PascalCase` components & types.
  - `snake_case`: Used ONLY at boundaries (JSON fields, DB columns, env vars).
- **Architecture:** SOLID + pragmatic Clean Architecture. Each domain defines its own repository interface (e.g. `mailbox.Store`). Services depend on interfaces, not concrete structs. `sqlc` models serve directly as persistence types.

---

## 5. Development & Verification Commands

### Verification & Linting
- **Go Unit Tests:** `go test ./...`
- **Go Linter:** `golangci-lint run`
- **Web Typecheck:** `npm run typecheck` (run inside `web/` directory)
- **Web Linter:** `npm run lint` (run inside `web/` directory via `oxlint`)

### Local Environment Note (Windows)
Ensure Go, Node, and linter binaries are on PATH. If needed in bash/PowerShell, ensure PATH includes Go binaries:
```bash
export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"
```
