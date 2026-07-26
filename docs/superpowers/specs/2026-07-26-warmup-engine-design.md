# Warmup Engine — Design Spec (v1)

Status: approved for build · Owner: platform · Date: 2026-07-26

The warmup engine builds and maintains mailbox sender reputation by exchanging
low-volume, human-looking mail between a workspace's own opted-in mailboxes,
engaging with that mail the way a real recipient would (read, reply, rescue from
spam), and tracking each mailbox's inbox-placement health so its ramp adapts to
how it is actually landing. It reuses the existing send seam, SSRF guard,
claim-before-send idempotency, coreapi boundary, and workspace-pinning — it does
not reinvent them.

> Scope decisions (locked): **content** = curated static library behind an
> injected `ContentGenerator` seam; **engagement** = full in v1 (reply, mark-read,
> rescue-from-spam); **pool** = workspace-local (mailboxes only warm other
> opted-in mailboxes in the same workspace).

---

## 1. Concepts

- **Warmup participant** — a mailbox the user has opted into warmup. Stored as a
  row in `warmup_participants` (per mailbox, workspace-pinned). Carries the ramp
  config, current daily target, health state, and enabled flag.
- **Pool** — the set of *enabled, healthy* participants in one workspace. Never
  crosses workspaces. A workspace needs ≥2 participants for warmup to do anything;
  with <2 the engine idles (surfaced in the UI).
- **Warmup thread** — a synthetic conversation between two participants. Rooted at
  a first message; replies advance it. Stored in `warmup_threads` +
  `warmup_messages` so reply simulation is stateful and looks natural.
- **Warmup send** — one outbound warmup email (new thread or a reply). Has its own
  claim-before-send lifecycle in `warmup_sends`, exactly like campaign `sends`.
- **Engagement** — the recipient-side actions on a received warmup message:
  rescue-from-spam, mark-read, and (probabilistically) reply-in-thread.
- **Health** — per-participant inbox-placement + behavior signals that produce a
  state: `healthy → watch → throttled → paused`. State dampens the ramp and, at
  `paused`, halts sending until it recovers.

## 2. Architecture placement

Control plane (new domain `internal/app/warmup/`) owns all state and policy.
Execution plane (`internal/worker/warmup/`) sends and engages, reaching data ONLY
through `coreapi.Client` — same rule as every other worker. No new worker→DB path.

```
control plane                          execution plane
─────────────                          ───────────────
internal/app/warmup/                   internal/worker/warmup/
  store.go   (Store + PgStore)           send.go    (warmup:tick handler)
  service.go (pool/health/policy)        engage.go  (warmup:engage handler)
  handler.go routes.go dto.go            sweep.go   (warmup:sweep handler)
                                         thread receipt hook in worker/inbox/
coreapi.Client (new methods) ◀──────────┘   reaches warmup data only here
internal/platform/mail/  (+ Engager seam: IMAP/Gmail modify)
internal/platform/warmup/ (content library + ContentGenerator seam, token)
internal/platform/db/migrations/000017_warmup.up.sql
```

## 3. Data model — migration `000017_warmup`

All tables workspace-pinned; all FKs `ON DELETE CASCADE` from `workspaces` and
`mailboxes` so deleting either crypto-shreds/cleans warmup state.

