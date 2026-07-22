# Analytics & Tracking Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Per-campaign engagement metrics (sent, open%, click%, reply%, bounce%, unsubscribe%) via open/click tracking + aggregation of existing send/enrollment/suppression data. Clicks are the reliable signal; opens are proxy-filtered and labeled "indicative."

**Architecture:** A high-volume, partition-ready `tracking_events` table; public token-signed pixel + click-redirect endpoints (mirror `/u`); email-safe link rewriting + pixel injection in the send path (tokenizer, not parse+render); metrics computed by aggregation queries tuned to the exact per-campaign counts. Worker builds tracking URLs (send job already has `SendID`); the `tracking` app-domain records events (control-plane, like `suppression`).

**Tech Stack:** Go 1.25 · pgx/sqlc · chi · `golang.org/x/net/html` (tokenizer) · Postgres 16 · React SPA. Migration `000011`.

## Global Constraints

- Module `github.com/inroad/inroad`. Go files lowercase; frontend kebab-case; identifiers idiomatic; snake_case only at JSON/DB/env.
- `app/*` imports `platform/*`, never reverse; `app/*` don't import each other; workers reach data via `coreapi` only (may use `platform/*`). Each domain owns its `Store` interface.
- Every new sqlc query carries a `workspace_id` predicate. Public endpoints identify via HMAC-signed tokens, never a raw id/param.
- Toolchain PATH: prefix EVERY Go/sqlc command with `export PATH="$PATH:/c/Program Files/Go/bin:$HOME/go/bin"`. Shell state doesn't persist. Work in the worktree `C:/Users/Ahmed/OneDrive/Desktop/personal-projects/Inroad-analytics` (branch `feature/analytics-tracking`).
- **DB design is scale-critical** (`tracking_events` is the highest-volume table): partition-ready on `created_at`, indexes tuned to the aggregation queries (no over-indexing a write-heavy table), FK cascade via `campaign_id`. **`send_id` is a plain indexed column, NOT a FK** — `sends` PK is composite `(id, created_at)` (migration 000006), so a FK to `sends(id)` is invalid.
- Click token signs the destination URL (no open-redirect). UA + timestamp stored, NO recipient IP.
- Conventional commits. Commit at end of every task. Never commit to `main`.

---

## Task 1: Migration 000011 + queries (the scale-critical schema)

**Files:** Create `migrations/000011_tracking.{up,down}.sql`; create `queries/tracking.sql`; modify `queries/campaign.sql` (tracking_enabled read/update) → regen `gen/`.

- [ ] **Step 1: Confirm head=000010, verify sends PK, write up migration**

`ls migrations | grep -oE '^[0-9]+' | sort -u | tail -1` → `000010`. Confirm `sends` PK is `(id, created_at)` (`grep "PRIMARY KEY" migrations/000006*.up.sql`) — this is WHY send_id is non-FK.
`000011_tracking.up.sql`:
```sql
CREATE TYPE tracking_event_kind AS ENUM ('open', 'click');

CREATE TABLE tracking_events (
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id  UUID        NOT NULL REFERENCES campaigns(id)  ON DELETE CASCADE,
    send_id      UUID        NOT NULL,   -- indexed, NOT a FK: sends PK is composite (id, created_at)
    kind         tracking_event_kind NOT NULL,
    url          TEXT        NOT NULL DEFAULT '',
    user_agent   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)          -- partition-ready by range(created_at)
);
-- Aggregation-tuned: COUNT(DISTINCT send_id) per (campaign, kind) hits only the
-- campaign's slice, never the whole table. send_id trailing = index-only distinct.
CREATE INDEX idx_tracking_campaign_kind ON tracking_events (campaign_id, kind, send_id);
-- Per-send lookup (join to sends for the fast-open filter; dedupe).
CREATE INDEX idx_tracking_send ON tracking_events (send_id);

ALTER TABLE campaigns ADD COLUMN tracking_enabled BOOLEAN NOT NULL DEFAULT true;
```
`down`: `ALTER TABLE campaigns DROP COLUMN IF EXISTS tracking_enabled;` then `DROP TABLE IF EXISTS tracking_events;` then `DROP TYPE IF EXISTS tracking_event_kind;`.

