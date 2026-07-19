-- name: CreateCampaign :one
INSERT INTO campaigns (workspace_id, name, mailbox_id, list_id, subject, body_text, body_html)
VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING *;
-- name: GetCampaign :one
SELECT * FROM campaigns WHERE id = $1 AND workspace_id = $2;
-- name: ListCampaigns :many
SELECT * FROM campaigns WHERE workspace_id = $1 ORDER BY created_at DESC;
-- name: SetCampaignStatus :exec
UPDATE campaigns SET status = $3, launched_at = COALESCE(launched_at, $4)
WHERE id = $1 AND workspace_id = $2;
-- name: CountSendsByStatus :many
SELECT status, count(*) AS n FROM sends WHERE campaign_id = $1 GROUP BY status;