```sql
-- one row per opted-in mailbox
CREATE TABLE warmup_participants (
    mailbox_id        UUID PRIMARY KEY REFERENCES mailboxes(id) ON DELETE CASCADE,
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    enabled           BOOLEAN NOT NULL DEFAULT true,
    -- ramp
    start_volume      INT NOT NULL DEFAULT 4,     -- emails/day at day 0
    max_volume        INT NOT NULL DEFAULT 40,    -- ceiling
    ramp_increment    INT NOT NULL DEFAULT 2,     -- +N/day
    reply_rate        REAL NOT NULL DEFAULT 0.30, -- P(a send is a reply)
    -- runtime
    started_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    health_state      TEXT NOT NULL DEFAULT 'healthy'
                       CHECK (health_state IN ('healthy','watch','throttled','paused')),
    health_reason     TEXT NOT NULL DEFAULT '',
    paused_until      TIMESTAMPTZ,                -- set when throttled/paused
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX warmup_participants_ws ON warmup_participants(workspace_id) WHERE enabled;

-- a synthetic conversation between two participants
CREATE TABLE warmup_threads (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    sender_mailbox    UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    partner_mailbox   UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    subject           TEXT NOT NULL,
    root_message_id   TEXT NOT NULL DEFAULT '',   -- Message-ID of first message
    turn              INT  NOT NULL DEFAULT 0,     -- messages exchanged so far
    content_key       TEXT NOT NULL,               -- which library thread drives it
    last_activity_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX warmup_threads_partner ON warmup_threads(workspace_id, partner_mailbox, last_activity_at);

-- claim-before-send lifecycle for one warmup email (mirrors sends)
CREATE TABLE warmup_sends (
    id                UUID PRIMARY KEY,            -- deterministic (see §6)
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    thread_id         UUID NOT NULL REFERENCES warmup_threads(id) ON DELETE CASCADE,
    from_mailbox      UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    to_mailbox        UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    is_reply          BOOLEAN NOT NULL DEFAULT false,
    status            TEXT NOT NULL DEFAULT 'queued'
                       CHECK (status IN ('queued','sending','sent','failed','skipped')),
    message_id        TEXT NOT NULL DEFAULT '',
    token             TEXT NOT NULL,               -- HMAC receipt token (see §7)
    claimed_at        TIMESTAMPTZ,                 -- lease
    sent_at           TIMESTAMPTZ,
    last_error        TEXT NOT NULL DEFAULT '',
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- receipt + placement of a warmup message that arrived in the partner's inbox
CREATE TABLE warmup_receipts (
    id                UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    warmup_send_id    UUID REFERENCES warmup_sends(id) ON DELETE SET NULL,
    recipient_mailbox UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    placement         TEXT NOT NULL CHECK (placement IN ('inbox','spam','other')),
    engaged           BOOLEAN NOT NULL DEFAULT false,
    received_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (warmup_send_id, recipient_mailbox)     -- idempotent receipt
);
CREATE INDEX warmup_receipts_health ON warmup_receipts(recipient_mailbox, received_at);
-- note: placement is recorded against the SENDER's reputation via a join on
--       warmup_sends.from_mailbox; the recipient_mailbox is who observed it.

-- rolling daily counters per participant (drives ramp + health, one row/day)
CREATE TABLE warmup_daily_stats (
    mailbox_id        UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    workspace_id      UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    day               DATE NOT NULL,
    sent              INT NOT NULL DEFAULT 0,
    received          INT NOT NULL DEFAULT 0,
    inbox             INT NOT NULL DEFAULT 0,     -- of received, how many in inbox
    spam              INT NOT NULL DEFAULT 0,     -- of received, how many in spam
    replies           INT NOT NULL DEFAULT 0,
    PRIMARY KEY (mailbox_id, day)
);
```

## 4. coreapi additions (the control⇄execution seam)

Worker reaches all warmup data through these. Every method is `workspace_id`
pinned (belt-and-braces on the unguessable UUIDs), same as the existing seam.

