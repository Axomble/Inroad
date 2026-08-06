-- Reply-label taxonomy (migration 000047). Every statement is workspace-pinned;
-- ordering is (position, id) everywhere because position is deliberately not
-- unique (see the table comment).

-- name: ListReplyLabels :many
SELECT * FROM reply_labels
WHERE workspace_id = $1
ORDER BY position, id;

-- name: GetReplyLabel :one
SELECT * FROM reply_labels
WHERE workspace_id = $1 AND id = $2;

-- Resolve a classifier key to its label row. Returns zero rows for a key no
-- label claims (a deleted custom label whose key survives on historical
-- enrollment rows) — callers degrade to the pre-000047 behaviour rather than
-- inventing one.
-- name: GetReplyLabelByKey :one
SELECT * FROM reply_labels
WHERE workspace_id = $1 AND key = $2;

-- name: CreateReplyLabel :one
INSERT INTO reply_labels (workspace_id, key, label, color, position,
                          stops_enrollment, is_automated, suppresses_contact,
                          captures_deal, defers_enrollment)
VALUES ($1, $2, $3, $4,
        COALESCE((SELECT max(position) + 1 FROM reply_labels WHERE workspace_id = $1), 0),
        $5, $6, $7, $8, $9)
RETURNING *;

-- Update the editable surface of a label. `key` is deliberately absent: it is
-- the stable machine identifier historical rows and the classifier both name,
-- so it is immutable for builtin AND custom labels alike.
-- name: UpdateReplyLabel :one
UPDATE reply_labels SET
    label = $3, color = $4,
    stops_enrollment = $5, is_automated = $6, suppresses_contact = $7,
    captures_deal = $8, defers_enrollment = $9
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: SetReplyLabelPosition :exec
UPDATE reply_labels SET position = $3
WHERE workspace_id = $1 AND id = $2;

-- Guarded on NOT is_builtin so a builtin can never be removed even if a caller
-- reaches this query without the service-level guard. Zero rows means "not
-- found or builtin" — the service distinguishes the two before it gets here.
-- name: DeleteReplyLabel :execrows
DELETE FROM reply_labels
WHERE workspace_id = $1 AND id = $2 AND NOT is_builtin;
