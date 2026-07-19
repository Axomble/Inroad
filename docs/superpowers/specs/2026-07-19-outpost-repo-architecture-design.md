# Outpost — Repository & Folder Architecture Design

**Date:** 2026-07-19
**Status:** Approved design, pre-implementation
**Source PRD:** `PRD.md` (Draft v1)
**Informed by:** structural analysis of `warmbly/warmbly` (Go/Rust/Elixir/React/Swift polyglot monorepo)

---

## 1. Purpose & Scope

Defines the complete file/folder infrastructure for Outpost, a self-hostable cold email sequencing + mailbox warmup platform (open-core competitor to Instantly/Smartlead, structural successor to Warmbly). This spec covers repository layout, layering rules, and the frontend/backend library set. It does **not** cover feature implementation details — those follow in per-feature plans.

## 2. Locked Decisions

| Decision | Choice | Rationale |
|---|---|---|
| Repo shape | **Single monorepo, single license** | Solo-builder velocity; Core/Cloud physical split (`ee/` dir or split repos) deferred until Cloud work begins. License choice (Apache vs AGPL) remains open per PRD §14.3 — must be decided before public launch. |
| Backend | **Go end-to-end** (API + workers) | PRD principle #2 ("one stack, deeply understood"). Rejects Warmbly's 5-runtime sprawl (Go+Rust+Elixir+TS+Swift). |
| Data access | **pgx/v5 + sqlc** (hand-written SQL, generated type-safe Go) + **squirrel** escape hatch for runtime-dynamic queries (dynamic segments) | Deliverability tooling is query-heavy (pacing loops, reputation windows, funnel aggregations) — SQL must stay visible and tunable. ORM rejected. |
| Queue | **Redis + asynq** | Replaces Warmbly's Kafka+Zookeeper+Schema Registry. Redis-persisted job state satisfies PRD §11 restart-survival. |
| Migrations | **golang-migrate**, embedded via `go:embed`, applied on boot | Migrations are the single schema source of truth (no vestigial `schema.sql`). |
| Frontend | **One React app** (`web/`), role-gated lazy-loaded admin section | Separate admin app (Warmbly-style) deferred to Cloud-era back-office needs. |
| Realtime | **gorilla/websocket in the Go API** | No separate realtime service at v1 scale (PRD §8). |
| Tracking | **Routes in the Go API** (`internal/app/tracking` package) | Promotable to its own binary later without rewrite. Server-side ticket redirects (`/c/<uuid>`), never `?url=` params (no open-redirect surface — pattern adopted from Warmbly). |
| Plane split | **`coreapi` seam from day one**: workers never touch Postgres; v1 uses an in-process implementation, v2 swaps in HTTP | Warmbly's best architectural idea, adopted at interface level now, paid for only when worker fleets scale out. |
| Docs site | **Deferred to Phase 1 launch** (Astro + Starlight under `site/`) | Repo-root markdown `docs/` suffices for dogfood phase. |

## 3. What We Copy From Warmbly vs. Reject

**Copy (patterns):** `cmd/` thin entrypoints; feature-sliced `internal/app/<domain>` packages; embedded numbered migrations; full-stack docker-compose with mail mocks; control/execution plane boundary; envelope encryption (per-tenant DEKs); server-side tracking tickets.

**Reject (implementations):** Rust tracking service; Elixir realtime service; Kafka event bus; separate admin app; iOS app; 78 KB monolithic `routes.go` (we register routes per-domain); 31 KB Makefile; central `internal/repository/` with ~96 `pg_*.go` files (we put data access inside each domain slice).

## 4. Repository Tree