```go
// --- warmup send path (warmup:tick) ---

// GetWarmupSendJob picks the next warmup action for a warming mailbox: selects a
// healthy same-workspace partner (recent-partner-avoidance), decides new-thread
// vs reply (participant.reply_rate + open threads), resolves content from the
// library, builds threading headers + a signed receipt token, and loads the
// decrypted transport for from_mailbox. Read-only w.r.t. warmup_sends. Returns
// Skip=true when the mailbox is paused, over today's target, has no eligible
// partner, or is no longer enabled.
GetWarmupSendJob(ctx, mailboxID, workspaceID string) (WarmupSendJob, error)

// ClaimWarmupSend inserts/reclaims the warmup_sends row 'sending' with a lease
// (claim-before-send). Same ClaimOutcome semantics as ClaimStepSend:
// Won / AlreadySent / Skip. The row id is the deterministic id on the job.
ClaimWarmupSend(ctx, job WarmupSendJob) (ClaimOutcome, error)

// MarkWarmupSent finalizes 'sent' + records message_id, advances the thread turn
// (and sets root_message_id on turn 0), and increments warmup_daily_stats.sent —
// in one tx. workspace pinned.
MarkWarmupSent(ctx, job WarmupSendJob, messageID string) error

// ReleaseWarmupSend releases a claimed-but-unsent row after a RETRYABLE failure.
ReleaseWarmupSend(ctx, job WarmupSendJob) error

// FailWarmupSend finalizes 'failed' after a PERMANENT failure (no thread advance).
FailWarmupSend(ctx, job WarmupSendJob, errMsg string) error

// NextWarmupDue returns when this mailbox should send its next warmup email,
// applying ramp target, per-day volume factor, jitter, health dampening, and
// (optional) sending window. Pure policy over daily_stats + participant.
NextWarmupDue(ctx, mailboxID, workspaceID string) (due time.Time, sendNow bool, err error)

// --- receipt + engagement path (inbox poll → warmup:engage) ---

// RecordWarmupReceipt is called by the inbox poller when it detects a warmup
// message (verified token, §7). Upserts warmup_receipts (idempotent on
// (send,recipient)), updates warmup_daily_stats.received/inbox/spam for the
// RECIPIENT's placement observation, and returns an engagement plan describing
// what the recipient should do. A duplicate receipt returns Plan{}, so a
// re-poll never double-engages.
RecordWarmupReceipt(ctx, in WarmupReceiptInput) (WarmupEngagePlan, error)

// GetWarmupEngageJob loads what the engage worker needs to act on one received
// warmup message: recipient transport, the source folder it landed in, the
// thread (for a reply), and a fresh receipt token for the reply. workspace pinned.
GetWarmupEngageJob(ctx, receiptID, workspaceID string) (WarmupEngageJob, error)

// MarkWarmupEngaged sets warmup_receipts.engaged=true (+ replies counter when the
// plan replied) so a retried engage is a no-op. workspace pinned.
MarkWarmupEngaged(ctx, receiptID, workspaceID string, replied bool) error

// --- scheduling fan-out (warmup:sweep) ---

// ListDueWarmupMailboxes returns (mailbox,workspace) for every enabled,
// non-paused participant whose next warmup is due. Consumed by the sweeper.
ListDueWarmupMailboxes(ctx) ([]MailboxRef, error)

// EvaluateWarmupHealth recomputes health_state for participants with recent
// activity (spam-rate / bounce / invalid-token thresholds, §8) and persists
// transitions. Called on the sweep tick.
EvaluateWarmupHealth(ctx) error
```

New job/DTO structs (`coreapi` package) mirror the existing `StepSendJob` style —
decrypted secrets as `[]byte` (zeroized after use), `Provider` transport
discriminator, `AllowPlaintext` threaded through. `WarmupSendJob` carries
`FromMailbox/ToEmail/Subject/BodyText/BodyHTML/InReplyTo/References/Token/
Provider/AccessToken/SMTP*`. `WarmupEngageJob` carries `Provider/AccessToken/
IMAP or Gmail creds/SourceFolder/DoRescue/DoReply/ReplySubject/ReplyBody/
InReplyTo/References/Token`.

## 5. Queue tasks + scheduler (`internal/platform/queue`)

Reuse the existing `warmup:tick` constant/payload (currently a no-op) and add two:

| Task | Trigger | Handler | Idempotency |
|---|---|---|---|
| `warmup:tick` | one warming mailbox is due | `warmup.SendHandler` | `ClaimWarmupSend` row claim; TaskID `warmup:<mailbox>:<dueUnix>` |
| `warmup:engage` | a warmup receipt needs engagement, delayed | `warmup.EngageHandler` | `warmup_receipts.engaged` guard; TaskID `warmupengage:<receiptID>` |
| `warmup:sweep` | scheduler `@every 5m` | `warmup.SweepHandler` | fan-out + health eval; naturally idempotent |

Enqueue helpers: `EnqueueWarmupTickAt(mailboxID, workspaceID, t)`,
`EnqueueWarmupEngageIn(receiptID, workspaceID, d)`. `RegisterWarmupSweep(sch)`
registers the 5-minute sweep. The engage delay is humanized (§8) so reads/replies
don't fire milliseconds after delivery.

## 6. Warmup send flow (`worker/warmup/send.go`) — mirrors `sequence/advance.go`

