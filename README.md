<div align="center">

<pre>
██╗ ███╗   ██╗ ██████╗   ██████╗   █████╗  ██████╗ 
██║ ████╗  ██║ ██╔══██╗ ██╔═══██╗ ██╔══██╗ ██╔══██╗
██║ ██╔██╗ ██║ ██████╔╝ ██║   ██║ ███████║ ██║  ██║
██║ ██║╚██╗██║ ██╔══██╗ ██║   ██║ ██╔══██║ ██║  ██║
██║ ██║ ╚████║ ██║  ██║ ╚██████╔╝ ██║  ██║ ██████╔╝
╚═╝ ╚═╝  ╚═══╝ ╚═╝  ╚═╝  ╚═════╝  ╚═╝  ╚═╝ ╚═════╝ 
</pre>

**The self-hostable cold email sequencing and mailbox warm-up platform.**

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)](go.mod)
[![React](https://img.shields.io/badge/React-19-61DAFB?logo=react&logoColor=black)](web/package.json)
[![Postgres](https://img.shields.io/badge/Postgres-16-336791?logo=postgresql&logoColor=white)](deploy/compose/docker-compose.yml)
[![Self-hosted](https://img.shields.io/badge/self--hosted-docker%20compose-2496ED?logo=docker&logoColor=white)](docs/self-hosting.md)

[Features](#features) · [How it works](#how-it-works) · [Quick start](#quick-start) · [Self-hosting](#self-hosting) · [Docs](#documentation) · [Security](#security) · [Contributing](#contributing)

⭐ **If Inroad saves you a per-seat subscription, star the repo** — it's the cheapest way to help.

</div>

---

Inroad sends cold email sequences from the mailboxes you already own — Gmail, Microsoft 365, or plain
SMTP — and paces them on a warm-up ramp so they keep landing in the inbox. Replies, bounces, and
opt-outs are polled back in, classified, and used to stop the sequence automatically. Everything runs
on your own hardware: one Postgres, one Redis, two Go binaries, and a React SPA. No SaaS account, no
per-seat pricing, no third party holding your mailbox credentials.

It's an open-core alternative to Instantly and Smartlead, built for people who would rather run the
infrastructure than rent it.

<div align="center">
  <img src="docs/images/login.png" alt="Inroad sign-in" width="100%">
</div>

---

## Features

| | |
|---|---|
| **Multi-step sequences** | Ordered steps with per-step delays, drag-to-reorder, and `{{first_name}}` / `{{company}}` merge fields. Structural edits are draft-only; copy edits stay live on a running campaign. |
| **Three transports, one seam** | Gmail API, Microsoft Graph, and SMTP/IMAP behind a single `MultiSender`. The worker doesn't know or care which one it's using. |
| **Mailbox warm-up pool** | Mailboxes you enable exchange real threaded mail with each other on a ramping daily volume — recipient-side, Inroad rescues the message from spam, marks it read, and sometimes replies in-thread. Per-mailbox health (`healthy` / `watch` / `at risk`) is recomputed from measured inbox-vs-spam placement, and a mailbox that goes bad is paused instead of pushed. |
| **Ramp + daily caps on campaigns** | Every mailbox ramps linearly from a small starting cap to its full daily cap over N days. The cap is enforced on the send path — over-cap enrollments defer and retry, and a permanently mis-set cap fails out instead of looping forever. |
| **Reply detection & classification** | IMAP / Gmail history / Graph delta polling matches replies to the original send and classifies them — positive, negative, neutral, auto-reply, out-of-office, unsubscribe — with zero AI dependency and no network calls. |
| **Out-of-office trap fix** | A vacation auto-responder gets tagged but does *not* halt the sequence. An explicit opt-out inside an auto-reply still suppresses, because compliance wins. |
| **Bounce handling** | DSN parsing on inbound mail; hard bounces mark the send and stop the enrollment before the next step fires. |
| **Suppression & one-click unsubscribe** | Workspace-wide suppression list, signed unsubscribe tokens, opt-outs enforced at send time. |
| **Open / click tracking** | Signed, per-send tracking tokens, toggleable per campaign, with opens honestly labelled *indicative* and clicks *reliable*. |
| **Contacts & lists** | CSV import with skip/duplicate reporting, paginated list views. |
| **Multi-workspace teams** | One account, many workspaces. Owner / admin / member roles, email invites, workspace switcher. |
| **Envelope-encrypted secrets** | Per-workspace data-encryption keys wrapped by a key-encryption key. Deleting a workspace crypto-shreds its secrets. |

---

## A look inside

### Run campaigns

Draft until you launch it, then running until it's done — with the launch button only where launching
is actually legal.

![Campaign list](docs/images/campaigns.png)

### Build the sequence

Steps, delays, and merge fields — with structural edits locked once the campaign is running, so you
can't reorder the ground out from under contacts mid-flight.

![Campaign sequence editor](docs/images/campaign-sequence.png)

### Watch what actually happened

Send counts, engagement rates, and every enrollment with the reply class that stopped it.

![Campaign metrics and reply classification](docs/images/campaign-metrics.png)

### Connect the mailboxes you already own

Gmail and Microsoft 365 connect over OAuth; anything else connects over SMTP/IMAP. Credentials are
verified with a live connection test before they're saved, then sealed.

![Mailbox list with provider picker](docs/images/mailboxes.png)

TLS is on by default, and turning it off is an explicit, persisted decision — the same policy is
applied to the connection test *and* to every subsequent send, so a mailbox can never silently
downgrade to cleartext auth.

![Connect an SMTP mailbox](docs/images/connect-mailbox.png)

### Warm them up

Enable warmup on two or more mailboxes and they start exchanging real threaded mail on a ramping
daily volume. Health is measured, not assumed — inbox-vs-spam placement over the trailing week
decides whether a mailbox reads as healthy, watch, or at risk, and a mailbox that turns bad gets
paused rather than pushed harder. Orange is reserved for this one concept in the whole product.

![Warmup pool with per-mailbox health](docs/images/warmup.png)

### Import contacts

![Contacts and lists](docs/images/contacts.png)

---

## How it works

Inroad splits into a **control plane** that owns all state and an **execution plane** that owns all
outbound network I/O. They meet at exactly one interface, `coreapi.Client`.

```
                        CONTROL PLANE                          EXECUTION PLANE
        ┌──────────────────────────────────────────┐      ┌──────────────────────┐
        │                                          │      │                      │
        │   cmd/inroad  ──── REST ────  web/ SPA   │      │      cmd/worker      │
        │       │                                  │      │                      │
        │       │  domains: auth · mailbox ·       │      │  sender ─────────────┼──▶ SMTP
        │       │  campaign · sequencestep ·       │      │  sequence (advance)  │    Gmail API
        │       │  contact · list · enrollment ·   │      │  inbox   (poll)      │    MS Graph
        │       │  suppression · tracking          │      │  track · personalize │
        │       │                                  │      │                      │
        │  ┌────┴─────┐   ┌────────┐               │      └──────────┬───────────┘
        │  │ Postgres │   │ Redis  │◀── asynq ─────┼─────────────────┘
        │  └──────────┘   └────────┘   job queue   │      workers reach data ONLY
        │       ▲                                  │      through coreapi.Client
        │       └────── coreapi.Client ────────────┼──────────────┘
        └──────────────────────────────────────────┘
```

**The worker never touches Postgres.** It asks `coreapi` for a job, gets back a short-lived
credential (a decrypted SMTP password or a freshly refreshed OAuth access token), sends, and reports
the outcome. Token refresh, re-sealing, and persistence all happen control-plane side. That seam is
in-process today and becomes an HTTP call the day you want workers on separate hosts and separate
IPs — nothing else changes.

**Outbound mail leaves through each mailbox's own provider**, not through the worker's IP, so
scaling throughput means adding mailboxes and workers rather than warming up new IPs.

**Secrets use a two-level key hierarchy.** Each field ciphertext is sealed under a per-workspace
data-encryption key (DEK), AAD-bound to the workspace id; each DEK is wrapped by a key-encryption key
(KEK) behind a `KeyProvider` seam. Today that's a local AES master key; a cloud KMS drops into the
same seam. Because DEKs cascade-delete with the workspace, deleting a workspace permanently shreds
its secrets.

Full write-up in [docs/architecture.md](docs/architecture.md); the non-negotiables are in
[docs/security.md](docs/security.md).

---

## Quick start

You need **Docker**, **Go 1.25+**, and **Node 22+**.

```bash
git clone https://github.com/Axomble/Inroad && cd Inroad
cp .env.example .env
```

Generate the two required secrets and put them in `.env`:

```bash
openssl rand -base64 32     # → INROAD_JWT_SECRET
openssl rand -base64 32     # → INROAD_MASTER_KEY  (must decode to 32 bytes)
```

Then bring it up — Postgres and Redis in Docker, everything else native:

```bash
make db-up          # Postgres on :5433 + Redis on :6379
make migrate-up     # apply migrations
make run-api        # API on :8080
make run-worker     # in a second shell
```

```bash
cd web && npm install && npm run dev     # SPA on :5173
```

Optionally seed a demo workspace:

```bash
go run ./cmd/seed
# → login demo@inroad.test / demodemo
```

Open <http://localhost:5173> and register, or log in with the seeded account.

> **Note**
> `cmd/*` reads configuration from the environment, not from `.env` directly. Export it first
> (`set -a && . ./.env && set +a`) or use a loader like `direnv`. The Docker Compose stack in
> [Self-hosting](#self-hosting) reads `.env` for you.

### Every make target

| Target | What it does |
|---|---|
| `make db-up` / `db-down` | Start / stop the dev Postgres + Redis |
| `make migrate-up` / `migrate-down` | Apply / roll back one migration |
| `make sqlc` | Regenerate the sqlc query layer |
| `make run-api` / `run-worker` | Run the API server / the worker |
| `make build` | Build `inroad`, `worker`, `migrate`, `seed` into `./bin` |
| `make test` | Unit tests (no external services) |
| `make test-integration` | Integration tests (needs `make db-up`) |
| `make lint` | golangci-lint + oxlint + strict `tsc` |

---

## Self-hosting

Inroad runs with no cloud account of any kind — no AWS, no GCP, no Stripe, no managed queue. One
command brings up the whole platform:

```bash
git clone https://github.com/Axomble/Inroad && cd Inroad
cp .env.example .env        # fill in INROAD_JWT_SECRET + INROAD_MASTER_KEY
docker compose up --build
```

The API serves the built SPA on <http://localhost:8080>, migrations run automatically on the API
container's startup, and the worker attaches to Redis. The stack refuses to boot if the two secrets
are missing — no insecure defaults.

| Concern | Self-host default | Optional / swap-in |
|---|---|---|
| Database | PostgreSQL 16 (container) | Any Postgres — RDS, Cloud SQL, Neon |
| Cache / job queue | Redis 7 (container, asynq) | ElastiCache, any Redis |
| Root key (KEK) | Local AES-256 master key | Cloud KMS — the `KeyProvider` seam exists, the provider does not yet |
| Mail transport | Your own mailboxes | Gmail API · Microsoft Graph · any SMTP/IMAP host |
| Reply classification | Deterministic, offline, no network | An optional `ModelClassifier` seam for the ambiguous middle |
| Payments / licensing | None — everything is unlocked | — |

Reverse-proxy `:8080` behind TLS in production. For a worker fleet across multiple hosts, run the
`worker` binary directly rather than via Compose. Connecting Gmail and Microsoft 365 (OAuth client
setup, exact scopes requested, redirect URIs) is covered step by step in
[docs/self-hosting.md](docs/self-hosting.md).

---

## Documentation

| Read this | To learn |
|---|---|
| [docs/architecture.md](docs/architecture.md) | The control/execution split, the transport abstraction, the encryption model, and how reply classification routes |
| [docs/security.md](docs/security.md) | The security invariants that must never be broken — read before touching credentials, outbound dials, or tenant queries |
| [docs/self-hosting.md](docs/self-hosting.md) | Deploying, generating keys, and connecting Gmail / M365 mailboxes |
| [api/openapi.yaml](api/openapi.yaml) | The REST contract. The SPA's typed client is generated from it — never hand-edited |
| [CLAUDE.md](CLAUDE.md) | Repo conventions: layering rules, naming, and the code-quality bar |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Dev loop, tests, and what a good PR looks like |

---

## Project layout

```
cmd/                    thin binary entrypoints — inroad · worker · migrate · seed
internal/
  app/<domain>/         feature slices; each owns its own store + Routes()
  platform/             cross-cutting infra — config · log · db · httpx · queue · crypto · mail
  worker/               execution-plane engines — sender · sequence · inbox · track · warmup
  coreapi/              the control ⇄ execution seam
web/src/
  features/<domain>/    mirrors the backend domains
  routes/               composed from features/
api/openapi.yaml        REST contract; the SPA's client is generated from it
deploy/                 Dockerfiles + compose stacks
docs/                   architecture · security · self-hosting
```

Layering is enforced by convention and review: `app/*` may import `platform/*` but never the reverse,
`app/*` packages never import each other, and workers reach relational data only through `coreapi`.

---

## Status

Inroad is under active development and pre-1.0. What's built works and is covered by unit and
integration tests; what isn't built is listed here rather than implied by a feature grid.

**Working today:** multi-workspace auth with refresh-token rotation and reuse detection · Gmail /
M365 / SMTP mailbox connect · multi-step sequences with reorder · enrollment engine with ramp-aware
daily caps · the warmup pool end-to-end (ramping volume, threaded replies, rescue-from-spam,
mark-read, measured placement health, per-IP worker routing) · reply and bounce polling across all
three transports · deterministic reply classification · suppression and one-click unsubscribe ·
open/click tracking.

**On the roadmap:** enforcement of the per-mailbox minimum send interval (stored and editable, not
yet applied at send time) · a unified cross-mailbox inbox UI · cloud KMS as a second `KeyProvider` ·
rate limiting and an audit log on auth, connect, and reply-driven suppression · a key-rotation CLI.

---

## Contributing

Pull requests are welcome. Keep each one to a single logical change, and open an issue first for
anything that changes the architecture or the product surface.

Before opening a PR:

```bash
make test              # unit
make test-integration  # needs make db-up
make lint              # golangci-lint + oxlint + strict tsc — keep it green
```

Branch by type (`feature/…`, `fix/…`, `chore/…`), never commit feature work straight to `main`, and
use conventional commits. Full details in [CONTRIBUTING.md](CONTRIBUTING.md); conventions and the
code-quality bar are in [CLAUDE.md](CLAUDE.md).

---

## Security

Inroad handles mailbox credentials, so security is a hard requirement rather than a feature. The
invariants are written down in [docs/security.md](docs/security.md) and every security-sensitive PR
is checked against them: credentials are envelope-encrypted and never appear in a response or a log,
every tenant-scoped query is pinned to a `workspace_id`, user-supplied hosts are dialed only through
the SSRF guard, and TLS is enforced by default on SMTP and IMAP.

**Found a vulnerability?** Please report it privately rather than opening a public issue — open a
[GitHub security advisory](https://github.com/Axomble/Inroad/security/advisories/new) on this
repository. Responsible disclosure is credited in the release notes.

---

## License

[Apache License 2.0](LICENSE). Copyright 2026 Ahmed Mustufa Malik.