```
outpost/
├── cmd/                          # Thin main.go per deployable binary (~30 lines each)
│   ├── outpost/                  #   API server: HTTP + WebSocket + tracking routes
│   ├── worker/                   #   Execution plane: send/poll/warmup host
│   ├── migrate/                  #   Migration runner (up/down/version)
│   └── seed/                     #   Dev/demo data seeder
│
├── internal/
│   ├── app/                      # FEATURE SLICES — one package per domain
│   │   ├── auth/                 #   register/login, JWT sessions, API keys
│   │   ├── workspace/            #   tenants, members, roles, invites
│   │   ├── mailbox/              #   Gmail/M365 OAuth + SMTP/IMAP, caps, ramp state
│   │   ├── contact/              #   contacts, CSV import, custom fields (JSONB)
│   │   ├── list/                 #   static lists + dynamic segments
│   │   ├── suppression/          #   global do-not-contact, unsubscribe handling
│   │   ├── campaign/             #   campaigns, mailbox assignment, caps
│   │   ├── sequence/             #   steps, delays, branching, spintax, personalization
│   │   ├── send/                 #   send records, scheduling, pacing decisions
│   │   ├── tracking/             #   open pixel + click redirect (ticket-based)
│   │   ├── deliverability/       #   bounce/reply parsing, reputation, auto-pause
│   │   ├── warmup/               #   ramp engine (v1); pooled-warmup seam (v2)
│   │   ├── analytics/            #   funnel + mailbox dashboards, CSV export
│   │   ├── webhook/              #   outbound event webhooks
│   │   └── audit/                #   audit log
│   │
│   ├── platform/                 # CROSS-CUTTING INFRA — no business logic
│   │   ├── config/               #   env loading → typed config struct
│   │   ├── db/
│   │   │   ├── db.go             #     pgx pool setup
│   │   │   ├── migrate.go        #     //go:embed migrations/*.sql
│   │   │   ├── migrations/       #     000001_init.up.sql / .down.sql …
│   │   │   ├── queries/          #     sqlc SOURCE .sql (hand-written)
│   │   │   ├── gen/              #     sqlc GENERATED Go (never hand-edited)
│   │   │   └── sqlc.yaml
│   │   ├── queue/                #   asynq client/server wrappers, task registry
│   │   ├── crypto/               #   envelope encryption (master key → per-tenant DEKs)
│   │   ├── mail/                 #   SMTP send, IMAP poll, OAuth token refresh
│   │   ├── httpx/                #   server bootstrap, router, middleware, error mapping
│   │   ├── ws/                   #   gorilla/websocket hub
│   │   ├── log/                  #   structured JSON logging
│   │   └── validate/             #   request validation helpers
│   │
│   ├── worker/                   # EXECUTION PLANE (asynq handlers → engines)
│   │   ├── sender/               #   SMTP send worker
│   │   ├── poller/               #   IMAP reply/bounce poller
│   │   ├── warmup/               #   ramp pacing engine
│   │   └── handlers.go           #   asynq task handler registration
│   │
│   └── coreapi/                  # CONTROL ⇄ EXECUTION SEAM
│       ├── coreapi.go            #   interface: data + decrypted-credential access
│       ├── inprocess/            #   v1: direct service calls
│       └── http/                 #   v2: /api/v1/internal/* client + server
│
├── web/                          # React SPA (see §5)
│
├── deploy/
│   ├── docker/                   # Per-binary Dockerfiles (outpost, worker)
│   ├── compose/                  # docker-compose.yml + docker-compose.dev.yml
│   └── systemd/                  # worker unit templates for prod fleets
│
├── api/                          # openapi.yaml — public REST API source of truth
├── docs/                         # markdown: architecture, self-hosting, warmup honesty
│   └── superpowers/specs/        #   design docs (this file)
├── scripts/                      # dev helpers, install-worker.sh
│
├── go.mod / go.sum
├── Makefile                      # dev, build, sqlc-gen, migrate, test targets
├── docker-compose.yml            # thin wrapper → deploy/compose (root `up` UX)
├── .env.example
├── LICENSE
└── README.md
```

### Backend layering rules (enforced, non-negotiable)

1. `app/*` may import `platform/*`; **never** the reverse.
2. `app/*` packages do not import each other — cross-domain coordination goes through interfaces or the queue.
3. Workers (`internal/worker/*`) access relational data and decrypted credentials **only** through `coreapi` — never `platform/db` directly.
4. Each domain owns its data access (`app/<domain>/store.go` calling sqlc-generated queries) — no central repository layer.
5. Route registration is per-domain (each `app/<domain>` exposes a `Routes()` mount) — no monolithic route file.
6. Migrations, sqlc queries, and generated code live together under `platform/db/` because `go:embed` cannot reference parent directories.

## 5. Frontend (`web/`)

**Stack (latest stable, July 2026):** React 19 · Vite 7 · TypeScript · TanStack Router (file-based, type-safe) · **Redux Toolkit 2.x + RTK Query + react-redux v9** · **redux-persist** · shadcn/ui (Radix primitives) · Tailwind CSS v4 (CSS-first `@theme` config — no `tailwind.config.ts`) · react-hook-form + zod v4 · Playwright (e2e) · Vitest (unit).