1. Decode payload → `GetWarmupSendJob`. `defer zeroize` secrets.
2. `Skip` (paused / over target / no partner / disabled) → return nil.
3. `ClaimWarmupSend`: `Skip` → nil; `AlreadySent` → just schedule next; `Won` → send.
4. Build the MIME message from library content (or thread reply), with the signed
   `X-Inroad-Warmup` receipt header/token and threading headers.
5. `sender.Send(...)` through `mail.MultiSender` — the SAME SSRF-guarded,
   TLS-enforced transport campaign sends use. No separate dial path.
6. On success → `MarkWarmupSent` (finalize + advance thread + bump stats), then
   `NextWarmupDue` → `EnqueueWarmupTickAt` (lazy chain, one tick per mailbox).
   Retryable → `ReleaseWarmupSend` + return err. Permanent → `FailWarmupSend` then
   schedule next.

Deterministic send id: `uuidv5(namespace, mailboxID + ":" + dueUnix)` so a retried
tick claims the same row (idempotent), matching the campaign pattern.

## 7. Receipt detection + token (`internal/platform/warmup/token.go`)

- Every warmup send carries `X-Inroad-Warmup: <token>` where `token =
  base64(payload) || HMAC-SHA256(payload, warmupSecret)`, payload =
  `{workspace_id, warmup_send_id, from_mailbox}`. Same HMAC discipline as the
  unsub/tracking tokens. An **unsigned or mismatched header is ignored** — never
  trust the header alone.
- The inbox poller (`worker/inbox/poll.go`) already fetches + parses every inbound
  message. Add an early branch: if `X-Inroad-Warmup` verifies AND the payload's
  `workspace_id` matches the polled mailbox's workspace, the message is warmup —
  call `RecordWarmupReceipt` (placement = which folder it was found in: INBOX vs a
  Junk/Spam mailbox vs other), enqueue `warmup:engage`, and **stop** — do NOT run
  reply/bounce classification on it (a warmup reply must never stop a campaign
  enrollment). This is the one change to the existing inbox path.
- Placement detection: the IMAP poller already knows the folder; for Gmail/Graph
  the label/`SPAM` flag gives placement. v1 detects INBOX vs SPAM; anything else →
  `other`.

## 8. Engagement (`worker/warmup/engage.go`) + `mail.Engager` seam

New seam in `internal/platform/mail`:

```go
type Engager interface {
    // MarkRead clears the unread flag on the message.
    MarkRead(ctx, job EngageTarget) error
    // Rescue moves a message out of the spam/junk folder into the inbox and
    // clears the spam marker ("not spam"). No-op if already in inbox.
    Rescue(ctx, job EngageTarget) error
}
```

- **IMAP** impl: `STORE +FLAGS \Seen`; rescue = `MOVE`/`COPY+EXPUNGE` from Junk→INBOX
  (+ provider "not spam" where available). Dials through the SAME SSRF guard.
- **Gmail** impl: `users.messages.modify` removing `UNREAD` / `SPAM` labels.
- **Graph (M365)**: seam present; v1 returns a graceful `ErrEngageUnsupported`
  logged as skipped (engagement lands for Graph in a follow-up). Sends still work.

Engage handler steps: decode → `GetWarmupEngageJob` → if `DoRescue` and it landed
in spam, `Rescue` → `MarkRead` (after a humanized dwell) → if `DoReply`, build a
threaded reply from the next library turn and `sender.Send` a NEW warmup send
(its own claim + token) → `MarkWarmupEngaged`. All idempotent via the `engaged`
flag.

**Humanization** (all in `internal/platform/warmup/schedule.go`, pure + tested):
- ramp target = `min(max_volume, start_volume + daysWarming*ramp_increment)`.
- per-day volume factor 0.8–1.1 (deterministic from mailbox+day; ~1 lighter day/wk).
- inter-send spacing = `targetSpread/target` × multiplicative jitter (0.6–1.4) so
  sends never land on fixed boundaries.
- engage dwell = heavy-tailed seconds; engagement deferred outside 07:00–22:00
  recipient-local to the next morning (participant timezone; default workspace TZ).

**Health thresholds** (`EvaluateWarmupHealth`, evaluated over a trailing window):
- spam-placement rate > 15% → `watch`; > 30% → `throttled` (halve target,
  `paused_until = now+24h`); > 50% or hard-bounce spike → `paused` (72h).
