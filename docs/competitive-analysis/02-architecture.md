# 02 — Architecture Deep-Dive

Where the Reference platform is architecturally stronger than Inroad, what it
actually buys, and — honestly — whether Inroad should **replicate it**, **do it
better**, or **skip it** at our stage. The reflexive answer "they're bigger so
they're right" is wrong here: several of these patterns are correct at their scale
and premature at ours.

A framing fact repeated throughout: the Reference platform ships **two default
stacks**. Its Go factory functions default to a *cloud* stack (Kafka + Avro +
AWS-KMS + S3 + GCP Cloud Tasks), while its shipped compose file overrides
everything to an *all-local* stack (NATS + JSON + local-AES + filesystem +
in-process poller). Every dependency sits behind an interface with a `FromEnv`
factory, so the same binaries run either way. That indirection is the core of what
makes them "swappable," and it's the main thing worth learning from.

---

## 1. Control plane / execution plane split — *physical* vs Inroad's *logical*

**What they do.** Three long-running binaries:
- **backend** (control) — REST API, auth, orchestration; opens Postgres, runs migrations.
- **consumer** (control) — subscribes to worker/tracking events, writes analytics/suppression/warmup-health to Postgres; runs the sweep loops.
- **worker** (execution) — sends/syncs mail; **holds no Postgres connection at all**. It reaches the two bits of relational data it needs (per-org decrypted DEK, and the message-ID→internal-email map) over the backend's internal HTTP API with a bearer token. Each worker subscribes only to its own per-worker topic on the event bus.

The rationale is specifically about cold-email reputation: reputation lives at the
**IP** level, so the unit of identity is the VPS/IP, not a pod. A DB-less worker on
an untrusted VPS can't leak the whole tenant database if compromised, and you scale
by adding workers, not DB connections.

**Inroad today.** We have the *interface* (`internal/coreapi` — the worker package
depends only on `coreapi.Client`), but the in-process implementation opens a pgx
pool, so `cmd/worker` **does hit Postgres at runtime**. The split is logical, not
physical.

**Verdict: Do-better, but DEFER the physical cut.** Our `coreapi` seam is already
the right design — arguably cleaner than theirs, because it's one interface instead
of two ad-hoc HTTP repositories. The physical split (swap the in-process impl for
an HTTP impl, take Postgres off the worker) becomes worth doing **only when we run
workers on untrusted/multi-IP hosts**. Until then it's pure cost. When we do it,
we already have the seam; it's an implementation swap, not a redesign. **Keep the
seam honest in the meantime** (no `platform/db` imports creeping into worker code).

---

## 2. Provider-swappable infrastructure — the `FromEnv` factory pattern

**What they do.** A single `internal/config/providers.go` plus a `FromEnv` factory
per subsystem selects an implementation by env var (and, for the CGO-heavy Kafka/
Avro path, a build tag). The matrix: event bus (NATS/Kafka), codec (JSON/Avro),
KMS (local-AES/AWS), blob storage (filesystem/S3-compatible), scheduler
(Postgres-poller/GCP Cloud Tasks), billing (none/Stripe), captcha (none/Turnstile),
realtime transport (Redis/GCP Pub/Sub), push (off/APNs). A fully-local deploy skips
loading the AWS SDK entirely.

**Verdict: Do-better, selectively.** The *pattern* (interface + `FromEnv`) is
excellent and cheap, and Inroad already uses it in spirit (`mail.ConnectionTester`,
`coreapi.Client`, `notify.Sender` console/smtp). Adopt it **only where a second
provider is real**:
- **Blob storage (filesystem / S3-compatible)** — worth adding *before* we build
  attachments, tracking assets, or email-body offload. Small interface, real payoff.
- **KMS (local / cloud)** — see §4; add the interface now, cloud impl later.
- **Event bus, codec, scheduler-as-Cloud-Tasks** — **Skip.** We have asynq/Redis;
  a swappable event bus with an Avro/schema-registry path is solving a problem we
  don't have. Their own compose defaults away from it.

The trap to avoid: adding a factory with exactly one implementation is just
indirection. Add the seam when the second implementation is on the roadmap, not
speculatively.

---

## 3. Event bus / messaging

