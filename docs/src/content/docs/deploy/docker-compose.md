---
title: Single-Instance Docker Compose Self-Hosting
description: Zero-configuration single-command self-hosting guide for Inroad using Docker Compose.
---

Inroad provides a canonical, single-command Docker Compose setup suitable for self-hosting on any single VPS, EC2 instance, or dedicated server. The manifest is the `docker-compose.yml` at the repository root — the only production compose file.

## Quick Start (Zero-Config)

Clone the repository and run:

```bash
git clone https://github.com/Axomble/Inroad && cd Inroad

# Boots secret generation, PostgreSQL, Redis, auto-migrations, API, Worker, and Web SPA
docker compose up -d
```

Open `http://localhost` (or your server's IP) in your browser.

## Service Composition

The root `docker-compose.yml` includes 7 services:

- `init-secrets`: One-shot service that generates a real random `INROAD_JWT_SECRET` and `INROAD_MASTER_KEY` into a Docker volume on first boot, so a bare `docker compose up` never runs on fixed, publicly-known secrets. Set both explicitly in the environment (or a `.env` file) to override — required for any multi-host deployment, since the generated file lives on a volume local to this host.
- `postgres`: PostgreSQL 16 database.
- `redis`: Redis 7 in-memory queue & cache.
- `migrate`: Automatic schema migration service; API and Worker wait for it to complete.
- `api`: Control plane REST API server (`cmd/inroad`).
- `worker`: Execution plane background worker (`cmd/worker`).
- `web`: Nginx container hosting the React SPA on port 80 and reverse proxying `/api/` calls to the API server.

## Configuration

Compose auto-loads a `.env` file next to the manifest; every `INROAD_*` variable can be set there or in the shell. Notable ones:

- `INROAD_PUBLIC_URL`: The externally reachable URL — OAuth redirect URLs derive from it.
- `INROAD_GOOGLE_CLIENT_ID` / `INROAD_GOOGLE_CLIENT_SECRET`: Gmail mailbox connect (blank keeps the provider disabled). `INROAD_GOOGLE_SIGNIN_*` optionally splits "Continue with Google" onto a scope-light client.
- `INROAD_MS_CLIENT_ID` / `INROAD_MS_CLIENT_SECRET`: Microsoft 365 mailbox connect.

See the [environment variables reference](/deploy/environment-variables/) for the full list.

> **Caution:** a `.env` written for native development typically points `INROAD_DATABASE_URL` at a host port (e.g. `localhost:5433`). Compose passes it into the containers, where `localhost` is the container itself — leave it unset (or blank) when running the compose stack so the in-network `postgres:5432` default applies.

## Local Development

Development does not use the production manifest. The dev stack lives at `deploy/compose/docker-compose.dev.yml` and bind-mounts the source tree:

```bash
docker compose -f deploy/compose/docker-compose.dev.yml up
```

It runs Go with `air` hot-reloading, the SPA under Vite HMR on `:5173`, Mailpit catching all transactional email on `:8025`, the Astro docs site on `:4321`, and creates the `inroad_test` database for integration tests. Dev secrets are deliberately hardcoded in that file; it must never serve anything internet-reachable.