- invalid/mismatched receipt tokens or tampering (recipient marks warmup as spam)
  count against the sender; sustained → `throttled`.
- recovery: a clean trailing window steps the state back down one level.

## 9. Security invariants (append to `docs/security.md`)

1. Warmup uses the mailbox's existing sealed credential — no new secret storage,
   no new dial path. Warmup sends and engagement dials go through `mail.vetAddr`
   (SSRF guard) and the TLS policy, identical to campaign sends/polls.
2. Warmup is workspace-local: partner selection, receipts, and engagement are all
   `workspace_id`-pinned. A mailbox never receives a partner from, or acts on a
   message belonging to, another workspace. Cross-workspace = zero rows.
3. The `X-Inroad-Warmup` token is HMAC-signed and verified before a message is
   treated as warmup; the signed payload's `workspace_id` must equal the polled
   mailbox's. An unsigned/forged header is ignored and falls through to normal
   reply/bounce handling.
4. Warmup mail is excluded from campaign reply/bounce classification, so warmup
   traffic can never stop, suppress, or bounce a real campaign enrollment.
5. Warmup send + engagement are claim-before-act idempotent (`warmup_sends` claim,
   `warmup_receipts.engaged` guard): a retried task never double-sends or
   double-replies.
6. Warmup mailbox credentials are zeroized after each send/engage, like every
   other worker secret.

## 10. HTTP API (append to `api/openapi.yaml`) — the frontend contract

All under the authenticated `/api/v1`, workspace from the JWT.

```
PUT    /mailboxes/{id}/warmup      enable/update  body: WarmupSettings          → WarmupParticipant
DELETE /mailboxes/{id}/warmup      disable                                      → 204
GET    /mailboxes/{id}/warmup      one mailbox's warmup detail + 30d series     → WarmupDetail
GET    /warmup/overview            pool summary + per-mailbox health/stats       → WarmupOverview
```

Response shapes (JSON, snake_case at the boundary):

```jsonc
// WarmupParticipant
{ "mailbox_id","enabled","start_volume","max_volume","ramp_increment",
  "reply_rate","health_state","health_reason","started_at","today_sent",
  "today_target" }

// WarmupSettings (request)  — all optional, validated: 1≤start≤max≤200, 0≤reply_rate≤1
{ "start_volume?","max_volume?","ramp_increment?","reply_rate?" }

// WarmupOverview
{ "pool_size", "active", "series_days": 30,
  "mailboxes": [ { ...WarmupParticipant, "inbox_rate_7d","spam_rate_7d" } ] }

// WarmupDetail
{ ...WarmupParticipant,
  "series": [ { "day","sent","received","inbox","spam","replies" } ] }
```

Validation at the boundary: reject `start_volume>max_volume`, `max_volume>200`,
`reply_rate` outside [0,1]; enabling a mailbox not owned by the workspace → 404.

## 11. Frontend (`web/src/features/warmup/`)

- Types generated from the openapi additions (`npm run gen:api`) — never
  hand-declared. Endpoints injected via `store/empty-api.ts` `injectEndpoints`.
- **Warmup toggle** on the mailbox detail/list: enable/disable + settings
  (start/max/increment/reply-rate) with client-side + server validation, RTK
  mutation, optimistic invalidation of the overview.
- **Warmup overview screen** (`/app/warmup`): pool size + "needs ≥2 mailboxes"
  empty state; per-mailbox card with health badge (healthy/watch/throttled/paused
  color), ramp progress (today_sent/today_target), 7-day inbox-placement rate, and
  a 30-day sent/received sparkline (respect the design-system tokens; dark-mode
  aware). Error states through `@/lib/rtk-error`. Route-level code-split (lazy).
- Vitest coverage: settings-form validation, health-badge mapping, the empty/
  <2-mailbox state, and an error branch. (Frontend memory: exacting on error
  handling + code splitting.)

## 12. Testing

- **coreapi/inprocess**: partner selection avoids self + recent + cross-workspace;
  claim idempotency (Won/AlreadySent/Skip); receipt upsert idempotent; stats +
  thread turn advance; `NextWarmupDue` ramp/jitter bounds; health transitions +
  recovery. Reuse the existing `claim_integration_test.go` harness style.