- [ ] **Step 2: queries/tracking.sql**

```sql
-- name: InsertTrackingEvent :exec
INSERT INTO tracking_events (workspace_id, campaign_id, send_id, kind, url, user_agent)
VALUES ($1,$2,$3,$4,$5,$6);
-- name: CountEngagedSendsByKind :many
-- Numerators: distinct sends with >=1 event, per kind, for a campaign.
SELECT kind, count(DISTINCT send_id)::bigint AS n
FROM tracking_events
WHERE campaign_id = $1 AND workspace_id = $2
GROUP BY kind;
-- name: CountHumanOpens :one
-- Indicative opens: distinct sends with an 'open' NOT from a known prefetch UA and
-- NOT firing within 2s of the send (proxy prefetch). Joined to sends for sent_at.
SELECT count(DISTINCT te.send_id)::bigint
FROM tracking_events te
JOIN sends s ON s.id = te.send_id
WHERE te.campaign_id = $1 AND te.workspace_id = $2 AND te.kind = 'open'
  AND te.user_agent NOT ILIKE '%GoogleImageProxy%'
  AND (s.sent_at IS NULL OR te.created_at > s.sent_at + interval '2 seconds');
```
(Reply/bounce counts come from the existing `CountEnrollmentsByStatus`; unsubscribes from a `CountSuppressionsByCampaign` — add to `queries/suppression.sql` only if absent; check first. If suppression isn't campaign-scoped, count workspace suppressions created after launch, or skip unsub-rate for v1 and note it.)

- [ ] **Step 3: campaign tracking_enabled read/update in queries/campaign.sql**

Add `SetCampaignTracking` (`UPDATE campaigns SET tracking_enabled=$3 WHERE id=$1 AND workspace_id=$2`) if a general campaign update doesn't exist; `GetCampaign`/`Get` (SELECT *) now includes `tracking_enabled`.

- [ ] **Step 4: regen + build** — `make sqlc && go build ./...`.
- [ ] **Step 5: Commit** — `feat(db): 000011 tracking_events (partition-ready) + campaign tracking flag + aggregation queries`.

---

## Task 2: `internal/platform/track` — signed tokens

**Files:** Create `track/token.go`, `token_test.go`.

**Interfaces:** `MakeOpenToken(secret []byte, sendID string) string`, `ParseOpenToken(secret, token) (sendID string, ok bool)`, `MakeClickToken(secret, sendID, url string) string`, `ParseClickToken(secret, token) (sendID, url string, ok bool)`.

- [ ] **Step 1: failing tests** — open token round-trips; click token round-trips (sendID+url recovered); a tampered signature → ok=false; a click token whose URL is altered → ok=false (the URL is inside the signed payload); cross-parse (open token given to ParseClick) → false.
- [ ] **Step 2: fail; Step 3: implement** — mirror `internal/platform/unsub/token.go` (HMAC-SHA256, `hmac.Equal` constant-time, RawURLEncoding). Payload for click = `sendID + "\x00" + url`, signed; ParseClick splits only after verifying the HMAC over the whole payload. Prefix domains (`"o"`/`"c"`) so an open token can't validate as a click.
- [ ] **Step 4: pass; Step 5: commit** — `feat(track): HMAC-signed open/click tokens (click token signs the destination URL)`.

---

## Task 3: Email-safe link rewrite + pixel inject

**Files:** Create `internal/worker/track/inject.go`, `inject_test.go`. (Add `golang.org/x/net/html` — likely already indirect via chi/go-mail; `go get golang.org/x/net/html && go mod tidy`.)

**Interfaces:** `RewriteHTML(htmlBody, baseURL, sendID string, secret []byte) string` — rewrites `<a href>` http(s) links to click-tracking URLs and appends the open pixel. Plain-text is never passed here.

- [ ] **Step 1: failing tests** — HTML with 2 links → both hrefs become `{base}/t/c/{token}` and the token's decoded URL == the original; a `mailto:`/`#`/relative link → left unchanged; the unsubscribe link (contains `/u/`) → left unchanged; output ends with the open-pixel `<img ... src="{base}/t/o/{token}.gif" ...>`; malformed/partial HTML → no panic, best-effort; empty body → returns body unchanged (caller guards, but be safe).
- [ ] **Step 2: fail; Step 3: implement — TOKENIZER, not parse+render**

Use `html.NewTokenizer`: iterate tokens, re-emitting each **verbatim** (`z.Raw()`) EXCEPT for `<a>` start tags — for those, rewrite only the `href` attribute value (when http/https and not the unsub link) to the click URL, re-serialize just that tag. Append the pixel `<img>` at the end (before `</body>` if present, else appended). This preserves the original email HTML byte-for-byte outside the anchors — do NOT full-parse + re-render (it reformats/normalizes and can break email-client markup). Document this rationale in the file.
- [ ] **Step 4: pass; Step 5: commit** — `feat(track): email-safe link rewrite + open-pixel injection (tokenizer preserves markup)`.

---

## Task 4: `internal/app/tracking` — public pixel + click endpoints

**Files:** Create `tracking/{store.go,service.go,handler.go,routes.go,*_test.go}`.

**Interfaces:** `Store` iface (`RecordEvent(ctx, ws, campaign, sendID uuid.UUID, kind, url, ua string) error` over `InsertTrackingEvent`; a `ResolveSend(ctx, sendID) (workspace, campaign uuid.UUID, ok bool)` to map a token's sendID → its workspace/campaign — via a `GetSendTenant` query, workspace derived from the send row, NOT the request). Handler: `openGIF`, `clickRedirect`. `Routes()` mounts `GET /t/o/{token}.gif`, `GET /t/c/{token}`.

- [ ] **Step 1: failing handler tests** — `GET /t/o/{valid}.gif` → 200, `image/gif`, `Cache-Control: no-store`, RecordEvent('open') called with the request UA; invalid open token → 200 GIF, NO event recorded (no oracle); `GET /t/c/{validClick}` → 302 to the signed URL, RecordEvent('click', url) called; tampered click token → 404, no redirect, no event; a click token for URL X must never 302 anywhere but X.
- [ ] **Step 2: fail; Step 3: implement** — handlers parse the token (Task 2), resolve the send's workspace/campaign server-side (so the event is tenant-correct and a forged token can only hit an existing send in its own workspace), record via the store, respond. The 1×1 GIF is a fixed byte slice constant. Never log tokens/UA at error level with PII; log counts/status.
- [ ] **Step 4: pass; Step 5: commit** — `feat(tracking): public open-pixel + click-redirect endpoints (token-signed, no open-redirect)`.

---

## Task 5: Send-path wiring (tracking flag + injection)

**Files:** Modify `coreapi/coreapi.go` + `inprocess/*` (add `TrackingEnabled bool` to the step/send job, populate from the campaign's `tracking_enabled`); modify `internal/worker/sender/sender.go` (+ sequence advance if it builds its own MIME) to inject tracking when `TrackingEnabled && bodyHTML != ""`.

- [ ] **Step 1** — read how the job is assembled (`GetStepSendJob`/`GetSendJob`) + how `withUnsubHTML` is applied in `sender.go`. The job already has `SendID`.
- [ ] **Step 2: failing test** — a sender/inject unit test: job with TrackingEnabled=true + HTML body → the sent `mail.Message.BodyHTML` contains the pixel + rewritten links; TrackingEnabled=false → unchanged; no HTML body → text unchanged, no injection.
- [ ] **Step 3: implement** — add `TrackingEnabled` to the job structs + populate in inprocess from `campaign.tracking_enabled`; in `sender.go`, after `withUnsubHTML`, call `track.RewriteHTML(bodyHTML, cfg.PublicURL, job.SendID, secret)` when enabled. Thread the tracking secret + PublicURL (already in config/coreapi) to the sender. Order: personalize → unsub footer → tracking rewrite (so the unsub link is present and gets skipped by the rewriter).
- [ ] **Step 4: build+test; Step 5: commit** — `feat(sender): inject open/click tracking into HTML sends when enabled`.

---

## Task 6: Metrics aggregation + campaign detail + toggle

**Files:** Modify `internal/app/campaign/{service.go,store.go,handler.go,routes.go}`.

- [ ] **Step 1: failing service test** — with seeded counts (fake store returning sent=N, opens/clicks/replies/bounces/unsub), `CampaignDetail.Metrics` computes correct rates (guard divide-by-zero: sent=0 → rates 0).
- [ ] **Step 2: implement** — `CampaignDetail` gains a `Metrics` struct: `Sent, OpensIndicative, Clicks, Replies, Bounces, Unsubscribes int64` + `OpenRate, ClickRate, ReplyRate, BounceRate, UnsubRate float64` (0..1, rounded in the DTO). Store method aggregates: sent from `CountSendsByStatus`, opens from `CountHumanOpens`, clicks from `CountEngagedSendsByKind`, replies/bounces from `CountEnrollmentsByStatus` (stop_reason), unsub from the suppression count. `GET /campaigns/{id}` response gains `metrics`. Add `PUT /campaigns/{id}/tracking {enabled}` (or fold into a campaign update) — draft-or-anytime editable, workspace-scoped, RequireRole not needed beyond auth (it's the owner's campaign).
- [ ] **Step 3: build+test; Step 4: commit** — `feat(campaign): per-campaign engagement metrics + tracking toggle`.

---

## Task 7: OpenAPI + wiring + regen types

**Files:** Modify `api/openapi.yaml`, `cmd/inroad/main.go`; generated `web/src/store/api.ts`.

- [ ] Mount `tracking.Routes()` at `/t` in the PUBLIC router group (recipients unauthenticated — mirror `/u`). Add the `metrics` block + tracking-toggle endpoint to OpenAPI (match handler shapes). `go build ./... && cd web && npm install && npm run gen:api && npx tsc -b --noEmit`. Commit `feat(api): tracking endpoints + campaign metrics in OpenAPI + regen types`.

---

## Task 8: Frontend — metrics section + tracking toggle

**Files:** `web/src/features/campaigns/*` (metrics panel + toggle), test.

- [ ] Metrics panel on the campaign detail page: rates as %, **clicks emphasized as the reliable number, opens labeled "indicative"** (tooltip explaining prefetch). A tracking-enabled toggle (uses the generated hook). Vitest for the panel render (rates formatted, opens labeled). `npx oxlint && npx tsc -b --noEmit && npx vitest run`. Commit `feat(web): campaign engagement metrics panel + tracking toggle`.

---

## Task 9: Integration tests (compile-verified; docker down)

**Files:** Create `internal/app/tracking/tracking_integration_test.go` (`//go:build integration`).

- [ ] Seed campaign+send; GET `/t/o/{token}.gif` and `/t/c/{token}` → events inserted (correct kind/url/workspace/campaign); `GET /campaigns/{id}` returns expected counts/rates; a GoogleImageProxy-UA open excluded from indicative opens; `tracking_enabled=false` → sender injects nothing (no events on a send from that campaign); click endpoint 302s only to the signed URL, 404 on tamper; cross-tenant campaign can't read another's metrics. Compile-verify: `go vet -tags=integration ./... && go build -tags=integration ./...`. Execution NOT-RUN(no docker); run cmd: `make db-up && go test -tags=integration ./internal/app/tracking/... ./internal/app/campaign/... -v`. Full `go build ./... && go vet ./... && gofmt -l internal cmd && go test ./...`. Commit `test(tracking): integration — pixel/click/metrics/toggle flows (compile-verified)`.
