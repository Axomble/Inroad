# Inroad

Self-hostable cold email sequencing + mailbox warmup platform (open-core).

## Quick start (dev)

    cp .env.example .env        # then fill in secrets
    make db-up                  # start Postgres + Redis
    make migrate-up             # apply migrations
    make run-api                # start the API server on :8080
    make run-worker             # (separate shell) start the worker

Web app:

    cd web && npm install && npm run dev

## Layout

- `cmd/`        thin binary entrypoints
- `internal/app/`       feature-sliced domains
- `internal/platform/`  cross-cutting infra
- `internal/worker/`    execution plane
- `internal/coreapi/`   control⇄execution seam
- `web/`        React SPA
- `deploy/`     Docker + compose + systemd
- `docs/`       architecture + self-hosting guides
