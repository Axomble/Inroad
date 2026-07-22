# Analytics & Tracking Design

**Goal:** Give operators per-campaign engagement metrics — sent, open%, click%,
reply%, bounce%, unsubscribe% — by adding open/click tracking (a pixel + link
rewriting) and aggregating it together with the send/enrollment/suppression data
the platform already records. Clicks are the reliable engagement signal; opens
are filtered and labeled "indicative" (prefetch proxies inflate raw opens).

**Builds on:** the merged `main` (sequencing + auth + reply/bounce). Reuses the
public token-endpoint pattern (`/u` unsubscribe + `unsub.MakeToken` HMAC signing),
the send MIME path (`sender`/`personalize` + `withUnsub*` footer injection), and
the existing `CountSendsByStatus`/`CountEnrollmentsByStatus` aggregation. Branch:
`feature/analytics-tracking` off `main`.

**Tech stack:** Go 1.25 · pgx/sqlc · chi · Postgres 16 · React SPA. Migration `000011`.

---

## 0. Assumptions (confirmed with product owner)

| # | Decision | Rationale |
|---|---|---|
| A1 | **Opens filtered + labeled "indicative"; clicks are the headline signal.** | Apple MPP / Gmail image proxy prefetch the pixel with no human — raw opens overcount. |
| A2 | **Events table** (`tracking_events`), user-agent + timestamp only, **no raw recipient IP**. Per-link CTR + proxy-filtering + timelines derive from it. | Privacy-by-design; the filtering (A1) needs the UA, per-link CTR needs the URL. |
| A3 | **Per-campaign dashboard only** (extend `GET /campaigns/{id}`). Workspace-wide analytics page deferred. | Smallest slice; builds on existing per-campaign aggregation. |
| A4 | **Per-campaign `tracking_enabled`, default true, disableable.** | Link rewriting can hurt deliverability; operators must be able to turn it off. |
| A5 | **Click token signs the destination URL** (`HMAC(secret, send_id + url)`); the redirect only ever sends the recipient to the signed URL. | Prevents our tracking domain becoming an open-redirect (phishing/reputation). |
| A6 | **Tracking is scale-critical:** `tracking_events` is the highest-volume table. Design partition-ready on `created_at`, index tuned to the exact aggregation queries, and document a rollup path so per-campaign metrics never require a full scan. | It will dwarf `sends` in row count. |

**Non-goals (later phases):** workspace-wide analytics page, per-step / per-link
breakdown UI, real-time streaming, click-fraud detection beyond proxy filtering,
IP/geo, A/B testing.

---

## 1. Data model — migration `000011` (the crux; design to scale)

```sql
-- One row per open/click hit. Highest-volume table in the system: partition-ready
-- on created_at (mirrors sends' 000006 posture), FK-cascaded, minimally indexed
-- (write-heavy — every extra index is write amplification).
CREATE TYPE tracking_event_kind AS ENUM ('open', 'click');

CREATE TABLE tracking_events (
    id           UUID        NOT NULL DEFAULT gen_random_uuid(),
    workspace_id UUID        NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    campaign_id  UUID        NOT NULL REFERENCES campaigns(id)  ON DELETE CASCADE,
    send_id      UUID        NOT NULL REFERENCES sends(id)      ON DELETE CASCADE,
    kind         tracking_event_kind NOT NULL,
    url          TEXT        NOT NULL DEFAULT '',   -- click target; '' for opens
    user_agent   TEXT        NOT NULL DEFAULT '',   -- for proxy-filtering; NO IP stored
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (id, created_at)                    -- created_at in PK -> partition-ready by range(created_at)
);

-- Denormalizing campaign_id onto the event (rather than only send_id) is deliberate:
-- the dashboard aggregates per campaign, so this lets the count queries filter/group
-- without a join to sends over tens of millions of rows.
-- Aggregation-tuned index: counts of distinct engaged sends per (campaign, kind).
CREATE INDEX idx_tracking_campaign_kind ON tracking_events (campaign_id, kind, send_id);
-- Per-send lookup (dedupe / first-touch), covers the tenant filter too.
CREATE INDEX idx_tracking_send ON tracking_events (send_id);
```
- **Why `campaign_id` denormalized on the event:** the dashboard groups per campaign; carrying `campaign_id` avoids a join to `sends` at aggregation time (which would be a join over the largest table). Set at insert (the signed token / send row carries it).
- **Why `(campaign_id, kind, send_id)` index:** the headline metric is *distinct engaged sends* per campaign per kind — this index serves `COUNT(DISTINCT send_id) WHERE campaign_id=? AND kind=?` as an index-only-ish scan of just that campaign's slice, never the whole table.
- **Partition-readiness:** `created_at` in the PK means range-partitioning by month is a later ALTER with no query rewrite. Not partitioned now (YAGNI), but the door is open — documented as the scale path alongside a periodic rollup into a `campaign_metrics_daily` summary table if `COUNT(DISTINCT)` on the hot slice ever gets slow.

