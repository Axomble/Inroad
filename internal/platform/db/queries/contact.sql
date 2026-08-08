-- name: UpsertContact :one
INSERT INTO contacts (workspace_id, email, first_name, last_name, company)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (workspace_id, lower(email))
DO UPDATE SET first_name = EXCLUDED.first_name
RETURNING id, (xmax = 0)::boolean AS inserted;
-- name: AddListMember :exec
INSERT INTO list_members (list_id, contact_id) VALUES ($1, $2)
ON CONFLICT (list_id, contact_id) DO NOTHING;

-- The record-page queries below read tables the crm app package owns
-- (companies, deals, pipeline_stages) and the send/sequence tables the campaign
-- package owns. They read the shared schema through sqlc rather than calling
-- another domain's service: app packages do not import each other, and routing a
-- read through another domain's HTTP-shaped service would be the worse coupling
-- (same reasoning as agentchat.PgModelResolver).

-- name: GetContactRecord :one
-- deal_count is the TRUE total, counted independently of the capped deals list,
-- so a truncated list stays honest ("25 of 38") instead of implying the cap is
-- the whole set. Same correlated-subquery shape the contact search already uses
-- (see internal/app/contact/search.go searchColumns), served by idx_deals_contact.
SELECT c.id, c.email, c.first_name, c.last_name, c.job_title, c.linkedin_url,
       c.company_id, co.name AS company_name, co.domain AS company_domain,
       (SELECT count(*) FROM deals d
         WHERE d.workspace_id = c.workspace_id AND d.primary_contact_id = c.id)::bigint AS deal_count,
       c.created_at, c.updated_at
FROM contacts c
LEFT JOIN companies co ON co.workspace_id = c.workspace_id AND co.id = c.company_id
WHERE c.workspace_id = $1 AND c.id = $2;

-- name: GetContactSuppression :one
-- "May this person be emailed at all" — the record page's first question.
--
-- Matched against the contact's whole ALIAS set, not just contacts.email: the
-- 000042 trigger keeps a contact_emails row (is_primary = true) in lockstep with
-- contacts.email, so driving off contact_emails covers the send-path address AND
-- every secondary alias in one lookup. A suppressed secondary matters because
-- promoting it (PUT /crm/contacts/{id}/emails/{emailID}/primary) would silently
-- stop sending; is_primary tells the caller which of the two situations it has.
--
-- Ordered is_primary DESC so that when several of a contact's addresses are
-- suppressed, the answer is about the address sends actually use, then oldest
-- first for a stable pick among aliases.
SELECT s.reason, ce.email::text AS email, ce.is_primary, s.created_at
FROM contact_emails ce
JOIN suppression s
  ON s.workspace_id = ce.workspace_id AND lower(s.email) = lower(ce.email::text)
WHERE ce.workspace_id = $1 AND ce.contact_id = $2
ORDER BY ce.is_primary DESC, s.created_at, ce.id
LIMIT 1;

-- ListContactDeals is capped, not paginated: a contact page renders a roster.
-- The caller asks for one row beyond the cap and uses the surplus to report
-- deals_truncated.
-- name: ListContactDeals :many
SELECT d.id, d.name, d.pipeline_id, d.stage_id,
       s.label AS stage_label, s.color AS stage_color,
       s.is_won AS stage_is_won, s.is_lost AS stage_is_lost,
       d.amount_micros, d.currency, d.close_date, d.created_at, d.updated_at
FROM deals d
JOIN pipeline_stages s ON s.workspace_id = d.workspace_id AND s.id = d.stage_id
WHERE d.workspace_id = $1 AND d.primary_contact_id = $2
ORDER BY s.position, d.position, d.id
LIMIT $3;

-- name: ContactSendStats :one
-- Sent count and last send. 'sent' is the same numerator the campaign rollup's
-- stats.sent uses (queries/campaign.sql CountSendsByStatus), so a contact's
-- number rolls up to the campaign's.
SELECT count(*) FILTER (WHERE status = 'sent')::bigint AS emails_sent,
       (max(sent_at) FILTER (WHERE status = 'sent'))::timestamptz AS last_sent_at
FROM sends
WHERE workspace_id = $1 AND contact_id = $2;

-- name: ContactTrackingStats :one
-- Per-send engagement numerators, defined exactly as the campaign rollup defines
-- them: an indicative open is a distinct send with an open event that is neither
-- from a known image proxy nor within two seconds of the send (CountHumanOpens),
-- and a click is a distinct send with a click event (CountEngagedSendsByKind).
-- Both tenancy filters are explicit -- te.workspace_id serves the pin and
-- s.workspace_id keeps the join from ever pairing rows across workspaces.
SELECT count(DISTINCT te.send_id) FILTER (
           WHERE te.kind = 'open'
             AND te.user_agent NOT ILIKE '%GoogleImageProxy%'
             AND (s.sent_at IS NULL OR te.created_at > s.sent_at + interval '2 seconds')
       )::bigint AS opens_indicative,
       count(DISTINCT te.send_id) FILTER (WHERE te.kind = 'click')::bigint AS clicks,
       max(te.created_at)::timestamptz AS last_event_at
FROM tracking_events te
JOIN sends s ON s.id = te.send_id AND s.workspace_id = te.workspace_id
WHERE te.workspace_id = $1 AND s.contact_id = $2;

-- name: ContactEnrollmentCounts :many
-- One row per stop reason ('' for an enrollment that has not stopped). The
-- caller sums them for the lifetime enrollment count, so the total and the
-- per-reason counts cannot disagree.
SELECT COALESCE(stop_reason, '') AS stop_reason, count(*)::bigint AS n
FROM sequence_enrollments
WHERE workspace_id = $1 AND contact_id = $2
GROUP BY 1;

-- name: ListContactCampaigns :many
-- tracking_enabled travels with each enrollment because it is the only thing
-- that distinguishes "nobody opened" from "opens were never recorded": a
-- campaign with tracking off contributes sends but structurally cannot
-- contribute opens or clicks. The rollup's counts deliberately do NOT adjust for
-- it (campaign.Metrics does not either, and the two must agree), so this flag is
-- how a caller explains a zero instead of guessing at it.
SELECT e.campaign_id, c.name AS campaign_name, c.tracking_enabled, e.status, e.current_step,
       e.stop_reason, e.enrolled_at, e.last_sent_at
FROM sequence_enrollments e
JOIN campaigns c ON c.workspace_id = e.workspace_id AND c.id = e.campaign_id
WHERE e.workspace_id = $1 AND e.contact_id = $2
ORDER BY e.enrolled_at DESC, e.campaign_id
LIMIT $3;