- **worker/warmup**: send handler happy/ retryable/permanent paths with a fake
  Sender; engage handler rescue+read+reply with a fake Engager; skip paths.
- **platform/warmup**: token sign/verify (tamper rejected); schedule math is pure
  and table-tested; content library returns stable threads.
- **platform/mail**: Engager IMAP/Gmail against fakes; unsupported Graph is a
  clean skip.
- Integration (needs Docker): two participants warm each other end-to-end through
  the in-process coreapi; a forged token is ignored; a warmup reply does not touch
  a campaign enrollment.

## 13. Build order (contract-first, agent team)

1. **I** land this spec + the `api/openapi.yaml` additions + the security-invariant
   append (the shared contract).
2. **backend-developer**: migration `000017`, sqlc queries, `warmup` domain
   (store/service/handler/routes/dto), coreapi methods + inprocess impl, queue
   tasks + scheduler, `platform/warmup` (content/token/schedule), `mail.Engager`
   impls, worker `send/engage/sweep` + the inbox-poll receipt hook, wiring in
   `cmd/worker` + `cmd/inroad`. TDD.
3. **frontend-developer** (parallel): regenerate types, warmup feature + overview
   route + toggle + tests, reconcile injected vs generated endpoints.
4. **reviewer** + **security** + **qa**: quality, invariant, and test-suite gates
   before merge on `feature/warmup-engine`.

Never reference any third-party product in code, comments, or commits.
```

---

## 14. Transport seam (`internal/platform/bus`) — Redis now, Kafka/NATS later

Follows the same "interface + one shipped impl" pattern as `coreapi.Client`
(in-process) and the `replyclassify` model seam. The interface expresses delivery
*intent*, not asynq mechanics, so a future Kafka/NATS impl provides the same
guarantees its own way without touching domain or worker code.

```go
package bus

// Job is a transport-neutral unit of work.
type Job struct {
    Kind    string // task type, e.g. "warmup:tick"
    Payload []byte // opaque, JSON-encoded domain payload
    Key     string // dedup / idempotency key ("" = none)
    Dest    string // routing destination (§15); "" = default/shared
}

// Options are the delivery guarantees the domain asks for.
type Options struct {
    At       time.Time     // deliver at (zero = now)
    In       time.Duration // or deliver after (0 = now)
    MaxRetry int           // 0 = transport default
}

// Dispatcher publishes jobs. redisbus (asynq) is the only impl in v1.
type Dispatcher interface {
    Publish(ctx context.Context, j Job, o Options) error
}

