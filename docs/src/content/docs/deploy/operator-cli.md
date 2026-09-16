---
title: Operator CLI (inroadctl)
description: The out-of-band recovery CLI — create a user, reset a password, restore a lost owner, list workspaces and users, and check instance health when sign-in itself is broken.
---

`inroadctl` is the operator CLI (`cmd/inroadctl`). It is the tool you reach for
when the web app cannot help you: nobody can sign in, the only owner account was
deleted or demoted, or the instance is up but you do not yet know what is wrong
with it.

**It talks to Postgres directly, never through the HTTP API.** That is the whole
point of it, not an implementation shortcut — an account-recovery tool that
authenticated against the thing you are locked out of would be useless exactly
when you need it. It does reuse the API's own password-hashing and
user-creation paths (`internal/app/identity`, `internal/app/auth`) rather than
reimplementing them: a second hashing implementation that drifted from the one
the API verifies against would be a security bug, not a convenience.

The trade is that `inroadctl` has **no authentication and no authorization of its
own**. Anyone who can run it can create an owner in any workspace and read every
user's email address. Its access control is the shell it runs in and the database
credentials in that shell's environment — treat the ability to exec into the API
container as equivalent to instance-admin.

## Running it

`inroadctl` is baked into the **API image** (`deploy/docker/Dockerfile.api`
builds it alongside `inroad` and `migrate` into `/usr/local/bin/`), so a
container deployment needs no extra download:

```bash
docker compose exec api inroadctl help
```

It loads the **same `INROAD_*` configuration `cmd/inroad` does** — at minimum
`INROAD_DATABASE_URL`, and `INROAD_JWT_SECRET` (which `config.Load` rejects
below 16 bytes, so an empty one is a hard startup failure even though this
binary mints no tokens). `INROAD_MASTER_KEY` is *not* required — no command here
decrypts anything — but if it is set it must still be valid base64 decoding to 32
bytes, because `config.Load` refuses a malformed key rather than letting a typo
degrade into "this process has no key". `docker compose exec` inherits the
container's environment, so whether that is enough depends on how your stack
supplies its secrets:

| Stack | Command |
| :--- | :--- |
| `deploy/docker/docker-compose.prod.yml` (what `scripts/install.sh` deploys) | `docker compose exec api inroadctl …` — the manifest sets `INROAD_JWT_SECRET` / `INROAD_MASTER_KEY` as container environment from your `.env`, so they are present |
| Repository-root `docker-compose.yml` **with** both secrets set in `.env` or the shell | `docker compose exec api inroadctl …` — same as above |
| Repository-root `docker-compose.yml` using the **generated** secrets volume (the zero-config default) | `docker compose exec api sh -c '. /usr/local/bin/load-secrets.sh && inroadctl …'` |

That last row is the one that surprises people. In the zero-config path the
secrets do not live in the container's environment at all — `init-secrets` writes
them to a volume and `api-entrypoint.sh` sources `load-secrets.sh` before
`exec inroad`. A new process started with `exec` never ran that entrypoint, so it
sees `INROAD_JWT_SECRET` as the empty string the manifest sets and fails with
`inroadctl: config: INROAD_JWT_SECRET must be set and at least 16 bytes`. Source
the same script first and it behaves identically to the other rows.

The **dev stack** (`docker-compose.dev.yml`) does not carry the binary — its
`api` service is the `air` live-reload image, not `Dockerfile.api`. Run it from
source instead:

```bash
docker compose -f docker-compose.dev.yml exec api go run ./cmd/inroadctl help
```

Outside containers, build it like any other binary in this repo:

```bash
go build -o inroadctl ./cmd/inroadctl
```

Remember that the Go binaries do not read `.env` themselves — export it first
(`set -a && . ./.env && set +a`).

## Two rules that apply to every command

**Passwords are never a flag.** There is no `--password`. A credential-changing
command reads it from stdin when stdin is a pipe, and otherwise prompts twice on
stderr with terminal echo suppressed, rejecting a mismatch before it writes
anything. The reason is that `argv` is visible to every other process on the host
(`ps`, `/proc`) and lands in shell history, where a password leaked that way
outlives the command that typed it by months. The floor is **8 characters**,
matching the `min=8` the HTTP register and reset-password bodies enforce.

**Destructive and credential-changing commands confirm.** `create-user`,
`set-password` and `grant-role` print what they are about to do and wait for an
explicit `y`/`yes`; anything else (including an empty line) aborts. `--yes` skips
the prompt for scripted use.

Those two rules interact in a way worth knowing before you script anything:

:::caution[A piped password needs `--yes`]
The confirmation prompt reads from the **same stdin** the password was just read
from. Pipe a password without `--yes` and the prompt gets EOF, which is not
`y`, so the command prints `aborted` — and **exits 0**, because aborting on
request is not an error. Nothing is written, but nothing tells you loudly either.

```bash
# Correct: password on stdin, confirmation waived.
printf '%s\n' "$NEW_PASSWORD" | \
  docker compose exec -T api inroadctl set-password --email you@example.com --yes
```

`-T` disables `docker compose exec`'s TTY, which is what makes stdin a pipe and
selects the read-a-line path rather than the interactive prompt.
:::

## Commands

