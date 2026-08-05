---
title: System Architecture & Planes
description: Deep dive into Inroad control plane, execution plane, coreapi boundary, and system design.
---

Inroad separates operations into two distinct planes: the **Control Plane** and the **Execution Plane**.

## Planes & Isolation

- **Control Plane (`cmd/inroad`):** Hosts the HTTP REST API server, identity/authentication services, workspace configurations, webhooks, and database management. It directly manages PostgreSQL and Redis.
- **Execution Plane (`cmd/worker`):** Contains background engines responsible for sending campaign emails, inbox polling, deliverability evaluation, and mailbox warmup.
- **CoreAPI Boundary (`internal/coreapi`):** The execution worker **never accesses PostgreSQL directly**. It reaches relational data and unseals encrypted mailbox credentials strictly through `internal/coreapi` (in-process now, HTTP/gRPC remote transport later).

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
