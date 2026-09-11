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
| `INROAD_RUN_SCHEDULER` | Whether **this** worker process runs the periodic scheduler | `true` |
| `INROAD_WORKER_ROLE` | Which half of the worker this process runs — `control`, `send`, or unset for both. See [Splitting control and send roles](#splitting-control-and-send-roles) before using it in production | unset (`all`) |

The default of `10` is sized for small deployments. Every per-mailbox send and
inbox-poll task shares this pool, so with many active mailboxes the queue backs
up behind it and sending looks slow even though nothing is wrong. As a rule of
thumb, raise it to at least `25` once you run **50 or more active mailboxes**
on one worker node.

When raising concurrency, keep the database pool larger than worker concurrency
plus headroom for the periodic sweepers and HTTP handlers — see
[Database connection budget](#database-connection-budget) below.

### The scheduler must be a singleton

The worker binary also runs the asynq **scheduler**, which enqueues the periodic
reconcile sweeps (enrollments, inbox, warmup, domain auth, recipient ESP,
maintenance cleanup).

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

By default a worker process runs everything: the scheduler, the six periodic
sweeps above, and every per-message handler (campaign sends, warmup ticks and
engagement, inbox polls, manual replies, test sends, webhook deliveries). This
is the self-host topology — one process, one trust domain, nothing to
configure.

`INROAD_WORKER_ROLE` splits which handlers a process registers into two
halves, **and which queues it consumes follows the same split** — that second
half used to be missing, which is why this section used to warn you off using
it. It doesn't any more; the topology below is operable.

- **`control`** — the scheduler and the six periodic sweeps/purges. These scan
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

And one limit holds regardless of whether the queues route correctly: it is
an operational lever, not a security boundary. A `send`-role process is
*logically* restricted to per-message handlers (it simply never registers the
cross-tenant handlers), but it still holds the same database connection as an
`all` process, so it isn't *physically* prevented from reaching the rest of the
schema.

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