`inroadctl help` (also `-h`, `--help`) prints the same list. No arguments, or an
unrecognised command, prints usage to stderr and exits 1.

### `create-user`

```
inroadctl create-user --email E [--workspace ID] [--role owner|admin|member] [--yes]
```

Bootstraps an account. `--email` is required; the password is read as described
above.

- **Without `--workspace`** it creates a **brand-new workspace** owned by the new
  user — the same transaction self-serve signup runs (`identity.Service.Register`:
  workspace, user, owner membership and a first session, committed together). The
  workspace is named after the email's local part, so `ops@acme.test` gives a
  workspace called `ops`; rename it in the UI afterwards. A verification email is
  sent **best-effort through whatever `INROAD_TRANSACTIONAL_DRIVER` is configured**
  — on the default `console` driver that is a log line and nothing else, and a
  send failure never fails the account creation. `--role` is rejected here unless
  it is `owner`: a new workspace's first user is always its owner.
- **With `--workspace <uuid>`** it adds the user to an **existing** workspace at
  `--role` (default `owner`), via `identity.Service.CreateMember`. This path
  mints no session and sends no verification email.

An unknown workspace id, an invalid UUID, a role outside
`owner`/`admin`/`member`, and an email that already exists each produce their own
message rather than a raw database error — the duplicate-email case is detected
as a Postgres unique violation (SQLSTATE 23505) by type, not by string matching.

### `set-password`

```
inroadctl set-password --email E [--yes]
```

Overwrites a forgotten password **and revokes every existing session** for that
account in the same transaction, then reports how many it revoked. The revocation
is deliberate: a recovery that left a possibly-compromised session alive would not
be a recovery. The new password is hashed through the same argon2id path the API
uses. An unknown email is refused by name, with a pointer to `inroadctl users`.

This does not clear anything else about the account — it changes the password and
kills sessions, nothing more.

### `grant-role`

```
inroadctl grant-role --email E --workspace ID --role owner|admin|member [--yes]
```

Grants a role in a workspace, **adding the membership if the user has none** and
updating it in place if they do. All three flags are required. This is the
primitive for restoring a lost owner whichever way the access was lost — demoted,
or removed from the workspace entirely. On success it prints the workspace's name
alongside its id, which is the cheapest confirmation that you targeted the
workspace you meant to.

### `workspaces` and `users`

```
inroadctl workspaces
inroadctl users
```

Every workspace (id, name, whether onboarding completed, created) and every user
(id, email, whether the email is verified, created) on the instance, newest
first, as a plain aligned table; an empty instance prints `no workspaces` /
`no users`. Neither takes flags. These are how you find the UUID the two commands
above want.

`users` never prints a password hash. The row builder simply does not read that
field, so it cannot reach the terminal or your shell history even though the
underlying row carries it.

### `status`

```
inroadctl status
```

A one-screen instance health check. It is the only command that does **not**
share the fail-fast database connect the others use — a database that is down is
precisely the condition it exists to report, so each probe runs independently
(the connect, worker-count and Redis probes each bounded by a 5-second timeout)
and one failure does not suppress the rest:

- **database** — reachable, or `UNREACHABLE` with the connection error.
- **migrations** — the applied version, and whether the schema is `DIRTY` (a
  migration that failed partway and needs manual repair). Only attempted when the
  database answered.
- **workers** — how many worker rows are registered and how many heartbeated
  within the last **15 minutes**. Zero live is reported as a failure, with the
  consequence spelled out: sends, warmup and inbox polling are not running.
- **redis** — reachable, or `UNREACHABLE` with the error.

It ends with `status: healthy` or `status: UNHEALTHY`, and **exits non-zero when
anything failed**, so it is usable as a monitoring probe and not only as
something to read.

## Recipes

**Nobody can sign in.** Reset a known account and use the printed session count
to confirm you hit the right one:

```bash
docker compose exec api inroadctl users
docker compose exec api inroadctl set-password --email you@example.com
```

**The only owner was demoted or removed.** Restore the role directly — the user
does not need to still be a member:

```bash
docker compose exec api inroadctl workspaces
docker compose exec api inroadctl grant-role \
  --email you@example.com --workspace <workspace-uuid> --role owner
```

**A fresh install, and you want the first account from the shell** rather than
through the signup form — this is what `scripts/install.sh` prints as its final
step:

```bash
docker compose exec api inroadctl create-user --email you@example.com
```

**Something is wrong and you do not know what.** Start here; it distinguishes
"the database is gone" from "the worker is dead" from "a migration is half
applied" in one command:

```bash
docker compose exec api inroadctl status
```

## What it deliberately does not do

- **No mailbox, campaign, or credential operations.** It touches identity and
  reads health. It never decrypts a mailbox credential, so it needs no working
  keyring for any of the commands above.
- **No delete.** There is no `delete-user` or `delete-workspace`. Removing a
  tenant is not a recovery operation, and the destructive-by-accident cost is
  much higher than the demoted-owner case `grant-role` covers.
- **No export or import.** There is no workspace export or import subcommand.
- **It does not prove a healthy instance is a working one.** `status` probes
  dependencies, not delivery: it can print `healthy` while every campaign is
  paused by the deliverability circuit breaker.