```
web/
├── src/
│   ├── main.tsx                  # React 19 root: Redux Provider, PersistGate, Router
│   ├── routes/                   # TanStack Router file-based route tree
│   │   ├── __root.tsx            #   shell: providers, error boundary, toaster
│   │   ├── (auth)/               #   public: login, register, forgot-password
│   │   ├── (onboarding)/         #   first-run: workspace + mailbox connect
│   │   ├── _app/                 #   authed layout: sidebar, workspace switcher, WS
│   │   │   ├── dashboard.tsx
│   │   │   ├── campaigns/        #   list, detail, sequence builder
│   │   │   ├── contacts/         #   lists, segments, CSV import wizard
│   │   │   ├── mailboxes/        #   connect flows, health, ramp progress
│   │   │   ├── inbox/            #   replies view
│   │   │   ├── analytics/
│   │   │   └── settings/         #   workspace, members, API keys, webhooks
│   │   └── _admin/               #   role-gated, lazy-loaded admin section
│   │       ├── route.tsx         #     guard: owner/admin only
│   │       ├── users.tsx
│   │       ├── audit-log.tsx
│   │       └── system.tsx        #     instance health, queue depth, worker status
│   │
│   ├── features/                 # Domain UI+logic, mirrors internal/app 1:1
│   │   ├── campaigns/            #   api.ts (injectEndpoints), hooks, components/
│   │   ├── sequences/            #   sequence builder (largest feature)
│   │   ├── contacts/
│   │   ├── mailboxes/
│   │   ├── deliverability/
│   │   ├── analytics/
│   │   ├── auth/
│   │   └── workspace/
│   │
│   ├── components/
│   │   ├── ui/                   # shadcn primitives (CLI-managed, vendored source)
│   │   ├── layout/               # sidebar, topbar, page shells
│   │   └── shared/               # composed cross-feature (data-table, empty-state)
│   │
│   ├── store/
│   │   ├── index.ts              # configureStore + redux-persist setup
│   │   ├── api.ts                # RTK Query base API slice (GENERATED from openapi.yaml
│   │   │                         #   via @rtk-query/codegen-openapi; features extend it
│   │   │                         #   with injectEndpoints)
│   │   └── slices/               # ui.ts, drafts.ts, preferences.ts
│   │
│   ├── lib/
│   │   ├── ws.ts                 # WebSocket client → dispatches invalidateTags
│   │   └── utils.ts              # cn(), formatters
│   ├── hooks/                    # generic hooks only
│   └── styles/
│       └── globals.css           # Tailwind v4 @theme tokens — THE design config
│
├── e2e/                          # Playwright smoke tests
├── index.html
├── package.json
├── vite.config.ts                # @tailwindcss/vite + TanStack Router plugins
├── tsconfig.json
├── components.json               # shadcn CLI config
└── .env.example
```

### Frontend rules

1. `routes/` files are thin — they compose from `features/`.
2. `features/*` may import `components/`, `store/`, `lib/` — never each other.
3. **redux-persist whitelists UI slices only** (`ui`, `drafts`, `preferences`). The RTK Query `api` reducer is explicitly blacklisted — persisting server cache rehydrates stale data and fights invalidation.
4. `store/api.ts` is regenerated from `api/openapi.yaml` (npm script) — API types are generated, never hand-written, so Go and TS cannot drift.
5. `components/ui/` is shadcn-CLI-managed; composed components live in `shared/` or feature folders.
6. Realtime: WebSocket events map to RTK Query `invalidateTags` (and `onCacheEntryAdded` streaming for the inbox view).

## 6. Deployment Shape

- **Self-hosters:** `docker compose up` from repo root → postgres:16, redis:7, `outpost` (API+static web), `worker`, mailpit (SMTP mock, dev compose only). Dev compose adds an IMAP test server (greenmail or dovecot).
- **Prod worker fleets (Cloud-era):** single worker binary + systemd unit templates in `deploy/systemd/`, installed via `scripts/install-worker.sh`.
- The production API binary serves the built `web/` assets (single-container default; separable later).

## 7. Testing Layout

- Go: table-driven unit tests beside code (`_test.go`); integration tests against dockerized Postgres/Redis under `internal/**` with a `//go:build integration` tag; `make test` / `make test-integration`.
- Web: Vitest for unit/component tests beside source; Playwright e2e in `web/e2e/` (login → create campaign → send via mailpit smoke path).

## 8. Explicitly Deferred (with pre-cut seams)

| Deferred item | Seam already in place |
|---|---|
| Core/Cloud (`ee/`) physical split | Monorepo domains are isolated packages; Cloud-only domains (billing, pooled warmup) will be added as new `app/` packages behind build tags when Cloud work starts |
| Pooled warmup | `app/warmup/` package boundary |
| Worker fleets over HTTP | `coreapi/http/` implementation slot |
| Dedicated tracking binary | `app/tracking/` package → future `cmd/tracking/` |
| Astro docs/marketing site | future `site/` top-level dir |
| Separate admin app | `_admin` route section → future `admin/` app |
| License choice (Apache vs AGPL) | Must be decided before Phase 1 public launch (PRD §14.3) |

## 9. Scaffolding Order (input to implementation plan)

1. Repo init: go.mod, Makefile, compose skeleton, `.env.example`, README
2. `platform/`: config → log → db (pool + migrate + sqlc wiring) → httpx
3. First vertical slice: `app/auth` + `app/workspace` (proves the layering end-to-end)
4. `web/` scaffold: Vite + Tailwind v4 + shadcn + router + store + codegen pipeline
5. Queue + worker skeleton + `coreapi` interface with in-process impl
6. Then per-feature plans follow PRD Phase 0 order: mailbox → sequence → send → tracking
