-- name: UpsertContact :one
-- custom_fields is MERGED (||), never replaced. An import maps only the columns
-- present in one CSV, so replacing would make every import silently delete the
-- custom values that file happened not to carry -- a second list upload wiping
-- the enrichment from the first. Merge means an import can only add or overwrite
-- the keys it actually supplied; the caller omits empty cells from the object so
-- a blank column cannot overwrite a real value with "".
--
-- COALESCE, not a bare $6, and not the column's DEFAULT. contacts.custom_fields
-- is NOT NULL DEFAULT '{}', but a DEFAULT only applies when the column is OMITTED
-- from the INSERT -- naming it and binding a Go nil []byte sends an explicit SQL
-- NULL, which the default does not rescue and the NOT NULL constraint rejects.
--
-- That is not a hypothetical: adding this parameter broke every caller that had
-- no custom fields to write, which is most of them (the agent's contact tool and
-- ~20 integration-test helpers all build gen.UpsertContactParams by hand). The
-- normalisation belongs HERE rather than in one app-layer store, because the
-- query is what every caller shares -- and a guard in one wrapper is exactly how
-- the other twenty were missed.
INSERT INTO contacts (workspace_id, email, first_name, last_name, company, custom_fields)
--
-- The parameter is NAMED and CAST rather than left as a bare $6: sqlc cannot
-- infer a type through COALESCE, and silently generates `interface{}` for it —
-- which still compiles, so the loss only shows up as a runtime encode error.
VALUES ($1, $2, $3, $4, $5, COALESCE(sqlc.narg(custom_fields)::jsonb, '{}'::jsonb))
ON CONFLICT (workspace_id, lower(email))
DO UPDATE SET first_name = EXCLUDED.first_name,
              custom_fields = contacts.custom_fields || EXCLUDED.custom_fields
RETURNING id, (xmax = 0)::boolean AS inserted;

-- name: GetContactCustomFields :one
-- Zero rows means the contact is not in this workspace, which the caller reports
-- as 404 -- the workspace pin is what makes that safe to say.
SELECT custom_fields FROM contacts WHERE workspace_id = $1 AND id = $2;

-- name: SetContactCustomFields :execrows
-- Whole-object replacement, unlike the import path's merge: this serves an edit
-- form that submitted the contact's complete custom field set, so a key absent
-- from the payload is a deliberate clear rather than an unmentioned column. The
-- service is what decides which of the two shapes it has; the SQL does not guess.
UPDATE contacts SET custom_fields = $3
WHERE workspace_id = $1 AND id = $2;
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

-- name: CompanyExistsInWorkspace :one
-- Ownership pre-check for the company link. The composite FK
-- (company_id, workspace_id) -> companies(id, workspace_id) would refuse a
-- foreign company anyway, but only as a 23503 the caller cannot act on; this
-- turns it into a clean 404. The FK stays the backstop for the race where the
-- company is deleted between this check and the UPDATE.
SELECT EXISTS(SELECT 1 FROM companies WHERE workspace_id = $1 AND id = $2);

-- name: SetContactCompany :execrows
-- Links a contact to a company, or unlinks it when company_id is NULL. Zero
-- affected rows means the contact is not in this workspace, which the caller
-- reports as 404 — the workspace pin is what makes that safe to say.
UPDATE contacts SET company_id = sqlc.narg(company_id)
WHERE workspace_id = sqlc.arg(workspace_id) AND id = sqlc.arg(id);

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
--
-- opens_measurable answers "could an open have been recorded at all" over the
-- contact's WHOLE history, which is the question the capped enrollment list
-- cannot answer: a client seeing only the 20 newest enrollments would conclude
-- "never tracked" for a contact whose 21st was tracked, and explain away a real
-- zero. It is scoped to sends that actually went out (status = 'sent') because a
-- campaign the contact was enrolled in but never sent to could not have produced
-- an open regardless of its tracking flag.
--
-- LEFT JOIN so this addition provably cannot change emails_sent: a send whose
-- campaign row is missing still counts. sends.campaign_id is NOT NULL with an
-- ON DELETE CASCADE FK, so that cannot actually happen — the outer join is here
-- to make the count independent of the join rather than to handle a real case.
SELECT count(*) FILTER (WHERE s.status = 'sent')::bigint AS emails_sent,
       (max(s.sent_at) FILTER (WHERE s.status = 'sent'))::timestamptz AS last_sent_at,
       COALESCE(bool_or(s.status = 'sent' AND c.tracking_enabled), false)::bool AS opens_measurable
FROM sends s
LEFT JOIN campaigns c ON c.workspace_id = s.workspace_id AND c.id = s.campaign_id
WHERE s.workspace_id = $1 AND s.contact_id = $2;

-- name: ContactTrackingStats :one
-- Per-send engagement numerators, defined exactly as the campaign rollup defines
-- them: both an indicative open and a click are a distinct send with a
-- human-classified event of that kind (platform/botfilter's write-time verdict,
-- the same column CountHumanOpens and CountEngagedSendsByKind read).
--
-- last_event_at deliberately spans ALL events including machine ones: it answers
-- "when did anything last happen to this contact's mail", which is a diagnostic
-- an operator wants to be complete, not a rate that a prefetch would inflate.
-- The join to sends stays -- unlike the campaign queries this one is scoped by
-- contact_id, which only sends carries.
--
-- Both tenancy filters are explicit -- te.workspace_id serves the pin and
-- s.workspace_id keeps the join from ever pairing rows across workspaces.
SELECT count(DISTINCT te.send_id) FILTER (
           WHERE te.kind = 'open' AND NOT te.is_machine
       )::bigint AS opens_indicative,
       count(DISTINCT te.send_id) FILTER (
           WHERE te.kind = 'click' AND NOT te.is_machine
       )::bigint AS clicks,
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
