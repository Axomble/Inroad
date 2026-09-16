---
title: Environment Variables Reference
description: Every environment variable the Inroad backend reads, with its real default, grouped by what it configures.
---

`internal/platform/config/config.go` is the source of truth for this page. It is
the only place the binaries read `INROAD_*` configuration from — the one
exception is `INROAD_LOG_LEVEL`, which `internal/platform/log` reads directly
(and alone) so a logger can exist before config does. Every default below is
that file's, and the tables are complete against it: all **82** variables the
backend reads are listed here.

## How values are parsed

Four rules apply to everything on this page, and each of them has surprised
somebody:

- **Empty is the same as unset**, and whitespace counts as empty. A variable set
  to the empty string takes its default. You cannot blank out a defaulted value
  (`INROAD_MS_TENANT=""` is `common`, not empty); you have to give it a different
  value.
- **Booleans accept `1`/`0`, `true`/`false`, `t`/`f`, `yes`/`no`, `y`/`n` and
  `on`/`off`**, in any case, with surrounding whitespace ignored. **Anything else
  is a startup error** that names the variable and quotes what you gave it.
  `ture` does not mean `false`.
- **A number or duration that does not parse is a startup error too.**
  `INROAD_ACCESS_TOKEN_TTL=5` is not five of anything — a duration needs a unit
  (`30s`, `5m`, `720h`) — so the process refuses to start rather than taking the
  5-minute default and leaving you to believe your setting took effect.
- **Everything wrong is reported together.** One startup lists every malformed
  value, so a bad configuration costs one restart to diagnose rather than one per
  mistake. `INROAD_JWT_SECRET`, `INROAD_REDIS_ADDR`, `INROAD_MASTER_KEY`,
  `INROAD_FLEET_BROKER_TOKEN` and the database-pool budget are validated on top
  of that.

:::caution[Upgrading: a value that used to be ignored can now stop the process]
Until this changed, an unrecognised boolean read as `false` and an unparseable
number or duration was discarded in favour of its default — so a working
deployment may be carrying a typo that has never done anything visible. It now
fails at startup, naming the variable.

Nothing that parsed before stops parsing: `1`, `true` and `yes` are still
accepted and the boolean set only grew. The typo is what changed, and on the
three flags that default to **`true`** (`INROAD_MAIL_ALLOW_PRIVATE_HOSTS`,
`INROAD_RUN_SCHEDULER`, `INROAD_COOKIE_SECURE`) that typo was silently turning
the flag **off** — which is how `INROAD_COOKIE_SECURE=on` dropped the `Secure`
attribute from the session cookie while looking like it had enabled it.
:::

## Core

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_ENV` | Deployment environment name. Sets the default log level, and `cmd/seed -sandbox` **refuses to run** when it is `production`, `prod`, `live` (case-insensitive) or empty — a harness that fabricates data will not assume an environment it cannot identify is safe | `development` |
| `INROAD_DATABASE_URL` | PostgreSQL connection URL. `pool_max_conns` / `pool_min_conns` in the DSN override the pool variables — see [Database connection budget](#database-connection-budget) | `postgres://inroad:inroad@localhost:5432/inroad?sslmode=disable` |
| `INROAD_REDIS_ADDR` | Either a bare `host:port` or a `redis://` / `rediss://` URL (auth, database index, TLS). **Validated at startup** — a malformed URL fails with the offending value rather than panicking inside a client constructor later | `localhost:6379` |
| `INROAD_PUBLIC_URL` | Externally reachable base URL. Unsubscribe links in outbound mail, the OAuth redirect URLs, and the WebAuthn relying party all default from it | `http://localhost:8080` |
| `INROAD_APP_BASE_URL` | Frontend origin that emailed links (verify, reset, invite) point at | `http://localhost:5173` |
| `INROAD_WEB_DIR` | Built SPA directory. When it exists the API also serves the static assets and an index fallback; an empty or missing directory leaves the API in API-only mode and logs a warning | unset (API-only) |

