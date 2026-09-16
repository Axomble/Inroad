---
title: Single-Instance Docker Compose Self-Hosting
description: Zero-configuration single-command self-hosting guide for Inroad using Docker Compose.
---

Inroad provides a canonical, single-command Docker Compose setup suitable for self-hosting on any single VPS, EC2 instance, or dedicated server. The manifest is the `docker-compose.yml` at the repository root — the production compose file for anyone who has cloned the repo (it **builds** the images locally). If you'd rather not clone at all, `scripts/install.sh` (`curl | sh`) pulls the equivalent published-image manifest, `deploy/docker/docker-compose.prod.yml`, instead — see that script's own `--help` for the environment overrides.

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

## Splitting the worker into control and send roles

The manifest ships **one** `worker` service, and that stays the recommended
default: it needs no role at all, and it is what a fresh `docker compose up`
runs. If you are splitting the worker across hosts, `INROAD_WORKER_ROLE` divides
it into a `control` host (scheduler plus the periodic sweeps, beside the API) and
one or more `send` hosts (per-message work only — sends, warmup, inbox polls,
webhooks). Queue consumption follows the role, so the topology is operable; read
[splitting control and send roles](/deploy/environment-variables/#splitting-control-and-send-roles)
first, because the *order* you roll it out in matters and one of the wrong orders
fails silently.

:::caution[A `send` worker must not receive `INROAD_MASTER_KEY`]
`role=send` **refuses to start** if it is given the master key, and also refuses
to start without a credential broker to ask instead. Both checks run at startup.

That makes the shared `appsecrets` volume a trap: `load-secrets.sh` sources
`SECRETS_FILE` and exports `INROAD_MASTER_KEY` from it whenever the variable is
not already set, so a `send` service that mounts that volume inherits the key and
dies on boot. A `send` host therefore mounts **no** secrets volume and is given
`INROAD_JWT_SECRET` explicitly, plus the broker URL and token. The `api` service
grows `INROAD_FLEET_BROKER_ADDR` to serve the broker on a separate listener.

See [credential brokering](/deploy/environment-variables/#credential-brokering-send-role)
for what this does and does not contain.
:::

Add these to your own compose file and remove the stock `worker` service (or
scale it to 0), so the same work is not registered twice:

```yaml
  worker-control:
    build:
      context: .
      dockerfile: deploy/docker/Dockerfile.worker
    environment:
      INROAD_DATABASE_URL: ${INROAD_DATABASE_URL:-postgres://${POSTGRES_USER:-inroad}:${POSTGRES_PASSWORD:-inroad}@postgres:5432/${POSTGRES_DB:-inroad}?sslmode=disable}
      INROAD_REDIS_ADDR: ${INROAD_REDIS_ADDR:-redis:6379}
      INROAD_JWT_SECRET: ${INROAD_JWT_SECRET:-}
      INROAD_MASTER_KEY: ${INROAD_MASTER_KEY:-}
      SECRETS_FILE: /run/secrets/inroad/env
      INROAD_ENV: ${INROAD_ENV:-production}
      INROAD_LOG_LEVEL: ${INROAD_LOG_LEVEL:-info}
      INROAD_WORKER_ROLE: control
    volumes:
      - appsecrets:/run/secrets/inroad:ro
    restart: unless-stopped
    depends_on:
      postgres: { condition: service_healthy }
      redis: { condition: service_healthy }
      migrate: { condition: service_completed_successfully }

  worker-send:
    build:
      context: .
      dockerfile: deploy/docker/Dockerfile.worker
    environment:
      INROAD_DATABASE_URL: ${INROAD_DATABASE_URL:-postgres://${POSTGRES_USER:-inroad}:${POSTGRES_PASSWORD:-inroad}@postgres:5432/${POSTGRES_DB:-inroad}?sslmode=disable}
      INROAD_REDIS_ADDR: ${INROAD_REDIS_ADDR:-redis:6379}
      # Explicit, NOT from the appsecrets volume: sourcing that file would also
      # export INROAD_MASTER_KEY, which this role refuses to start with.
      INROAD_JWT_SECRET: ${INROAD_JWT_SECRET:?set this on the send host}
      INROAD_ENV: ${INROAD_ENV:-production}
      INROAD_LOG_LEVEL: ${INROAD_LOG_LEVEL:-info}
      INROAD_WORKER_ROLE: send
      # Pin the id. It defaults to the container hostname, which changes on every
      # recreate — and a warmup tick already routed to the old w:<id> queue then
      # sits on a queue nothing consumes, with no dead-letter row to show for it.
      INROAD_WORKER_ID: send-1
      # This host's own public address, so its sends and polls authenticate to
      # each provider from a stable source IP.
      INROAD_WORKER_EGRESS_IP: ${SEND_1_EGRESS_IP:-}
      # No master key. Every credential is opened by the control plane, one
      # mailbox at a time — which is also why no Google/Microsoft client
      # credentials are needed here: the refresh happens where the key is.
      INROAD_FLEET_BROKER_URL: ${INROAD_FLEET_BROKER_URL:?e.g. https://api:8090}
      INROAD_FLEET_BROKER_TOKEN: ${INROAD_FLEET_BROKER_TOKEN:?openssl rand -base64 32}
    restart: unless-stopped
    depends_on:
      postgres: { condition: service_healthy }
      redis: { condition: service_healthy }
      migrate: { condition: service_completed_successfully }
```

The `api` service needs two additions to answer those brokered requests — a
listener address **separate** from `INROAD_HTTP_ADDR`, bound where only the fleet
can reach it, and the same shared token:

```yaml
  api:
    environment:
      INROAD_FLEET_BROKER_ADDR: ${INROAD_FLEET_BROKER_ADDR:-0.0.0.0:8090}
      INROAD_FLEET_BROKER_TOKEN: ${INROAD_FLEET_BROKER_TOKEN:?openssl rand -base64 32}
```

The broker is never mounted on the public API router, because its responses *are*
credentials. It is `https`-only unless `INROAD_FLEET_BROKER_ALLOW_PLAINTEXT` is
set explicitly — the channel carries both the bearer token and the decrypted
credential, so plaintext has to be chosen, never defaulted into.

`worker-control` keeps `INROAD_MASTER_KEY` above and that is fine: `role=control`
registers no handler that opens a credential, and the startup guard only refuses
a role that holds the key *and* is configured to broker.

For more than one send host, copy `worker-send` and give each copy its own
`INROAD_WORKER_ID` — do **not** `--scale` a single service past one replica.
Every replica would inherit the same pinned id, share one `w:<id>` affinity
queue and one row in the worker registry, which is precisely the per-IP
guarantee that queue exists to provide.

## Local Development

Development does not use the production manifest. The dev stack lives at `docker-compose.dev.yml` and bind-mounts the source tree:

```bash
docker compose -f docker-compose.dev.yml up
```

It runs Go with `air` hot-reloading, the SPA under Vite HMR on `:5173`, Mailpit catching all transactional email on `:8025`, the Astro docs site on `:4321`, and creates the `inroad_test` database for integration tests. Dev secrets are deliberately hardcoded in that file; it must never serve anything internet-reachable.
