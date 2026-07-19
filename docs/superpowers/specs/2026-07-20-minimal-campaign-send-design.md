# Minimal Campaign Send — Design Spec

**Date:** 2026-07-20
**Status:** Approved design, pre-plan
**Source PRD:** `PRD.md` §9.2–9.5, §10
**Builds on:** mailbox domain, `coreapi` seam (ADR-0003), `crypto.Sealer`, `platform/mail`

---

## 1. Purpose & Scope

Prove the deliverability core: a **real email leaves a connected mailbox and lands in an inbox**, driven by a minimal but real campaign. Backend only — verified by API + a live send. UI is the next increment.

The vertical spans three thin slices: **contacts (CSV import) → campaign (one message, one mailbox, one list) → send engine (worker sends over SMTP, respects caps, records results, honors unsubscribe)**.

## 2. Locked Decisions

| Decision | Choice |
|---|---|
| Trigger | Minimal campaign → send to a list's contacts |
| Contacts ingestion | CSV import (map email + first_name, dedup) |
| Unsubscribe | Included — footer link + `List-Unsubscribe` header + stateless HMAC token + suppression |
| UI | Deferred to the next increment (backend-first) |
| Message building | `github.com/wneessen/go-mail` (MIME, HTML+text, Message-ID) |
| Credential access | Worker gets decrypted SMTP config via `coreapi` (never touches `db`) |
| Send granularity | One `sends` row + one asynq task **per send_id** (restart-safe, idempotent) |
| Caps | Effective daily cap via ramp curve; strict min-interval pacing deferred |
| Deferred | open/click tracking, reply/bounce detection, multi-step sequences/branching, UI |

## 3. Data Model (scalability is a first-class requirement)

Migration `000003_campaign_send.up.sql`. All tenant tables carry `workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE`. All PKs are `UUID DEFAULT gen_random_uuid()`; all timestamps `TIMESTAMPTZ`.

```sql
CREATE TABLE contacts (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email         TEXT NOT NULL,
    first_name    TEXT NOT NULL DEFAULT '',
    last_name     TEXT NOT NULL DEFAULT '',
    company       TEXT NOT NULL DEFAULT '',
    custom_fields JSONB NOT NULL DEFAULT '{}',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Case-insensitive dedup per workspace (RFC-pragmatic: emails compared lower-case).
CREATE UNIQUE INDEX idx_contacts_ws_email ON contacts (workspace_id, lower(email));

CREATE TABLE lists (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_lists_workspace ON lists (workspace_id);

CREATE TABLE list_members (
    list_id    UUID NOT NULL REFERENCES lists(id) ON DELETE CASCADE,
    contact_id UUID NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    added_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (list_id, contact_id)
);
CREATE INDEX idx_list_members_contact ON list_members (contact_id);

CREATE TABLE campaigns (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    mailbox_id    UUID NOT NULL REFERENCES mailboxes(id) ON DELETE RESTRICT,
    list_id       UUID NOT NULL REFERENCES lists(id) ON DELETE RESTRICT,
    subject       TEXT NOT NULL,
    body_text     TEXT NOT NULL DEFAULT '',
    body_html     TEXT NOT NULL DEFAULT '',
    status        TEXT NOT NULL DEFAULT 'draft'
                    CHECK (status IN ('draft','running','paused','done')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    launched_at   TIMESTAMPTZ
);
CREATE INDEX idx_campaigns_workspace ON campaigns (workspace_id);

CREATE TABLE sends (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id   UUID NOT NULL REFERENCES campaigns(id) ON DELETE CASCADE,
    contact_id    UUID NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    mailbox_id    UUID NOT NULL REFERENCES mailboxes(id) ON DELETE CASCADE,
    to_email      TEXT NOT NULL,
    status        TEXT NOT NULL DEFAULT 'queued'
                    CHECK (status IN ('queued','sent','failed','skipped')),
    error         TEXT NOT NULL DEFAULT '',
    message_id    TEXT NOT NULL DEFAULT '',
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    sent_at       TIMESTAMPTZ,
    -- Idempotency: one send per contact per campaign. A re-launch can't double-send.
    UNIQUE (campaign_id, contact_id)
);
-- Hot path 1: daily cap counting (sends today for a mailbox). Partial index keeps it tiny.
CREATE INDEX idx_sends_mailbox_sent ON sends (mailbox_id, sent_at) WHERE status = 'sent';
-- Hot path 2: campaign stats (counts by status).
CREATE INDEX idx_sends_campaign_status ON sends (campaign_id, status);

CREATE TABLE suppression (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    workspace_id  UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    email         TEXT NOT NULL,
    reason        TEXT NOT NULL CHECK (reason IN ('unsubscribe','bounce','manual')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX idx_suppression_ws_email ON suppression (workspace_id, lower(email));
```

