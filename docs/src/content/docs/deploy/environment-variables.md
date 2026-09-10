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
| `INROAD_WORKER_ROLE` | Which half of the worker this process runs — `control`, `send`, or unset for both. **Leave unset:** the split is [not operable yet](#splitting-control-and-send-roles) | unset (`all`) |

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

`INROAD_WORKER_ROLE` splits which handlers a process registers into two halves.
It is **groundwork, not a deployment topology you can use yet** — read the
warning below before setting it.

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

:::caution[The two-host topology is not operable yet]
**Do not split the roles in production.** The flag works — a `control` host
really does register only the sweeps, and a `send` host only the per-message
handlers — but **nothing else in the system is role-aware yet, and splitting
today loses mail.**

Two pieces are still missing:

- **Both roles consume the same queues.** A worker's queue set is
  `{"w:<worker-id>", "default"}` regardless of its role.
- **Almost nothing is enqueued to a role-specific queue.** Only `warmup:tick`
  is routed to a particular worker's `w:<id>` queue. `sequence:advance`,
  `inbox:poll`, `webhook:deliver`, the manual-send tasks and every sweep all
  land on the shared `default` queue.

A queue consumer takes a task off the queue *before* anything checks which
handler will run it. So on a split deployment every host competes for the same
`default` queue and each one regularly wins a task it cannot run — the
`control` host picks up an `inbox:poll` or a manual reply, a `send` host picks
up a sweep. The host finds no handler registered for the task and fails it,
that failure retries **on the same queue** where a wrong host can claim it
again, and after the attempts run out the task is dead-lettered. It is never
re-routed to the host that could have done it, and nothing about it looks like
a misconfiguration: it presents as mail that quietly never sent.

Adding more `send` hosts makes this **worse**, not better: each additional
host raises the share of sweeps that land on a host with no sweep handler.

**The supported way to scale out today is unchanged:** run N workers with the
role unset (`all`), and set `INROAD_RUN_SCHEDULER=true` on exactly one of them
(see [The scheduler must be a singleton](#the-scheduler-must-be-a-singleton)).
`INROAD_WORKER_ROLE` is groundwork for a later change that makes queue
consumption and task routing role-aware; until that lands, the flag has no
topology to be correct in.
:::

If you do split roles in a test environment, note one more sharp edge: the
heartbeat gate only stops a `control` host being assigned *new* mailboxes, and
does nothing about mailboxes it already owns. A host that previously ran as
`send` keeps its `mailbox_worker_assignments` rows, and those rows stay
authoritative while its last heartbeat is inside the assigner's 15-minute live
window — so every warmup tick for those mailboxes is routed to a host with no
handler for it. **Give a host a new `INROAD_WORKER_ID` when you change it to
`control`, or delete its `mailbox_worker_assignments` rows at cutover.**

Splitting roles is also an operational lever, not yet a security boundary: a
`send`-role process is *logically* restricted to per-message handlers (it
simply never registers the cross-tenant handlers), but it still holds the
same database connection as an `all` process, so it isn't *physically*
prevented from reaching the rest of the schema.

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
