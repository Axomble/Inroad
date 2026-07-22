-- name: CreateStep :one
INSERT INTO sequence_steps (workspace_id, campaign_id, step_order, delay_seconds, subject, body_text, body_html)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING *;
-- name: GetStep :one
SELECT * FROM sequence_steps WHERE id = $1 AND workspace_id = $2;
-- name: ListStepsByCampaign :many
SELECT * FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2 ORDER BY step_order;
-- name: GetStepByOrder :one
SELECT * FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2 AND step_order = $3;
-- name: GetNextStep :one
-- The first step whose step_order is greater than $3, tolerating gaps left by
-- DeleteStep (which does not renumber). Used to resolve the enrollment's next
-- due step and to detect the last step: sql.ErrNoRows means no further step
-- exists, so the enrollment is complete.
SELECT * FROM sequence_steps
WHERE campaign_id = $1 AND workspace_id = $2 AND step_order > $3
ORDER BY step_order ASC LIMIT 1;
-- name: UpdateStep :one
UPDATE sequence_steps SET delay_seconds = $3, subject = $4, body_text = $5, body_html = $6, updated_at = now()
WHERE id = $1 AND workspace_id = $2 RETURNING *;
-- name: DeleteStep :exec
DELETE FROM sequence_steps WHERE id = $1 AND workspace_id = $2;
-- name: CountStepsByCampaign :one
SELECT count(*) FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2;
-- name: MaxStepOrder :one
SELECT COALESCE(max(step_order), 0)::int FROM sequence_steps WHERE campaign_id = $1 AND workspace_id = $2;
