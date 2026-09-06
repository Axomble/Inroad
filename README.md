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
[![Postgres](https://img.shields.io/badge/Postgres-16-336791?logo=postgresql&logoColor=white)](docker-compose.yml)
[![Self-hosted](https://img.shields.io/badge/self--hosted-docker%20compose-2496ED?logo=docker&logoColor=white)](docs/self-hosting.md)

[Quick start](#quick-start) · [Features](#features) · [How it works](#how-it-works) · [Docs](#documentation) · [Security](#security)

⭐ **If Inroad saves you a per-seat subscription, star the repo** — it's the cheapest way to help.

</div>

---

Inroad sends cold email sequences from the mailboxes you already own — Gmail, Microsoft 365, or plain
SMTP — and paces them on a warm-up ramp so they keep landing in the inbox. Replies, bounces, and
opt-outs are polled back in, classified, and used to stop the sequence automatically. Everything runs
on your own hardware: one Postgres, one Redis, two Go binaries, and a React SPA. No SaaS account, no
per-seat pricing, no third party holding your mailbox credentials.

It's an open-source alternative to Instantly and Smartlead, built for people who would rather run the
infrastructure than rent it.

<div align="center">
  <img src="docs/images/login.png" alt="Inroad sign-in" width="100%">
</div>

---

## Quick start

**Run it** (self-hosting — only Docker required):

```bash
git clone https://github.com/Axomble/Inroad && cd Inroad
docker compose up -d
```

That's the whole install. Secrets are generated on first boot, migrations run automatically, and the
app is on <http://localhost>. Deployment options (env vars, Gmail/M365 OAuth setup, Terraform, Helm)
are in [docs/self-hosting.md](docs/self-hosting.md).

**Hack on it** (live-reloading dev stack — still only Docker required):

```bash
docker compose -f docker-compose.dev.yml up -d
```

Go hot-reloads via `air`, the SPA runs Vite HMR on <http://localhost:5173>, Mailpit catches every
transactional email on <http://localhost:8025>, and the docs site serves on <http://localhost:4321>.
Seed a demo workspace to log into:

```bash
docker compose -f docker-compose.dev.yml exec api go run ./cmd/seed
# → login demo@inroad.test / demodemo
```

Prefer running Go and Node natively? See [CONTRIBUTING.md](CONTRIBUTING.md) — `make dev` (or
`.\scripts\dev.ps1` on Windows) does the same thing without containers for the binaries.

---

## Features

- **Sequencing** — multi-step campaigns with per-step delays, merge fields, A/B variants per step,
  timezone-aware send windows, and a natural send cadence that never emits on a uniform interval.
- **Sending infrastructure** — Gmail API, Microsoft Graph, and SMTP/IMAP behind one seam; sender
  pools with round-robin / LRU / weighted rotation; ramped daily caps and campaign-wide limits
  enforced on the send path.
- **Warm-up** — opted-in mailboxes exchange real threaded mail on a ramping volume; placement is
  measured (inbox vs spam), health is recomputed from it, and a mailbox that turns bad is paused
  instead of pushed. Warmup health gates cold sending.
- **Replies & deliverability** — reply polling across all three transports with deterministic,
  offline classification plus a user-definable taxonomy driving automation; DSN bounce handling;
  suppression and one-click unsubscribe; SPF/DKIM/DMARC checks per sending domain; a deliverability
  dashboard and cross-campaign reporting.
- **Unified inbox & CRM** — every reply from every mailbox in one threaded view, reply-from-inbox
  through the owning mailbox; companies, deals, pipelines, notes, tasks and an activity feed; typed
  custom fields validated at import and preflight; contacts at scale (trigram search, keyset paging).
- **AI, human-in-the-loop** — an in-app agent and AI-drafted replies behind an approval queue;
  bring your own key, and the entire feature is off (and makes no calls) until you add one.
- **Platform** — multi-workspace teams and roles; passkeys, TOTP, Google sign-in, refresh-token
  rotation; scoped API keys, an OAuth 2.0 provider, and an MCP server exposing the agent's typed
  tools; envelope-encrypted credentials with per-workspace crypto-shredding.

![Campaign metrics and reply classification](docs/images/campaign-metrics.png)

![Warmup pool with per-mailbox health](docs/images/warmup.png)

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

The worker never touches Postgres: it asks `coreapi` for a job, receives a short-lived credential,
sends, and reports back. Outbound mail leaves through each mailbox's own provider, not the worker's
IP. Secrets are envelope-encrypted per workspace behind a `KeyProvider` seam. The full write-up is in
[docs/architecture.md](docs/architecture.md); the non-negotiables are in
[docs/security.md](docs/security.md).

---

## Documentation

| Read this | To learn |
|---|---|
| [docs/architecture.md](docs/architecture.md) | The control/execution split, transports, encryption, reply classification |
| [docs/security.md](docs/security.md) | The security invariants — read before touching credentials, dials, or tenant queries |
| [docs/self-hosting.md](docs/self-hosting.md) | Deploying, env vars, and connecting Gmail / M365 (OAuth setup, scopes, redirect URIs) |
| [api/openapi.yaml](api/openapi.yaml) | The REST contract — the SPA's typed client is generated from it |
| [CONTRIBUTING.md](CONTRIBUTING.md) | Dev loop, native setup, tests, and what a good PR looks like |

The same docs ship as a browsable site (`docs/`, Astro/Starlight) — the dev stack serves it on
<http://localhost:4321>.

---

## Status

Pre-1.0 and under active development. Everything in [Features](#features) works today and is covered
by unit and integration tests. Notable roadmap items: lead-flow throttling, list verification before
send, soft-bounce retry and FBL ingestion, custom tracking domains, outbound webhooks, cloud KMS, and
billing for the open-core split. Open work is tracked as
[GitHub issues](https://github.com/Axomble/Inroad/issues).

---

## Contributing

Pull requests are welcome. Keep each one to a single logical change, open an issue first for anything
architectural, branch by type (`feature/…`, `fix/…`, `chore/…`), and keep `make test` /
`make test-integration` / `make lint` green. Details in [CONTRIBUTING.md](CONTRIBUTING.md); repo
conventions live in [CLAUDE.md](CLAUDE.md).

---

## Security

Inroad handles mailbox credentials, so security is a hard requirement rather than a feature:
credentials are envelope-encrypted and never appear in a response or a log, every tenant-scoped query
is pinned to a `workspace_id`, user-supplied hosts are dialed only through the SSRF guard, and TLS is
enforced by default on SMTP and IMAP. The invariants are written down in
[docs/security.md](docs/security.md).

**Found a vulnerability?** Please report it privately — open a
[GitHub security advisory](https://github.com/Axomble/Inroad/security/advisories/new) rather than a
public issue. Responsible disclosure is credited in the release notes.

---

## License

[Apache License 2.0](LICENSE). Copyright 2026 Ahmed Mustufa Malik.