In a compose deployment the API and worker each need `INROAD_DATABASE_URL`,
`INROAD_REDIS_ADDR` and `INROAD_JWT_SECRET` at minimum, plus `INROAD_MASTER_KEY`
on every process that opens a mailbox credential — which is every topology except
a [`role=send` fleet worker](#credential-brokering-send-role), where it is
forbidden.

## Listeners, metrics and profiling

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_HTTP_ADDR` | Address the public API server binds to (`cmd/inroad` only — the worker serves no HTTP) | `:8080` |
| `INROAD_METRICS_ADDR` | Address of the **dedicated** Prometheus `/metrics` listener, e.g. `:9091`. Started by both `cmd/inroad` and `cmd/worker`. Empty disables it entirely, so a self-hoster who runs no Prometheus opens no extra port | unset (disabled) |
| `INROAD_PPROF_ENABLED` | Mounts `net/http/pprof` under `/debug/pprof/*` on the **metrics** listener | `false` |

Metrics are never mounted on the public API router, which is why serving them
raises no authentication question — but it also means the listener has none.
Bind `INROAD_METRICS_ADDR` to an interface only your scrapers can reach.

`INROAD_PPROF_ENABLED` is a separate flag rather than something implied by
`INROAD_METRICS_ADDR` on purpose: goroutine stacks and heap profiles are a
strictly more sensitive disclosure than a counter value, and pointing a scraper
at a port is not consent to publish them.

## Secrets and key management

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_JWT_SECRET` | Signs access tokens. **Required**, minimum **16 bytes** — generate 32 with `openssl rand -base64 32` | none (startup error) |
| `INROAD_MASTER_KEY` | The KEK that wraps every per-workspace DEK. **Base64 decoding to exactly 32 bytes.** Unset is legal at the config layer and each binary asserts it for itself; a set-but-malformed value is always a hard error, so a typo can never degrade into "this process has no key" | unset |
| `INROAD_KEY_PROVIDER` | KEK backend. Only `local` (wrap under `INROAD_MASTER_KEY`) is selectable — an AWS KMS provider exists behind the same interface but is not wired to this switch, and any other value **fails closed** on every process that builds a keyring | `local` |
| `INROAD_TRACKING_SECRET` | Signs open/click tracking tokens. Falls back to `INROAD_JWT_SECRET`; minimum 16 bytes when set explicitly | `INROAD_JWT_SECRET` |
| `INROAD_WARMUP_SECRET` | Signs the `X-Inroad-Warmup` receipt header so the inbox poller can attribute a received warmup message to its send. Same fallback and floor | `INROAD_JWT_SECRET` |
| `INROAD_WS_TICKET_SECRET` | Signs the realtime WebSocket connect ticket (a browser cannot set an `Authorization` header on an Upgrade request). Same fallback and floor | `INROAD_JWT_SECRET` |

The three derived secrets exist so that rotating one does not invalidate the
others: change `INROAD_TRACKING_SECRET` and live sessions survive. Sharing
`INROAD_JWT_SECRET` between them is safe because each token payload carries a
domain prefix, so a tracking token cannot be presented as a connect ticket.

## Sessions, tokens and cookies

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_ACCESS_TOKEN_TTL` | Access-token lifetime | `5m` |
| `INROAD_REFRESH_TOKEN_TTL` | Refresh-token (session) lifetime | `720h` (30 days) |
| `INROAD_SESSION_CACHE_TTL` | How long a process caches a session's revocation/expiry state before re-reading Postgres. `0` or less disables the cache and every request hits the database | `5s` |
| `INROAD_COOKIE_SECURE` | `Secure` attribute on the refresh cookie | `true` |
| `INROAD_COOKIE_DOMAIN` | `Domain` attribute on the refresh cookie | unset (host-only) |
| `INROAD_EMAIL_VERIFY_TTL` | Lifetime of an email-verification token | `24h` |
| `INROAD_PASSWORD_RESET_TTL` | Lifetime of a password-reset token | `1h` |
| `INROAD_INVITE_TTL` | Lifetime of a workspace invite | `72h` |

The short access-token TTL is not the revocation guarantee on its own. Every
request re-validates the token against the session store, so a revoked session
stops working within `INROAD_SESSION_CACHE_TTL`, not within
`INROAD_ACCESS_TOKEN_TTL`. Raising the cache TTL buys database load and costs
revocation latency across replicas — that is the whole trade.

## WebAuthn relying party

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_RP_ID` | Registrable domain passkey ceremonies bind to — **host only**, no scheme or port | host of `INROAD_PUBLIC_URL` |
| `INROAD_RP_ORIGIN` | Fully-qualified origin the browser must present, `scheme://host[:port]` | origin of `INROAD_PUBLIC_URL` |

When `INROAD_PUBLIC_URL` is unparseable and neither is set explicitly, both stay
empty and the passkey endpoints fail cleanly — the feature is off rather than
validating ceremonies against a wrong domain.

`INROAD_RP_ORIGIN` does double duty: it is also the **allowed `Origin` for the
realtime WebSocket**. If you set it by hand, set it to the origin browsers
actually load the app from, or sockets will be refused even though passkeys work.

## Outbound dial policy

These three decide whether a destination on a private network is reachable at
all. **Loopback, link-local (including the cloud metadata address
`169.254.169.254`) and multicast stay blocked regardless of every one of them.**

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_MAIL_ALLOW_PRIVATE_HOSTS` | Permit mailbox SMTP/IMAP hosts on RFC1918 / ULA private ranges | **`true`** |
| `INROAD_AI_ALLOW_PRIVATE_BASE_URL` | Permit an `openai_compatible` AI provider base URL on a private or loopback host — a local Ollama or vLLM | `false` |
| `INROAD_WEBHOOK_ALLOW_PRIVATE` | Permit outbound webhook receiver URLs on private or loopback hosts. `cmd/inroad` logs a warning at startup when it is on | `false` |

The asymmetry is deliberate, not an oversight. Mail defaults **open** because a
self-hoster reaching an internal Exchange or Postfix server is the ordinary case;
set it to `false` for a multi-tenant deployment where a workspace could otherwise
point a "mailbox" at your internal network. The other two default **closed**
because they are development conveniences, and a convenience that is on by
default is a hole.

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_TRUSTED_PROXIES` | Comma-separated CIDRs whose `X-Forwarded-For` header is trusted. Empty trusts none and uses the direct peer address | unset (trust none) |

When the direct peer falls inside this set, the client IP is the **rightmost**
XFF entry that is not itself a listed proxy. Getting this wrong is not cosmetic:
the client IP is what the rate limits below are keyed on, so trusting nothing
behind a real load balancer collapses every caller onto one key, and trusting too
much lets a caller spoof the key entirely.

## Rate limits

All values are **requests per minute** in a fixed window, backed by the shared
Redis limiter, which **fails closed**. A non-positive value means no cap for that
key.

Pre-authentication — each abusable open endpoint is throttled on the client IP
and, where the request body names one, the target account:

| Variable | Applies to | Default |
| :--- | :--- | :--- |
| `INROAD_RATELIMIT_LOGIN_IP` | `POST /login`, per IP | `10` |
| `INROAD_RATELIMIT_LOGIN_ACCOUNT` | `POST /login`, per email | `5` |
| `INROAD_RATELIMIT_VERIFY_IP` | 2FA / passkey / email-OTP verify, per IP | `10` |
| `INROAD_RATELIMIT_VERIFY_ACCOUNT` | email-OTP verify, per email | `5` |
| `INROAD_RATELIMIT_SENSITIVE_IP` | password/forgot, email-OTP start, OAuth register, Google sign-in start, per IP | `5` |
| `INROAD_RATELIMIT_SENSITIVE_ACCOUNT` | password/forgot and email-OTP start, per email | `3` |

Authenticated — these are not throttled against abuse of an open door, so their
second key is the **workspace** rather than an email:

| Variable | Applies to | Default |
| :--- | :--- | :--- |
| `INROAD_RATELIMIT_DRAFT_REPLY_IP` | `POST /inbox/threads/{id}/draft-reply`, per IP | `20` |
| `INROAD_RATELIMIT_DRAFT_REPLY_WORKSPACE` | the same endpoint, per workspace | `60` |
| `INROAD_RATELIMIT_REALTIME_TICKET_IP` | `POST /realtime/ticket`, per IP | `60` |
| `INROAD_RATELIMIT_REALTIME_TICKET_WORKSPACE` | the same endpoint, per workspace | `600` |

Draft-reply is capped because every call spends real money at an AI provider, and
the workspace owns that budget. Realtime ticket minting is capped because the
endpoint issues a **credential** — but generously, because one tab mints one
ticket per connect and a reconnect storm after a deploy is legitimate traffic. In
both pairs the per-IP number is the more tolerant one, since a whole office can
share a single NAT address.

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_TURNSTILE_SECRET` | Cloudflare Turnstile secret, validated server-side on register / login / email-OTP start. Empty disables the captcha gate entirely (a verifier that always passes). Never logged | unset (no captcha) |

## Realtime connections

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_REALTIME_MAX_CONNS_PER_USER` | Open WebSockets per user. `0` takes the package default | `0` → `8` |
| `INROAD_REALTIME_MAX_CONNS_PER_WORKSPACE` | Open WebSockets per workspace. `0` takes the package default | `0` → `200` |

Each connection costs a goroutine, a buffer and a registry slot, so an unbounded
socket count is a trivial resource-exhaustion vector. Note that `0` here means
"use the default", **not** "unlimited".

## AI agent runs

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_AGENT_MAX_CONCURRENT_RUNS` | How many agent runs one API process executes simultaneously. `0` takes the package default | `0` → `20` |

Agent runs are goroutines inside the API binary rather than queued tasks, so
without a bound a burst is unbounded concurrency against the AI provider, the
database pool, and every tool the runs call.

## Blob storage

The filesystem backend is the default and needs nothing configured. Setting
`INROAD_S3_BUCKET` is what switches a deployment to the S3 (or S3-compatible:
MinIO, R2, Wasabi) backend.

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_STORAGE_FS_ROOT` | Filesystem blob root | `./data/blobs` |
| `INROAD_S3_BUCKET` | Bucket name. **Setting this selects the S3 backend** | unset (filesystem) |
| `INROAD_S3_REGION` | AWS region | `us-east-1` |
| `INROAD_S3_ENDPOINT` | Custom endpoint for an S3-compatible server. Empty uses AWS's own | unset |
| `INROAD_S3_ACCESS_KEY_ID` | Static access key. Blank falls back to the AWS SDK's default credential chain — env, shared config, an instance or task role | unset |
| `INROAD_S3_SECRET_ACCESS_KEY` | Static secret key, same fallback | unset |
| `INROAD_S3_FORCE_PATH_STYLE` | Path-style addressing (`https://host/bucket/key`), which MinIO and some other S3-compatible servers require and AWS S3 does not use | `false` |
| `INROAD_S3_ALLOW_PLAINTEXT_ENDPOINT` | **Dev only.** Permits a plaintext `INROAD_S3_ENDPOINT` | `false` |

A deployment already running on AWS with an attached role needs only the bucket
name. `INROAD_S3_ALLOW_PLAINTEXT_ENDPOINT` defaults to `false` so that an absent
value keeps HTTPS mandatory, and a misspelled one stops the binary rather than
being read as `false` — a misconfiguration can never put SigV4-signed requests,
object bodies and presigned URLs on the wire in cleartext, and never goes
unnoticed either.

:::note[Configured, not yet consumed]
These variables load and validate today, but nothing reads the storage provider
yet — the first consumer is the attachments feature. Setting them changes no
behaviour until then, and that is worth knowing before you spend an afternoon
debugging why nothing appears in your bucket.
:::

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

The links inside those messages point at `INROAD_APP_BASE_URL` (see
[Core](#core)), not at `INROAD_PUBLIC_URL` — they are frontend routes.

The `console` driver never logs message bodies, because they contain single-use
bearer credentials (verify/reset links, login codes). To read a real message in
development, use a mail catcher — the dev compose stack runs Mailpit and serves
the caught mail at `http://localhost:8025`.

`INROAD_SYSTEM_SMTP_ALLOW_PLAINTEXT` exists only so a local catcher (plaintext,
no AUTH) can be reached. TLS is mandatory unless it is set to an explicitly
truthy value; unset and empty keep TLS on, and a misspelled value refuses to
start, so a configuration mistake cannot downgrade delivery to cleartext. Do not
set it in production.

## OAuth providers

Three independent flows. Each is **disabled** while its client id or secret is
blank: the start endpoint returns `501` and, for sign-in, the SPA hides the
button.

Mailbox connect via Gmail:

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_GOOGLE_CLIENT_ID` | OAuth client id | unset (Gmail connect disabled) |
| `INROAD_GOOGLE_CLIENT_SECRET` | OAuth client secret | unset |
| `INROAD_GOOGLE_REDIRECT_URL` | Callback URL | `${INROAD_PUBLIC_URL}/oauth/google/callback` |

Inroad **sign-in** via Google — a different flow with its own callback and only
the `openid`/`email`/`profile` scopes, never Gmail:

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_GOOGLE_SIGNIN_CLIENT_ID` | OAuth client id | falls back to `INROAD_GOOGLE_CLIENT_ID` |
| `INROAD_GOOGLE_SIGNIN_CLIENT_SECRET` | OAuth client secret | falls back to `INROAD_GOOGLE_CLIENT_SECRET` |
| `INROAD_GOOGLE_SIGNIN_REDIRECT_URL` | Callback URL. **Does not fall back** | `${INROAD_PUBLIC_URL}/api/v1/auth/oauth/google/callback` |

The id and secret fall back **as a pair, keyed on the id**: setting a sign-in id
without a sign-in secret leaves both empty rather than pairing your sign-in id
with the *mailbox* secret, which would fail at Google in a way no log here could
explain. One configured Google client therefore makes both features work. The
reason to give sign-in its own client is that the mailbox client requests
restricted Gmail scopes subject to Google's verification review, and on a
separate client a pending review can never block people signing in. Whichever
client you use must list the exact redirect URL in its authorized redirect URIs.

Mailbox connect via Microsoft 365 / Graph:

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_MS_CLIENT_ID` | OAuth client id | unset (M365 connect disabled) |
| `INROAD_MS_CLIENT_SECRET` | OAuth client secret | unset |
| `INROAD_MS_REDIRECT_URL` | Callback URL | `${INROAD_PUBLIC_URL}/oauth/microsoft/callback` |
| `INROAD_MS_TENANT` | Azure AD authority | `common` |

## Logging

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_LOG_LEVEL` | `debug`, `info`, `warn` (or `warning`), or `error` | unset — see below |

Unset, the level comes from `INROAD_ENV`: **`debug` when it is `development`,
`info` otherwise.** An explicit level always wins; an *unrecognised* one falls
back to that same environment-derived default rather than failing.

This is the one variable read outside `config.go`, and it is read there *only*:
`log.New` consults the process environment itself, because a logger has to exist
before configuration is loaded. `Config` carries no log-level field — it used to,
set and never read by anything, which is one source of truth too many.

Unlike the parsed values above, a misspelled level does not stop the process —
deliberately. It logs at the environment default instead, because a binary that
refuses to start over the verbosity of its own logs helps nobody.

## Worker Tuning

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_WORKER_CONCURRENCY` | Number of concurrent asynq worker goroutines per worker process | `10` |
| `INROAD_RUN_SCHEDULER` | Whether **this** worker process runs the periodic scheduler | `true` |
| `INROAD_WORKER_ROLE` | Which half of the worker this process runs — `control`, `send`, or unset for both. See [Splitting control and send roles](#splitting-control-and-send-roles) before using it in production | unset (`all`) |
| `INROAD_WORKER_QUEUES` | Explicit comma-separated override of the asynq queues this worker consumes. Set, it replaces the role's queue set outright rather than merging with it | unset (derived from the role) |

The default of `10` is sized for small deployments. Every per-mailbox send and
inbox-poll task shares this pool, so with many active mailboxes the queue backs
up behind it and sending looks slow even though nothing is wrong. As a rule of
thumb, raise it to at least `25` once you run **50 or more active mailboxes**
on one worker node.

When raising concurrency, keep the database pool larger than worker concurrency
plus headroom for the periodic sweepers and HTTP handlers — see
[Database connection budget](#database-connection-budget) below.

### The scheduler must be a singleton

The worker binary also runs the asynq **scheduler**, which enqueues the seven
periodic reconcile sweeps (enrollments, inbox, warmup, domain auth, recipient ESP,
maintenance cleanup, and the fleet rotation pass).

asynq elects no leader. Every worker process with `INROAD_RUN_SCHEDULER=true`
registers every periodic task independently, so **N replicas fire each sweep N
times.** The sweep handlers are idempotent, so this corrupts nothing — but each
sweep scans, and you pay that scan N times per interval, silently.

- **One worker (the default self-host).** Leave `INROAD_RUN_SCHEDULER` unset. It
  defaults to `true`, so the single worker schedules and nothing needs configuring.
- **Scaling out.** Set `INROAD_RUN_SCHEDULER=false` on every replica **except
  exactly one**. That one replica is otherwise an ordinary worker; it just
  additionally owns the periodic registrations.
- **If zero replicas run it,** the sweeps stop. Nothing breaks and no live work is
  lost: every sweep is a *reconcile*, not a deadline. Sends, inbox polls and
  warmup ticks already enqueued keep running; what stops is the safety net that
  re-enqueues work whose live task was lost (rows committed but the Redis enqueue
  failed). Restore the flag on one replica and the next tick catches up.

Each process logs its mode at startup at INFO, so you can tell from the logs
which replica schedules:

```
level=INFO msg="scheduler enabled for this replica"  run_scheduler=true
level=INFO msg="scheduler disabled for this replica" run_scheduler=false
```

### Splitting control and send roles

By default a worker process runs everything: the scheduler, the seven periodic
sweeps above, and every per-message handler (campaign sends, warmup ticks and
engagement, inbox polls, manual replies, test sends, webhook deliveries). This
is the self-host topology — one process, one trust domain, nothing to
configure.

`INROAD_WORKER_ROLE` splits which handlers a process registers into two
halves, **and which queues it consumes follows the same split** — that second
half used to be missing, which is why this section used to warn you off using
it. It doesn't any more; the topology below is operable.

- **`control`** — the scheduler and the seven periodic sweeps/purges. These scan
  or delete across every workspace, so this role is meant to stay on trusted
  infrastructure beside the API.
- **`send`** — per-message work only: campaign sends, warmup ticks and
  engagement, inbox polls, manual replies, test sends, and webhook deliveries.
  This is the role intended for a fleet host.
- Leave it **unset** (equivalently `all`) for the single-process default.

An unrecognised value is a **startup error**, not a silent fallback to `all`
— a typo that quietly degraded to running every sweep would leave a host an
operator believed was send-only running work it shouldn't.

A `send` role never runs the scheduler, regardless of `INROAD_RUN_SCHEDULER`:
the role is the stronger statement, so it is not undone by a scheduler flag
left over from when the host ran everything. Relatedly, a worker only
heartbeats into the `workers` registry — becoming eligible for the mailbox
assigner to route work to it — if it runs per-message work, so a `control`
host never appears there and can never be assigned a *new* mailbox.

#### Queue routing is now role-aware

Every producer routes its task to a role-scoped asynq queue, and each role
consumes exactly the queues the handlers it registers can service:

| Role | Consumes |
| :--- | :--- |
| `control` | `control` only |
| `send` | `w:<worker-id>` (its own affinity queue, if assigned), `send`, `default` |
| `all` (unset — the default, and every install predating this split) | `w:<worker-id>` (if assigned), `send`, `control`, `default` |

`default` is asynq's built-in queue. Nothing new is enqueued there — every
producer routes by task type, including the dead-letter replay path. It is
consumed **transitionally**, by `send` and `all` only, and it does two things:
it drains any task already sitting in Redis from before the upgrade, and it is
what makes a rolling upgrade to this version lossless, because an upgraded
worker consumes both what this version produces and what the previous one
produced. `control` deliberately never consumes it: everything a control host
could run is routed to `control` by construction, so a task on `default` is
per-message work by definition, and a control host claiming it is exactly the
failure this split exists to prevent.

`default` needs no configuration, and it is **drain-only: it is removed in the
release after this one.** The signal that it has finished its job is one you can
watch rather than estimate — `inroad_queue_depth{queue="default"}` is scraped
per queue, and it must read `0` in every state and stay there across a window
wider than the longest delay a task can carry (a reply can be scheduled days
out).

`INROAD_WORKER_QUEUES` still overrides all of this entirely — set it and the
role's default queue set is ignored outright, not merged with it. And a producer
cannot forget to route a task any more: the queue is derived from the task type
by a single table, and a type that has no entry in it fails the enqueue loudly
instead of landing silently on `default`, so a future task type can't repeat the
mistake this section used to warn about.

If you do split roles, there is one sharp edge that survives this fix, and one
ordering rule to follow. Know both before you set the variable.

**First, a stale mailbox assignment now fails quieter, not louder, and that
makes the fix below more load-bearing than it looks.** The heartbeat gate only
stops a `control` host being assigned *new* mailboxes; it does nothing about
mailboxes it already owns. A host that previously ran as `send` keeps its
`mailbox_worker_assignments` rows, and those rows stay authoritative while its
last heartbeat is inside the assigner's 15-minute live window — so a warmup
tick for one of those mailboxes still gets enqueued to that host's `w:<id>`
queue. Before this queue split, a `control` host also consumed `w:<id>`, so it
claimed that task, found no handler, and dead-lettered it — wrong, but at
least *visible* in the dead-letter table. Now `QueuesFor` omits `w:<id>` for
the control role entirely, so the task instead **sits on a queue nothing
consumes**: no error, no retry, no dead-letter row, nothing to alert on. Give
a host a new `INROAD_WORKER_ID` when you change it to `control`, or delete its
`mailbox_worker_assignments` rows at cutover — that step was already required
before this slice, but it is now the only thing standing between a role change
and a silently-stalled mailbox rather than a merely-annoying one.

**Second, upgrade the fleet first and split the roles afterwards — these are two
windows, not one.** This version changes both what a producer writes to and what
a consumer reads, so a fleet that is half-upgraded *and* half-split has work
sitting on queues nobody is reading. In order:

1. Roll this version out to every process — API and workers — leaving
   `INROAD_WORKER_ROLE` exactly as it already is (unset, i.e. `all`, for any
   deployment that has not split). A rolling `all` → `all` upgrade is clean at
   any mix ratio: an upgraded `all` worker consumes every queue either version
   produces (`w:<id>`, `send`, `control`, `default`), and an un-upgraded one
   only produces onto `default` and `w:<id>`, which the upgraded ones consume
   too. Work an already-upgraded producer puts on `send`/`control` waits in
   Redis until the first upgraded worker is up — delayed, never lost.
2. Watch `inroad_queue_depth{queue="default"}` fall to zero and stay there. That
   is the pre-upgrade backlog draining.
3. *Then*, as a separate change, set `INROAD_WORKER_ROLE` on each host.

Splitting *during* the version rollout is what to avoid, and both directions
fail silently rather than loudly:

- **Send-first.** The un-upgraded control host keeps registering its sweeps bare,
  onto `default` — continuously, not as a bounded backlog — and upgraded `send`
  hosts consume `default`, so they claim them. A sweep on a send host has no
  handler, so it dead-letters; the next tick is claimed the same way, so the
  reconciles **stall rather than self-heal**. Meanwhile `deliverability:evaluate`
  is now produced onto `control`, which the old control host does not consume, so
  the campaign circuit breaker quietly stops evaluating for the whole window.
- **Control-first.** The upgraded control host fans out onto `send` and `w:<id>`,
  which an un-upgraded `send` host does not consume. Nothing is lost — those
  tasks sit in Redis until a `send` host is upgraded — but **sending stops
  meanwhile, with no error anywhere**.

And one limit holds regardless of whether the queues route correctly: the role
split is an operational lever, not a containment boundary. A `send`-role process
is *logically* restricted to per-message handlers (it simply never registers the
cross-tenant handlers), but it still holds the same database connection as an
`all` process, so it isn't *physically* prevented from reaching the rest of the
schema. What a `send` host no longer holds is the **encryption key** — see the
next section.

### Credential brokering (`send` role)

A `role=send` worker **must not** be given `INROAD_MASTER_KEY` and will refuse to
start if it is. That key is the KEK: it unwraps every workspace's DEK, and
therefore decrypts every stored SMTP password and every OAuth refresh token in
the installation. Handing it to a host you are running because it is *cheap*
gives that host the whole installation's credentials, offline and permanently.

Instead a `send` worker asks the control plane to open each credential, one
mailbox at a time, over an authenticated HTTP channel.

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_FLEET_BROKER_ADDR` | **API side.** Address `cmd/inroad` serves the **fleet listener** on, e.g. `10.0.0.5:8090`. A **separate listener** from `INROAD_HTTP_ADDR` — bind it to an address only the fleet's network can reach. Unset = nothing fleet-facing is served at all | unset |
| `INROAD_FLEET_BROKER_URL` | **Worker side.** Base URL a `send` worker asks at, e.g. `https://control.internal:8090` | unset |
| `INROAD_FLEET_BROKER_TOKEN` | Shared bearer credential both sides present and check, **at least 32 bytes**. Generate with `openssl rand -base64 32`. Required on either side once the other variable is set | unset |
| `INROAD_FLEET_BROKER_ALLOW_PLAINTEXT` | Permits an `http://` fleet URL. **Default false** — the channel carries the token and the decrypted credential | `false` |
| `INROAD_FLEET_COREAPI_REMOTE` | **Worker side.** Read `coreapi` over the fleet channel instead of this worker's own database connection. **Default false.** See [Remote coreapi](#remote-coreapi-worker-side) below | `false` |

Rules the binaries enforce at startup, rather than at the first send:

- `role=send` **with** `INROAD_MASTER_KEY` → refuses to start.
- `role=send` **without** a broker URL → refuses to start (it could not send).
- **Any** role with both the key and a broker URL → refuses to start. A process
  configured to broker has no business also being able to decrypt everything.
- `role=control` needs neither: it registers no handler that opens a credential.
  An existing control host that still sets `INROAD_MASTER_KEY` keeps working.
- **Unset `INROAD_WORKER_ROLE` (the single-process self-host default) is
  completely unaffected.** Set `INROAD_MASTER_KEY` and nothing else, exactly as
  before. None of the five variables above exist for you.

#### One listener, one token

`INROAD_FLEET_BROKER_ADDR` serves **both** fleet transports: the credential
broker (`/internal/fleet/credentials/…`) and the remote `coreapi` transport
(`/internal/fleet/coreapi/…`). They share one address and one token
deliberately. A `role=send` worker needs both at once — it brokers the
credential it dials with *and* it checks suppression before every send — so two
tokens would partition nothing while doubling what you have to rotate, and the
credential token is already the more powerful of the two. Rotate
`INROAD_FLEET_BROKER_TOKEN` and both are revoked.

#### Remote coreapi (worker side)

`INROAD_FLEET_COREAPI_REMOTE=true` moves a `role=send` worker's `coreapi` reads
from its own `pgxpool` onto the fleet channel. Today it moves **one method**:
the suppression check every send makes. The worker still opens a pool for
everything else, so this is the first step toward a worker that cannot read the
tenant database at all — not the finished thing. Leave it off unless you are
deliberately exercising that path.

What it enforces at startup:

- On any role **but** `send` → refuses to start. A `control` worker runs beside
  the API and a single-process (`all`) worker *is* the database's host; a
  network hop to reach it would be pure latency, and silently ignoring the
  setting would leave you believing your worker had stopped reading the
  database.
- Set **without** `INROAD_FLEET_BROKER_URL` → refuses to start.
- A worker that cannot reach the control plane **refuses to send**. It never
  falls back to a local read and never assumes "not suppressed": a false
  negative here is mail delivered to someone who opted out.
- There is **no cache**. Every check is a direct call, because a TTL on a
  suppression answer is a correctness decision, not a tuning knob.

#### What this does and does not contain

Worth stating precisely, because the obvious stronger claim is false.

**It removes the offline capability.** A stolen worker disk, container image or
environment file now decrypts nothing, ever. Access becomes revocable — rotate
`INROAD_FLEET_BROKER_TOKEN` instead of re-encrypting every DEK in the
installation — and every open is a request the control plane sees and can log.
Refresh tokens never leave the control plane at all: a worker receives only a
short-lived access token for one API call.

**It does not yet shrink what a LIVE compromised worker can reach.** Every
`send` worker consumes the shared `send` queue and may legitimately be handed a
job for any mailbox — only `warmup:tick` is routed by assignment — so the broker
has to answer for any mailbox the token names. Scoping a worker to only the
mailboxes actually routed to it needs per-worker identity *and* per-mailbox
routing; identity now exists (see [Worker identity](#worker-identity-and-egress-ip)
below), per-mailbox routing does not. The token is also shared across the fleet,
so the broker cannot tell which host is asking and revoking one revokes all.
Treat a `send` host as able to reach any mailbox in the installation while it is
running, and firewall the broker listener accordingly.

### Worker identity and egress IP

These two variables are what make a `send` host a distinct *sending identity*
rather than just more throughput. Neither is needed for the single-process
self-host default.

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_WORKER_EGRESS_IP` | Source address every outbound provider dial binds to (`net.Dialer.LocalAddr`) — SMTP and IMAP, and the Gmail and Microsoft Graph API clients alike. Set it to the host's own public address on a multi-IP fleet. Sets the **source** only — it never relaxes the SSRF destination vet | unset (OS default route) |
| `INROAD_WORKER_ID` | Pins this worker's id explicitly, overriding derivation. It keys the `workers` heartbeat row and names the worker's affinity queue `w:<id>` | unset (derived) |

**Identity is derived from the host's public IP, not its hostname.** Reputation is
per-IP: a host that is reinstalled keeps its IP and should keep its mailbox
affinity, while a host that gets a new IP is genuinely a new sender and must not
inherit the old one's assignments. A container hostname, by contrast, changes on
`--force-recreate` and silently strands the affinity queue keyed on it.

Resolution order:

1. `INROAD_WORKER_ID`, if set — an explicit operator pin always wins.
2. `role=all` (the self-host default): the OS hostname, exactly as before per-IP
   identity existed.
3. Any other role (`control`/`send`): a UUIDv5 of the host's first globally
   routable address, IPv4 preferred, in the fixed RFC 4122 DNS namespace
   `6ba7b810-9dad-11d1-80b4-00c04fd430c8`.

Because the namespace and algorithm are standard, a provisioning script can compute
the same id independently:

```bash
uuidgen --sha1 --namespace 6ba7b810-9dad-11d1-80b4-00c04fd430c8 --name 192.0.2.10
```

A host behind NAT with no public address on any interface falls back to the
hostname rather than failing to start — a self-hoster behind NAT still has to be
able to run Inroad. The `workers` row records which source was used (`ipv4`,
`ipv6`, `hostname`, `override`) so you can tell a NAT'd worker from an IP-derived
one without cross-referencing logs.

:::caution[Changing a host's role or IP strands its old assignments]
`mailbox_worker_assignments` rows survive a role change, and they stay
authoritative while the host's last heartbeat is inside the 15-minute live window.
A host switched to `control` no longer consumes `w:<id>`, so warmup ticks routed
to it sit on a queue nothing reads — no error, no retry, no dead-letter row. Give
the host a new `INROAD_WORKER_ID` at cutover, or delete its assignment rows.
Rotation's `unreachable` tier will eventually move those mailboxes once the host
falls out of the live window, but only if another live worker exists.
:::

### Watching the fleet

Fleet placement, rotation and provider signals are all visible in the console under
**Settings → Fleet** (`/app/settings/fleet`), which is **admin-session-only** — an
API key or OAuth token cannot reach it. It shows each worker carrying your
mailboxes, the provider verdicts recorded against it, the periodic job ledger, and
the per-mailbox decision log explaining why a mailbox sits where it does.

The decision log is the thing to read first when a mailbox is not sending: it
records every `assign`, `rotate` and `refused` with the reason. A `refused` entry
means every live worker has recently been blocked or unreachable for that mailbox's
provider — add fleet capacity or wait for the block to clear.

Rotation runs on the `control` queue every 5 minutes and is deliberately reluctant:
it moves a mailbox only when the worker it is on is unreachable or the provider is
refusing it, or (rarely) when a destination beats the incumbent by a wide margin
after 12 hours of residency. At most 20 moves per tick, 5 per destination. A fleet
with one live worker never rotates at all.

## Database connection budget

| Variable | Description | Default |
| :--- | :--- | :--- |
| `INROAD_DB_MAX_CONNS` | Maximum pgx pool connections **per process** | `25` |
| `INROAD_DB_MIN_CONNS` | Warm (idle) pool connections kept open per process | `4` |

The defaults are exactly the sizing the pool used before these variables existed,
so upgrading changes nothing for an existing deployment.

Both are **per process**, and the constraint that matters is cluster-wide:

```
replicas × INROAD_DB_MAX_CONNS  +  headroom  ≤  Postgres max_connections
```

Postgres defaults to `max_connections = 100`, and Postgres reserves some of those
for superuser connections (`superuser_reserved_connections`, 3 by default), so
leave real headroom for migrations, `psql`, backups and monitoring. At the stock
`25`, **four processes — three workers plus one API — reach 100 exactly.** The
symptom is not an error: `pool.Acquire` simply blocks, and the deployment
presents as "everything is slow".

Worked examples:

| Deployment | Setting | Total |
| :--- | :--- | :--- |
| 1 API + 1 worker (default self-host) | `25` (default) | 50 of 100 |
| 1 API + 3 workers | `INROAD_DB_MAX_CONNS=18` | 72 of 100 |
| 2 API + 6 workers | `INROAD_DB_MAX_CONNS=18`, raise Postgres `max_connections` to 200 | 144 of 200 |

Two levers, and prefer them in this order: **lower the per-process pool** to fit
the budget, or **raise Postgres `max_connections`** (each connection costs real
server memory, so raise it deliberately alongside `shared_buffers` and
`work_mem`). Past roughly five replicas the right answer is a connection pooler
(PgBouncer in transaction mode) in front of Postgres rather than either lever.

Invalid values fail at **startup**, not at the first query: a non-positive max, a
negative min, or a max below the min is rejected with the offending numbers.

A DSN that pins the pgxpool keys still overrides both variables. The full
precedence is:

```
pool_max_conns / pool_min_conns in INROAD_DATABASE_URL
  >  INROAD_DB_MAX_CONNS / INROAD_DB_MIN_CONNS
  >  built-in defaults (25 / 4)
```

The two keys resolve independently, so pinning only `pool_max_conns` in the DSN
leaves the minimum to the environment variable.

## Variables that are not application configuration

These appear in the deployment manifests and will show up in a `grep`, but
`config.Load` never reads them. They are listed so their absence above is not
mistaken for an omission:

| Variable | Read by | Purpose |
| :--- | :--- | :--- |
| `SECRETS_FILE` | `deploy/docker/load-secrets.sh` | Path the entrypoint sources generated secrets from when `INROAD_JWT_SECRET` / `INROAD_MASTER_KEY` are not already set. Defaults to `/run/secrets/inroad/env`. This is also why [`inroadctl`](/deploy/operator-cli/) needs an extra step on the zero-config compose stack |
| `INROAD_TEST_DATABASE_URL` | `internal/platform/db/dbtest` | Database the integration tests connect to. Never read by a running binary |
| `POSTGRES_USER`, `POSTGRES_PASSWORD`, `POSTGRES_DB`, `POSTGRES_PORT`, `REDIS_PORT`, `API_PORT`, `INROAD_IMAGE_REPO`, `INROAD_VERSION` | the compose manifests | Interpolated by Docker Compose to build image tags, port bindings and the default `INROAD_DATABASE_URL` |