```sql
ALTER TABLE campaigns ADD COLUMN tracking_enabled BOOLEAN NOT NULL DEFAULT true;
```
`down`: drop the column, drop `tracking_events`, drop the enum type.

## 2. Signed tokens — `internal/platform/track` (mirror `unsub`)

```go
// OpenToken signs a send id: HMAC(secret, "o:"+sendID). MakeClickToken signs the
// send id AND the destination URL so the redirect can't be turned into an
// open-redirect. Both are workspace-agnostic in the token (send_id resolves the
// workspace server-side) and URL-safe base64.
func MakeOpenToken(secret []byte, sendID string) string
func ParseOpenToken(secret []byte, token string) (sendID string, ok bool)
func MakeClickToken(secret []byte, sendID, url string) string
func ParseClickToken(secret []byte, token string) (sendID, url string, ok bool)
```
Constant-time HMAC compare (reuse `unsub`'s `sign`/`b64` helpers or lift them into a shared spot). The click token embeds the URL in the *signed* payload; `ParseClickToken` returns the URL only if the signature validates — the endpoint redirects solely to that value.

## 3. Tracking endpoints — `internal/app/tracking` (public, no auth; mirror `/u`)

Mounted in the PUBLIC router group (recipients are unauthenticated):
- `GET /t/o/{token}.gif` — `ParseOpenToken` → resolve the send (workspace/campaign) → insert an `open` event (UA from `r.UserAgent()`, no IP) → return a 1×1 transparent GIF, `Cache-Control: no-store`. Invalid/blank token → still return the GIF (never reveal validity), record nothing.
- `GET /t/c/{token}` — `ParseClickToken` → insert a `click` event (url + UA) → **302** to the signed URL. Invalid token → 404 (no redirect).
- Recording is control-plane (this domain inserts via its own store, like `suppression`), so no `coreapi`/worker change for *recording*. The worker only *builds* the URLs.

## 4. Send-path injection (`sender` + `personalize`)

The `sequence:advance` send job (and the direct-send job) gains `SendID string` and
`TrackingEnabled bool`. In `sender`, after personalization + the unsub footer, when
`TrackingEnabled && bodyHTML != ""`:
- **Link rewrite:** rewrite each `<a href="URL">` whose URL is http(s) to
  `{PublicURL}/t/c/{MakeClickToken(secret, sendID, URL)}`. (Skip mailto:, #, and the
  unsubscribe link.)
- **Open pixel:** append `<img src="{PublicURL}/t/o/{MakeOpenToken(secret, sendID)}.gif" width="1" height="1" alt="" style="display:none">` before `</body>` (or at end).
Plain-text body untouched (no tracking possible). New `internal/worker/track`
(or extend `personalize`) holds the pure rewrite/inject functions — unit-tested with
fixtures (HTML with multiple links, no-body, already-has-unsub-link, malformed HTML
→ never panic). The link rewriter uses `golang.org/x/net/html` (already or add) or a
careful regex — prefer the HTML parser for correctness.

## 5. Metrics aggregation + dashboard (extend `GET /campaigns/{id}`)

New sqlc queries (all workspace + campaign scoped):
```sql
-- name: CountEngagedSendsByKind :many
-- Distinct sends with >=1 open/click, per kind, for a campaign — the numerators.
-- Proxy-filtered opens are excluded here via the UA/timing predicate (see below).
SELECT kind, count(DISTINCT send_id) AS n
FROM tracking_events
WHERE campaign_id = $1 AND workspace_id = $2
GROUP BY kind;
```
Open proxy-filtering is applied in a dedicated "human opens" query that excludes
`user_agent ILIKE '%GoogleImageProxy%'` (+ a small known list) and events within
~2s of the send's `sent_at` (join to `sends.sent_at`); raw opens are also available
for a "N filtered as prefetch" note. The `CampaignDetail` gains a `Metrics` block:
```
sent (from CountSendsByStatus 'sent'), delivered≈sent,
opens_indicative, clicks, replies (enrollment stop_reason 'replied'),
bounces (stop_reason 'bounced'), unsubscribes (suppression count for campaign),
open_rate = opens_indicative/sent, click_rate = clicks/sent, reply_rate, bounce_rate, unsub_rate.
```
`GET /campaigns/{id}` response gains `metrics: {...}`. Frontend: a metrics section on
the campaign detail page (rates as %, clicks emphasized, opens labeled "indicative").
OpenAPI + regenerated types. A per-campaign `tracking_enabled` toggle in the UI
(PATCH/PUT the campaign, or a small dedicated endpoint) — draft-editable.

## 6. Security & privacy

- Endpoints public but **token-signed**: `send_id` unguessable; **click token signs
  the URL → no open-redirect**; invalid open-token still returns the GIF (no oracle),
  invalid click-token 404s.
- Abuse is bounded/self-inflicted: hitting tracking URLs only inflates the sender's
  own workspace metrics; events are workspace-scoped from the send, aggregation
  workspace-pinned. A forged token can't cross tenants (send_id resolves one workspace).
- **UA + timestamp only, no IP.** Per-campaign `tracking_enabled` (default on) — off
  means no rewrite/pixel and no events. Recipient engagement tracking is personal-data
  processing; the toggle + no-IP + the option to disable are the privacy posture.
- Tracking secret = the existing signing secret (`INROAD_JWT_SECRET`) or a dedicated
  `INROAD_TRACKING_SECRET`; decide at implementation, but it must be an HMAC secret
  from env, never committed.

## 7. Verification

- **Unit:** token round-trips (open + click; tampered signature rejected; click URL
  tamper → reject); link-rewrite/pixel-inject (multiple links, mailto/# skipped, unsub
  link skipped, no-body, malformed HTML no-panic); proxy-filter predicate (GoogleImageProxy
  excluded, fast-open excluded, human open counted); rate math.
- **Integration (`//go:build integration`):** seed campaign+send; hit `/t/o` and `/t/c`
  → events inserted with correct kind/url/workspace; `GET /campaigns/{id}` returns the
  expected counts/rates; proxy UA excluded from indicative opens; `tracking_enabled=false`
  campaign injects nothing; click endpoint 302s only to the signed URL, 404 on tamper;
  cross-tenant campaign can't read another's metrics. (Compile-verified now; run when docker up.)
- **Frontend:** Vitest for the metrics section render.

## 8. Open reconciliation notes

- **`send_id` FK vs partition PK (verify first):** check `sends`' actual primary key.
  If migration 000006 gave `sends` a composite PK like `(id, created_at)` (or it's
  actually partitioned), then `tracking_events.send_id REFERENCES sends(id)` is INVALID
  (a FK must target a unique constraint). In that case DROP the `send_id` FK and keep
  `send_id UUID NOT NULL` as an indexed, app-maintained column — the `campaign_id →
  campaigns(id)` FK already provides delete-cascade (sends only die with their campaign),
  so referential integrity is preserved without contorting the schema. Only keep the
  `send_id` FK if `sends` still has a plain `PRIMARY KEY (id)`.
- Confirm `sends` exposes `sent_at` for the fast-open filter join and that a `send_id`
  is available to the send job at MIME-build time (add to the job bundle if not).
- Confirm the HTML link-rewrite library choice (`x/net/html` vs regex) against what's
  already in `go.mod`; prefer a real parser.
- Decide `INROAD_JWT_SECRET` reuse vs a dedicated tracking secret (lean: dedicated, so
  rotating tracking links doesn't invalidate sessions).
