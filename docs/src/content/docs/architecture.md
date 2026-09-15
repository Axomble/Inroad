---
title: System Architecture & Planes
description: Deep dive into Inroad control plane, execution plane, coreapi boundary, and system design.
---

Inroad separates operations into two distinct planes: the **Control Plane** and the **Execution Plane**.

:::note
This page describes the system's **shape**. For the **rules** that govern how it
is built — and the reasoning behind each, so a change that must break one can
weigh what it gives up — see [Architecture Principles](/architecture-principles/).
:::

## Planes & Isolation

- **Control Plane (`cmd/inroad`):** Hosts the HTTP REST API server, identity/authentication services, workspace configurations, webhooks, and database management. It directly manages PostgreSQL and Redis.
- **Execution Plane (`cmd/worker`):** Contains background engines responsible for sending campaign emails, inbox polling, deliverability evaluation, and mailbox warmup.
- **CoreAPI Boundary (`internal/coreapi`):** The worker *packages* reach relational data and unseal encrypted mailbox credentials only through `internal/coreapi` — one seam, so the execution plane can move to its own host without touching worker code. That much is enforced mechanically: a `depguard` rule in `.golangci.yml` fails the build if a non-test file under `internal/worker/` imports `internal/platform/db`. The *process* boundary is still partial. The worker opens its own `pgxpool` and resolves `coreapi` to an in-process function call rather than a network hop, so a compromised worker host still reads the tenant database. What it no longer holds is the KEY: a `role=send` worker builds no `crypto.Keyring`, refuses to start if it is given `INROAD_MASTER_KEY`, and obtains each mailbox credential from the control plane over an authenticated channel (`internal/platform/credbroker`) — so the ciphertext it can read, it cannot decrypt. The single-process self-host topology (`role=all`) keeps its local keyring and is unchanged. The boundary becomes a full one — a worker host that cannot read the tenant database at all — when `coreapi` gains its remote transport (HTTP/gRPC) and the worker gives up its pool. That part is designed, not built.

## System Monorepo Layout

```
Inroad Monorepo
├── cmd/                          --> Binary entrypoints (inroad, worker, migrate, seed)
├── internal/
│   ├── app/                      --> 25 Feature domain slices (auth, campaign, contact, warmup, etc.)
│   ├── platform/                 --> 26 Infra packages (crypto, db, mail, queue, bus, storage)
│   ├── worker/                   --> Execution handlers (sender, sequence, inbox, warmup)
│   └── coreapi/                  --> Control <-> Execution seam interface
├── api/openapi.yaml              --> OpenAPI REST API contract
├── docs/                         --> Standalone Astro Starlight Documentation App
└── web/                          --> React 19 + Vite + Tailwind v4 + RTK Query SPA
```

## Technology Stack

- **Backend:** Go 1.25 · Chi Router · `pgx/v5` · `sqlc` · `golang-migrate` · `asynq` · AES-256-GCM Envelope Cryptography.
- **Frontend:** React 19 · Vite · Tailwind v4 · Redux Toolkit / RTK Query · TanStack Router.
- **MCP Server:** `@modelcontextprotocol/go-sdk` v1.7.0 · OAuth 2.1 Authorization Server (`/oauth2`).