**What they do.** A transport-only `EventBus` interface (`Publish`/`Subscribe`,
at-least-once, manual ack) with NATS JetStream (default, pure-Go) and Kafka (behind
a build tag) implementations; serialization is a separate `codec` axis (JSON or
Avro+Confluent-Schema-Registry). Notably, large email bodies are **stored in blob
storage and only an S3 key + encrypted subject ride the bus** — the event contract
stays small and fixed.

**Verdict: Skip the abstraction; steal one idea.** Inroad's asynq-over-Redis queue
is the right tool for our scale and far simpler. A generic event bus with a schema
registry is over-engineering for us today. **The one idea worth keeping:** don't
put large payloads on the queue — pass references (IDs) and let the worker fetch,
which we already do via `coreapi`. Their doc set even admits the event-bus docs are
stale relative to the code; treat the whole area as "interesting, not for us yet."

---

## 4. Encryption & key management — per-org DEK + KMS

**What they do.** Envelope encryption with a **per-organization data-encryption key
(DEK)**: a KMS provider (local AES master key, or AWS KMS) wraps each org's 32-byte
DEK; the plaintext DEK is cached briefly in Redis; field values are sealed
AES-256-GCM under the org DEK. Platform-level secrets (worker SSH keys, profile
secrets) are sealed under a zero-UUID "platform" DEK. Switching KMS providers means
re-encrypting every DEK, so the active backends are recorded read-only.

**Inroad today.** A single local AES-256-GCM master key (`crypto.Sealer`) seals all
mailbox secrets. No per-tenant keys, no KMS. Our security doc already lists
"KMS-backed per-tenant keys" as deferred.

**Verdict: Do-better — adopt the DEK indirection, keep it simple.** This is the one
place their design is cleanly ahead and worth moving toward, because it's a
**security capability, not just plumbing**:
- Introduce a per-workspace DEK wrapped by a `KeyProvider` interface. Default
  provider = our existing local master key (so nothing changes operationally for
  self-hosters). This gives per-tenant crypto isolation and makes a future cloud
  KMS a drop-in.
- **Do it better than them on assurance:** their `cipher`/`auth` are untested; ours
  should ship with unit + integration tests (our test density is our edge — keep it).
- Respect their hard-won lesson: **never overwrite an existing DEK** (it silently
  invalidates all prior ciphertext) — make `Put` fail-if-exists, rotation = explicit
  re-encrypt.

Effort is moderate and it's mostly mechanical given `crypto.Sealer` already exists.
See doc 03.

---

## 5. Scheduling

**What they do.** Postgres is the schedule store *and* the clock: every send is a
`tasks` row with `scheduled_at`; a `tasksched` interface has two providers — a
default **in-process poller** (`SELECT … due; dispatch; handlers idempotent`,
"Postgres is both the store and the clock") and an opt-in **GCP Cloud Tasks** impl
that POSTs a webhook at fire time. Multi-replica safety via idempotent handlers and
`FOR UPDATE SKIP LOCKED`.