// PeriodicScheduler registers recurring jobs (transport-specific under the hood).
type PeriodicScheduler interface {
    RegisterPeriodic(spec, kind string) error
}
```

- `internal/platform/bus/redisbus` implements both over the existing asynq client:
  `Key→asynq.TaskID`, `Dest→asynq.Queue`, `At/In→ProcessAt/ProcessIn`,
  `MaxRetry→asynq.MaxRetry`, `RegisterPeriodic→asynq.Scheduler.Register`. The
  `ErrTaskIDConflict`-as-success dedup rule moves here unchanged.
- **Migration scope (deliberately bounded):** the NEW warmup path and the routing
  layer depend on `bus.Dispatcher`. `*queue.Client` is made to satisfy the
  interface so existing campaign/sequence/inbox enqueuers keep working untouched;
  migrating them onto the seam is a documented follow-up, not part of this build.
  (Rationale: honor the seam the product wants without rewriting the whole send
  path for one feature — CLAUDE.md "don't abstract the world on the second use".)
- Honest limitation to document in the impl: delayed delivery, dedup, and retries
  are native to asynq/Redis; a Kafka/NATS impl must supply them itself (delay
  queues / a scheduler topic / a dedup store). The seam names the guarantee; each
  transport earns it. This is called out so nobody assumes Kafka is a free swap.

## 15. Worker identity + per-IP routing (asynq-native, behind the bus seam)

Goal: a mailbox's outbound mail consistently egresses from ONE worker's IP, and
mailboxes spread across workers — the deliverability win. No broker change.

**Config (`cmd/worker`):**
- `INROAD_WORKER_ID` — stable id (default: hostname).
- `INROAD_WORKER_EGRESS_IP` — optional source IP this worker binds outbound dials
  to; empty = OS default route (single-node dev).
- `INROAD_WORKER_QUEUES` — queues this worker consumes; default `w:<id>,default`.

**Registry + assignment (migration `000018_worker_routing`):**
```sql
CREATE TABLE workers (                     -- global infra, not tenant data
    worker_id     TEXT PRIMARY KEY,
    egress_ip     TEXT NOT NULL DEFAULT '',
    last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE mailbox_worker_assignments (  -- tenant data: workspace-pinned
    mailbox_id    UUID PRIMARY KEY REFERENCES mailboxes(id) ON DELETE CASCADE,
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    worker_id     TEXT NOT NULL,
    assigned_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);
```
- Each worker upserts its `workers` row on a heartbeat (reuses the sweep tick).
- A control-plane **assigner** (coreapi: `AssignMailboxWorker`) picks the
  least-loaded live worker (heartbeat within N min) for a mailbox on first
  send/warmup, persists the assignment, and is idempotent + rebalanceable. When
  no worker has a live heartbeat (single-node dev), it returns the shared
  `default` queue so everything still runs on one process.
- **Routing:** send/warmup enqueue resolves the mailbox's assignment →
  `Job.Dest = "w:"+worker_id` (or `""` for default). `Dest` is derived
  server-side from the assignment, NEVER from client input.

**Source-IP bind (`internal/platform/mail`):**
- The SSRF-guarded dialer gains an optional `LocalAddr` (the worker's egress IP).
  It sets the *source* address only — it still resolves and `vetAddr`-vets the
  *destination* IP and dials that. Binding the source never relaxes the SSRF
  destination checks. Applied to both send and engagement dials.

**Warmup interaction:** warmup send tasks route by the FROM-mailbox's assignment,
so a mailbox's warmup and campaign traffic share one egress identity — reputation
is built and spent on the same IP. Nothing in the warmup domain logic changes;
only the enqueue destination is resolved through the assigner.

## 16. Revised build order (supersedes §13) — three composable tracks

1. **I** land the spec, the `api/openapi.yaml` warmup additions, and the
   security-invariant appends (shared contract).
2. **backend-developer — Track A (transport seam):** `internal/platform/bus`
   interface + `redisbus` (asynq) impl; make `*queue.Client` satisfy it; warmup +
   routing enqueues go through it. TDD.
3. **backend-developer — Track B (routing):** migration `000018`, `workers` +
   `mailbox_worker_assignments` queries, worker identity config, heartbeat upsert,
   `AssignMailboxWorker` assigner, `Dest` resolution at enqueue, source-IP bind on
   the `mail` dialer (destination SSRF vet unchanged). TDD.
4. **backend-developer — Track C (warmup engine):** everything in §3–§9 — migration
   `000017`, warmup domain, coreapi methods + inprocess impl, `platform/warmup`
   (content/token/schedule), `mail.Engager`, worker `send/engage/sweep` + inbox
   receipt hook, wiring. Enqueues route via Track A + B. TDD.
5. **frontend-developer (parallel with A–C):** regenerate types; warmup feature,
   `/app/warmup` overview route, per-mailbox toggle, health/placement UI, tests;
   reconcile injected vs generated endpoints.
6. **reviewer + security + qa:** quality, invariant (esp. SSRF-source-bind,
   workspace-pinning, warmup-vs-campaign isolation), and full-suite gates before
   merge on `feature/warmup-engine`.

Tracks A→B→C are ordered by dependency but small enough to land as stacked commits
on the one branch. Frontend runs against the §10 contract in parallel throughout.

## 17. Additional security invariants (append to §9 / `docs/security.md`)

7. Binding a worker's outbound source IP (`LocalAddr`) never bypasses the SSRF
   guard: the destination host is still resolved and `vetAddr`-vetted, and the
   dial still targets the vetted destination IP. Source-bind is source-only.
8. A job's routing `Dest` (target worker/queue) is always derived server-side from
   the mailbox→worker assignment, never from a client-supplied field, so a caller
   can't pin their traffic to, or divert it through, an arbitrary worker.
9. `mailbox_worker_assignments` is workspace-pinned like all tenant data; the
   `workers` registry is global infrastructure state (no tenant rows) and is never
   returned on a tenant-facing API.

