-- name: GetStepEnrollmentBundle :one
-- Everything needed to build one step-send job, workspace-pinned. Joins the
-- enrollment to its campaign, contact and mailbox. The step content itself is
-- fetched separately by step_order.
--
-- The mailbox join resolves the THREAD's mailbox: the enrollment's pinned
-- mailbox_id once it has sent a step, and campaigns.mailbox_id before that (also
-- the standing fallback for a campaign with no sender-pool rows). A campaign with
-- a pool assigns the mailbox at the first send and pins it here, so a follow-up
-- step never changes mailbox mid-thread. enrollment_mailbox_id is returned
-- separately so the caller can tell "already pinned" from "resolve the pool now".
--
-- campaign_status is aliased (e.status is the ENROLLMENT's) and is load-bearing on
-- the send path, not informational: the deliverability circuit breaker sets
-- campaigns.status='paused', and until this column travelled, nothing downstream
-- read it — a stopped campaign kept sending, because every mid-sequence enrollment
-- is still 'active' at the moment of the pause and each successful send re-enqueues
-- the next advance.
SELECT e.id AS enrollment_id, e.workspace_id, e.contact_id, e.current_step,
       e.status, e.thread_root_id, e.next_due_at, e.mailbox_id AS enrollment_mailbox_id,
       cam.id AS campaign_id, cam.rotation_mode, cam.tracking_enabled, cam.timezone,
       cam.daily_limit, cam.max_new_leads_per_day, cam.status AS campaign_status,
       ct.email AS to_email, ct.first_name, ct.last_name, ct.company, ct.custom_fields,
       m.id AS mailbox_id, m.provider, m.email AS from_email, m.display_name AS from_name,
       m.smtp_host, m.smtp_port, m.smtp_username, m.secret_ciphertext, m.allow_plaintext,
       m.daily_cap, m.min_interval_seconds, m.ramp_enabled, m.ramp_start_cap, m.ramp_days,
       m.created_at AS mailbox_created_at
FROM sequence_enrollments e
JOIN campaigns cam ON cam.id = e.campaign_id
JOIN contacts ct ON ct.id = e.contact_id
JOIN mailboxes m ON m.id = COALESCE(e.mailbox_id, cam.mailbox_id) AND m.workspace_id = e.workspace_id
WHERE e.id = $1 AND e.workspace_id = $2;

-- name: ClaimStepSend :one
-- Claim one step-send for delivery (claim-before-send). The sends row is the
-- claim: a fresh INSERT wins it ('sending', claimed_at=now()). On conflict the
-- row already exists, and the claim is re-won ONLY when the existing row is a
-- STALE 'sending' lease (a crashed worker) — never a terminal 'sent'/'failed'
-- (already delivered / permanently done) nor a FRESH 'sending' (another worker
-- owns it). RETURNING id yields a row iff we won the claim; zero rows
-- (pgx.ErrNoRows) means "skip the send — someone else owns or already delivered
-- it". Staleness is claimed_at older than the lease window (lease_seconds).
-- workspace_id is pinned on both the insert value and the reclaim WHERE, so a
-- foreign/cross-tenant id claims zero rows (belt-and-braces on the campaign FK).
--
-- variant_id records which A/B variant this message used, and is written on the
-- RECLAIM path too: selection is deterministic per (enrollment, step), so a
-- reclaim recomputes the same variant, but the weights could have been edited
-- between the two attempts. Re-stamping it keeps the row describing what is
-- actually about to be sent rather than what a previous attempt intended.
INSERT INTO sends (id, workspace_id, campaign_id, contact_id, mailbox_id, to_email,
                   step_order, references_header, status, claimed_at, variant_id)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'sending', now(), sqlc.narg(variant_id))
ON CONFLICT (campaign_id, contact_id, step_order) WHERE step_order IS NOT NULL
DO UPDATE SET status = 'sending', claimed_at = now(), error = '',
              variant_id = EXCLUDED.variant_id
    WHERE sends.status = 'sending'
      AND sends.workspace_id = $2
      AND sends.claimed_at < now() - make_interval(secs => sqlc.arg(lease_seconds)::int)
RETURNING id;

-- name: LatestSentForContact :one
-- The most recent successfully-sent step for a (campaign, contact), used to
-- thread the next step (In-Reply-To = its message_id; References = its chain).
SELECT message_id, references_header FROM sends
WHERE campaign_id = $1 AND contact_id = $2 AND status = 'sent'
ORDER BY step_order DESC
LIMIT 1;