**Scalability rationale (explicit):**
- **`sends` is the only high-growth table.** Its two hot queries — per-mailbox daily-cap count and per-campaign status rollup — are served by a *partial* index (`WHERE status='sent'`) and a composite index, so both stay index-only regardless of table size. `to_email` is denormalized onto `sends` so the send worker and status views never join `contacts`.
- **Idempotency by constraint, not by code:** `UNIQUE (campaign_id, contact_id)` makes re-launch and worker retries safe at the DB layer.
- **Referential integrity:** `mailbox_id`/`list_id` on `campaigns` use `ON DELETE RESTRICT` (can't orphan a running campaign); child rows cascade from their workspace/campaign.
- **Case-insensitive email uniqueness** via `lower(email)` expression indexes on `contacts` and `suppression` prevents duplicate-identity drift.
- **Future scaling lever (documented, not built):** `sends` can be range-partitioned by `created_at` (monthly) when volume warrants; the schema is partition-ready (time column present, no cross-time unique except the campaign-scoped one). Campaign stat counters could also be denormalized onto `campaigns` if COUNT-on-index ever becomes hot. Neither is needed at v1 scale.

## 4. Type Safety (first-class requirement)

- **DB → Go:** sqlc generates typed structs/params (existing pattern). No `interface{}` except `custom_fields` (`map[string]any`, the one legitimately dynamic field).
- **Typed status enums:** each status column has a Go typed-constant set that is the *only* way code produces a status, mirrored by the DB `CHECK` constraint — compile-time safety + storage-time safety:
  ```go
  type CampaignStatus string
  const (
      CampaignDraft   CampaignStatus = "draft"
      CampaignRunning CampaignStatus = "running"
      CampaignPaused  CampaignStatus = "paused"
      CampaignDone    CampaignStatus = "done"
  )
  ```
  Same for `SendStatus` (`queued|sent|failed|skipped`) and `SuppressionReason` (`unsubscribe|bounce|manual`). Domains accept/return these types, converting to `string` only at the sqlc boundary.
- **DTOs are explicit structs** — request and response types per endpoint, no `map[string]any` request bodies.
- **`coreapi.SendJob` is a fully-typed bundle** (no loose maps).
- **OpenAPI is the contract** → the next-increment frontend regenerates typed RTK Query hooks; every new endpoint + schema is added to `api/openapi.yaml` here so the contract never drifts.

## 5. Validation (every route validates; requirement)

Introduce `internal/platform/validate` wrapping `github.com/go-playground/validator/v10` with one shared instance and a helper:

```go
func Struct(v any) error // returns a *ValidationError mapped to HTTP 400 with field details
```

Every request DTO carries validation tags; handlers call `validate.Struct(req)` before touching the service. Beyond field validation, **services enforce tenant-ownership existence checks** (a referenced `mailbox_id`/`list_id` must exist *and* belong to the caller's workspace → 404, never a cross-tenant leak).

Per-route validation rules:

| Route | Field validation | Ownership / semantic checks |
|---|---|---|
| `POST /lists` | `name` required, 1–200 chars | — |
| `POST /lists/{id}/import` | multipart; `Content-Type` = text/csv; file ≤ **10 MB**; ≤ **50,000** rows; **first row is a header**; columns detected by header name (`email` required; `first_name`,`last_name`,`company` optional), case-insensitive; 400 if no `email` header | list `{id}` belongs to workspace (404). Per row: valid email → upsert contact + add to list; invalid email → skipped (counted, not fatal). Dedup on `lower(email)`. Returns `{imported, skipped, duplicates}` |
| `GET /contacts?list=&limit=&offset=` | `limit` 1–200 (default 50), `offset` ≥ 0 | list (if given) belongs to workspace |
| `POST /campaigns` | `name` req 1–200; `subject` req 1–500; `body_text` OR `body_html` non-empty; `mailbox_id`,`list_id` req UUIDs | mailbox_id + list_id exist & belong to workspace (404); mailbox `status='active'` (422 otherwise) |
| `POST /campaigns/{id}/launch` | — | campaign belongs to workspace (404); status must be `draft` (409 otherwise); list non-empty (422) |
| `GET /campaigns/{id}` | — | belongs to workspace (404) |
| `GET /u/{token}` (public) | token present, HMAC-valid (else 400) | — |

CSV parsing uses `encoding/csv` with a hard row cap and a `LazyQuotes` tolerant reader; a malformed row is skipped and counted, never aborts the import.

## 6. `coreapi` Extension (worker stays DB-free — ADR-0003)

```go
type SendJob struct {
    SendID       string
    Suppressed   bool
    // effective cap gate
    EffectiveDailyCap int
    SentToday         int
    // message
    ToEmail   string
    FirstName string
    Subject   string
    BodyText  string
    BodyHTML  string
    UnsubURL  string
    // decrypted transport
    FromEmail    string
    FromName     string
    SMTPHost     string
    SMTPPort     int
    SMTPUsername string
    SMTPPassword string // decrypted in-memory only, never logged
    UseTLS       bool
}

// Added to coreapi.Client:
GetSendJob(ctx context.Context, sendID string) (SendJob, error)
MarkSend(ctx context.Context, sendID string, result SendResult) error // status, messageID, errMsg
```

`inprocess` implements both: joins `sends→campaigns→contacts→mailboxes`, decrypts the mailbox secret via `crypto.Sealer`, computes the effective cap + today's count, builds the unsubscribe URL. The worker imports **zero** `db`. `SMTPPassword` is decrypted only in the returned struct, used immediately, never logged (security invariant).

## 7. Send Flow

1. `POST /campaigns/{id}/launch` (transaction): set campaign `status='running'`, `launched_at=now()`; `INSERT` a `sends` row (`status='queued'`) for each list member `ON CONFLICT (campaign_id, contact_id) DO NOTHING`; enqueue one `send:email` asynq task per new `send_id`.
2. Worker `send:email` handler:
   - `job = GetSendJob(sendID)`; if `job.Suppressed` → `MarkSend(skipped)`, done.
   - If `job.SentToday >= job.EffectiveDailyCap` → re-enqueue the task with a delay (retry tomorrow's window); leave `sends.status='queued'`.
   - Else: personalize subject/body (`{{first_name}}`, `{{email}}`), append the unsubscribe footer, build MIME via go-mail with a `List-Unsubscribe` header, `Send()` over SMTP → on success `MarkSend(sent, messageID)`, on error `MarkSend(failed, err)`.
3. Campaign completion: when no `queued` sends remain, a light check flips campaign `status='done'` (computed on `GET /campaigns/{id}` and/or after each MarkSend).

**Effective daily cap (ramp):** `cap(day) = min(daily_cap, round(ramp_start_cap + (daily_cap - ramp_start_cap) * day / ramp_days))` where `day = days since mailbox.created_at`, if `ramp_enabled`; else `daily_cap`. "Today's count" = `sends` with `status='sent'` and `sent_at::date = today` for the mailbox (served by the partial index).

## 8. Unsubscribe (stateless, no tokens table)

- Token = `base64url(workspace_id + ":" + email)` + `.` + `base64url(HMAC_SHA256(jwtSecret, workspace_id + ":" + email))`. Verified by recomputing the HMAC — no DB lookup, unforgeable.
- Every send: footer `Unsubscribe: {PUBLIC_URL}/u/{token}` (text + HTML) and header `List-Unsubscribe: <{PUBLIC_URL}/u/{token}>`.
- `GET /u/{token}` (public, no auth): verify HMAC → `INSERT INTO suppression (workspace, email, 'unsubscribe') ON CONFLICT DO NOTHING` → render a plain confirmation page.
- New config `INROAD_PUBLIC_URL` (default `http://localhost:8080`) for absolute links.

## 9. Message Building — `platform/mail`

Add to `platform/mail` a `Sender` using `go-mail`:
```go
type Message struct { FromEmail, FromName, To, Subject, BodyText, BodyHTML, ListUnsubscribe string }
func (s *NetSender) Send(cfg SMTPConfig, msg Message) (messageID string, err error)
```
Reuses the SSRF-vetted dial rules (the guard applies to `Send` too). Generates a `Message-ID` for future reply threading. TLS per `SMTPConfig.UseTLS`.

## 10. New Domains & Files

- `internal/app/contact/` — Store iface + PgStore, service (upsert, CSV import), handler, routes.
- `internal/app/list/` — lists + membership.
- `internal/app/campaign/` — campaign CRUD + launch (enqueues sends).
- `internal/app/suppression/` — suppression store + the public unsubscribe handler (HMAC).
- `internal/worker/sender/` — the `send:email` handler.
- `internal/platform/validate/` — validator wrapper.
- `internal/platform/mail/sender.go` — go-mail Send.
- `coreapi` + `inprocess` — `GetSendJob`/`MarkSend`.
- migration `000003_campaign_send.*` + `queries/{contact,list,campaign,send,suppression}.sql`.
- `api/openapi.yaml` — new endpoints/schemas.

Follows the `mailbox` reference pattern (domain-owned `Store` interface, auth-scoped routes, DTOs, tenant checks).

## 11. Verification (the deliverability proof)

Backend e2e, no UI:
1. Import a CSV containing a **real test inbox you control** + a couple of others.
2. `POST /campaigns` on the live mailbox (`ahmed@axomble.com`), subject + body with `{{first_name}}`.
3. `POST /campaigns/{id}/launch`.
4. Worker sends → **confirm the test inbox physically receives the personalized email**; assert `sends` row `status='sent'` + non-empty `message_id`; cap respected.
5. Hit the unsubscribe link from the email → contact in `suppression`; re-launch (or a second campaign) **skips** them (`status='skipped'`).
6. Unit tests: cap/ramp math, personalization, HMAC token round-trip, CSV parse (valid/invalid/dedupe), validator rules. Integration tests: import→launch→send against dockerized Postgres with a mock SMTP (mailpit) so it runs in CI without a real provider.

## 12. Build Order (input to the plan)

1. Migration + sqlc + typed status enums.
2. `platform/validate`.
3. `contact` + `list` (+ CSV import).
4. `campaign` (CRUD + launch, enqueue).
5. `platform/mail` `Send` (go-mail) + `suppression` + unsubscribe HMAC + endpoint.
6. `coreapi` `GetSendJob`/`MarkSend` + `inprocess`.
7. `internal/worker/sender` handler + register.
8. Wire `cmd/inroad` routes + `cmd/worker`; `INROAD_PUBLIC_URL` config.
9. OpenAPI update.
10. E2E (mailpit in CI; real provider for the live proof).
