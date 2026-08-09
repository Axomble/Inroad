-- A/B variants on a sequence step. The step's own content is variant A (see
-- migration 000053), so these rows are the ALTERNATIVES, never the whole set.

-- name: ListStepVariants :many
-- Ordered by id, not by label or created_at, because this list feeds the
-- deterministic selection in platform/abtest: the interval a variant occupies
-- depends on its position, so the order has to be stable against renames and
-- against two variants created in the same transaction.
SELECT id, step_id, label, weight, subject, body_text, body_html, created_at, updated_at
FROM sequence_step_variants
WHERE step_id = $1 AND workspace_id = $2
ORDER BY id;

-- name: ListVariantsByCampaign :many
-- Every variant in a campaign, for the step editor and the results table. The
-- join is what pins the campaign; workspace_id is pinned on both sides so the
-- join can never pair rows across workspaces.
SELECT v.id, v.step_id, v.label, v.weight, v.subject, v.body_text, v.body_html,
       v.created_at, v.updated_at, s.step_order
FROM sequence_step_variants v
JOIN sequence_steps s ON s.id = v.step_id AND s.workspace_id = v.workspace_id
WHERE s.campaign_id = $1 AND v.workspace_id = $2
ORDER BY s.step_order, v.id;

-- name: CreateStepVariant :one
-- A duplicate label raises 23505 on UNIQUE (step_id, label), which the service
-- maps to a conflict rather than silently disambiguating -- two rows called "B"
-- in a results table would make the report unreadable.
INSERT INTO sequence_step_variants (workspace_id, step_id, label, weight, subject, body_text, body_html)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING id, step_id, label, weight, subject, body_text, body_html, created_at, updated_at;

-- name: UpdateStepVariant :one
-- Zero rows means the variant is not in this workspace, which the caller reports
-- as 404 -- the workspace pin is what makes that safe to say.
UPDATE sequence_step_variants
SET label = $3, weight = $4, subject = $5, body_text = $6, body_html = $7, updated_at = now()
WHERE id = $1 AND workspace_id = $2
RETURNING id, step_id, label, weight, subject, body_text, body_html, created_at, updated_at;

-- name: DeleteStepVariant :execrows
-- sends.variant_id is ON DELETE SET NULL, so deleting a variant does NOT delete
-- the messages sent under it -- those sends survive and read as base content.
-- That loses attribution, which is why the API steers operators to weight 0
-- instead; deletion stays available for a variant that never sent.
DELETE FROM sequence_step_variants WHERE id = $1 AND workspace_id = $2;

-- name: SetStepVariantWeight :execrows
-- The step's own weight in the split. 0 retires the base copy without deleting
-- what it said, which is how a winning variant is promoted.
UPDATE sequence_steps SET variant_weight = $3, updated_at = now()
WHERE id = $1 AND workspace_id = $2;

-- name: CountVariantsSent :one
SELECT count(*)::bigint FROM sends
WHERE workspace_id = $1 AND variant_id = $2 AND status = 'sent';

-- name: GetStepVariant :one
SELECT id, step_id, label, weight, subject, body_text, body_html, created_at, updated_at
FROM sequence_step_variants
WHERE id = $1 AND workspace_id = $2;
