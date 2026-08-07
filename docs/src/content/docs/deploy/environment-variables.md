---
title: Environment Variables Reference
description: Complete reference guide for all backend configuration environment variables.
---

## Required Configuration

| Variable | Description | Example / Default |
| :--- | :--- | :--- |
| `INROAD_DATABASE_URL` | PostgreSQL connection URL | `postgres://inroad:inroad@postgres:5432/inroad` |
| `INROAD_REDIS_ADDR` | Redis host & port | `redis:6379` |
| `INROAD_JWT_SECRET` | 32-byte secret for JWT signing | `openssl rand -base64 32` |
| `INROAD_MASTER_KEY` | Base64 encoded 32-byte master key | `base64 of 32 random bytes` |
| `INROAD_PUBLIC_URL` | Canonical public HTTP/HTTPS URL | `http://localhost:5173` |

## Optional & Security Overrides

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_LOG_LEVEL` | Logging level (`debug`, `info`, `warn`, `error`) | `info` |
| `INROAD_MAIL_ALLOW_PRIVATE_HOSTS` | Allow loopback/private RFC1918 mail server dials | `false` |
| `INROAD_KEY_PROVIDER` | Key Encryption Key provider — only `local` is implemented today (an AWS KMS backend exists behind the same seam but is not yet selectable) | `local` |

## Transactional Email

System-originated email: address verification, password reset, passwordless
login codes, and workspace invites. Separate from campaign mailboxes — this is
the operator's own sending identity.

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_TRANSACTIONAL_DRIVER` | `console` (logs recipient + subject, delivers nothing) or `smtp` | `console` |
| `INROAD_SYSTEM_SMTP_HOST` | System mailbox SMTP host (required for `smtp`) | — |
| `INROAD_SYSTEM_SMTP_PORT` | System mailbox SMTP port | `587` |
| `INROAD_SYSTEM_SMTP_USERNAME` | SMTP username; blank means no authentication is attempted | — |
| `INROAD_SYSTEM_SMTP_PASSWORD` | SMTP password | — |
| `INROAD_SYSTEM_EMAIL_FROM` | From address (required for `smtp`) | — |
| `INROAD_SYSTEM_SMTP_ALLOW_PLAINTEXT` | **Dev only.** Send over cleartext instead of requiring TLS | `false` |
| `INROAD_APP_BASE_URL` | Frontend origin that emailed links point at | `http://localhost:5173` |

The `console` driver never logs message bodies, because they contain single-use
bearer credentials (verify/reset links, login codes). To read a real message in
development, use a mail catcher — the dev compose stack runs Mailpit and serves
the caught mail at `http://localhost:8025`.

`INROAD_SYSTEM_SMTP_ALLOW_PLAINTEXT` exists only so a local catcher (plaintext,
no AUTH) can be reached. TLS is mandatory unless it is set to an explicit
`true`/`1`/`yes`; unset, empty, or misspelled all keep TLS on, so a
configuration mistake cannot downgrade delivery to cleartext. Do not set it in
production.

## Worker Tuning

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_WORKER_CONCURRENCY` | Number of concurrent asynq worker goroutines per worker process | `10` |

The default of `10` is sized for small deployments. Every per-mailbox send and
inbox-poll task shares this pool, so with many active mailboxes the queue backs
up behind it and sending looks slow even though nothing is wrong. As a rule of
thumb, raise it to at least `25` once you run **50 or more active mailboxes**
on one worker node.

When raising concurrency, keep the API server's database pool larger than
worker concurrency plus headroom for the periodic sweepers and HTTP handlers —
the pool already enforces a floor of 25 connections, sized to exceed the
default concurrency of 10.