**Verdict: Replicate the local model (we're already close); skip Cloud Tasks.**
Inroad's asynq scheduled tasks + sweepers already are "Postgres/Redis as the clock."
Their pure-Postgres poller is actually a *simpler* dependency than our Redis+asynq,
but not enough to justify a migration. **Skip the Cloud Tasks path entirely** — it's
a cloud coupling their own default avoids. If we ever want to drop the Redis
dependency, the `SELECT … due FOR UPDATE SKIP LOCKED` poller is the pattern to copy.

---

## 6. Outbound safety (SSRF guard) — near parity

**What they do.** A hardened HTTP client for all user-supplied URLs (webhooks,
automation HTTP actions): validates at **dial time** (closes DNS-rebinding),
refuses if *any* resolved IP is non-public, dials the validated IP directly, keeps
the hostname only for TLS SNI, denylists cloud-metadata hostnames, caps + re-checks
redirects, allowlists ports 443/8443.

**Verdict: Already at parity — extend ours when we add webhooks.** Inroad's
`mail.vetAddr` SSRF guard for SMTP/IMAP does exactly this class of thing (blocks
loopback/link-local/metadata/multicast, dials resolved IP, port allowlist). When we
add **outbound webhooks** (doc 03), generalize the same guard to HTTP/HTTPS with a
443/8443 allowlist and redirect re-validation. No new concepts, just reuse.

---

## 7. Observability — thinner than you'd expect

**What they do.** Sentry for errors + `zerolog` structured logging + **domain**
health in Postgres (workers emit a health event every 30s; a materialized
`worker_capacity_view` refreshes each minute). No OpenTelemetry tracing, no
Prometheus metrics pipeline. (They also carry *two* logging libs — a minor
inconsistency.)

**Verdict: Do-better, cheaply.** This is *not* a place they're ahead — their o11y is
Sentry + logs + DB views. Inroad already has structured `slog`. The cheap wins that
would put us ahead: add a `/metrics` (Prometheus) endpoint on the API and worker
(send counts, queue depth, poll latency), and optional Sentry (or any error sink)
behind our config pattern. Small effort, and better than what they ship.

---

## 8. Deployment & scaling model

**What they do.** Control plane in one region; execution plane a fleet of
one-worker-per-VPS across providers/IPs. Worker identity is derived deterministically
from the bound public IP (`UUIDv5(namespace, ip)`) so reputation persists across
reinstalls; outbound dialers are bound to the assigned source IP; workers come in
tiers (free/premium/dedicated) with risk-segregated mailbox assignment; SSH-driven
install/update; autoscaler + Hetzner provisioning (the last is force-dry-run even in
their code — not actually wired).

**Verdict: Mostly Skip / Defer — this is scale we don't have.** The per-VPS fleet,
tiers, autoscaler, and SSH orchestration are a real business's operational answer to
running *thousands* of mailboxes across many IPs. For Inroad (single-node,
self-hosters bring their own mailboxes that send via the provider's own IP), this is
premature by a wide margin. **Two ideas worth keeping in the back pocket:**
- **Deterministic worker identity from IP** — trivially cheap, useful the moment we
  run >1 worker.
- **Egress source-IP binding** (`net.Dialer.LocalAddr`) — relevant only if we ever
  host sending IPs ourselves; note it and move on.

Their auto-provisioning being permanently dry-run is a useful reminder: even they
haven't finished the ambitious parts.

---

## 9. The polyglot question

**What they do.** Go control plane + **Rust** tracking service (Axum, the hot
open/click edge) + **Elixir/Phoenix** realtime service (presence + WebSocket
fan-out) + **Swift** iOS app. Best-tool-per-job, paid for in operational surface
(four toolchains, four CI lanes, four hiring profiles).

**Verdict: Skip — this is Inroad's deliberate advantage.** Our PRD's "one stack,
deeply understood; split languages only when production data proves a bottleneck" is
the right call at our stage, and it's genuinely cheaper to run. Both features that
justify their extra languages can be built in Go at our scale:
- **Open/click tracking** — a small Go HTTP service (or an endpoint on the API)
  with an in-memory dedup cache. We don't need Rust for our volume.
- **Realtime** — Server-Sent Events or a `nhooyr/coder` WebSocket hub in Go covers
  live campaign stats and a unified-inbox badge without standing up Elixir.

Reconsider a separate language only when a *measured* hot path demands it.

---

## Scorecard

| Pattern | Are they ahead? | Inroad verdict |
|---|---|---|
| Control/execution split | Physically, yes | **Do-better / Defer** — seam exists, cut later |
| `FromEnv` provider swapping | Yes (breadth) | **Do-better, selectively** — blob + KMS now, rest skip |
| Event bus + schema registry | Yes (for their scale) | **Skip** — asynq/Redis is right for us |
| Per-org DEK + KMS | **Yes, cleanly** | **Do-better** — adopt DEK, keep local default, out-test them |
| Postgres-as-clock scheduling | Comparable | **Skip migration** — copy poller only if we drop Redis |
| SSRF guard | Parity | **Reuse** — extend to webhooks |
| Observability | **No** (thin) | **Do-better** — add `/metrics`, error sink |
| Per-VPS fleet + autoscaler | Yes (their scale) | **Skip / Defer** — premature for single-node |
| Polyglot services | Trade-off, not strictly ahead | **Skip** — one Go stack is our edge |

**Bottom line:** the only architectural item worth prioritizing *for its own sake*
is the **per-org DEK + KMS-provider seam** (§4). Everything else is either already
adequate in Inroad, or should be pulled in opportunistically *when a feature needs
it* (blob storage for tracking/attachments, SSRF extension for webhooks, `/metrics`
for ops) rather than as standalone infrastructure work.
