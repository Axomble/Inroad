-- name: CreateWorkspace :one
INSERT INTO workspaces (name) VALUES ($1) RETURNING *;

-- name: GetWorkspace :one
SELECT * FROM workspaces WHERE id = $1;

-- name: CompleteWorkspaceOnboarding :one
-- Set the workspace's real name and stamp onboarding complete. ONE statement, so
-- the rename and the stamp are inherently atomic -- there is no window in which a
-- crash could leave a renamed-but-unstamped (or stamped-but-unrenamed) workspace.
--
-- Idempotent by construction: on an already-completed workspace both CASE and
-- COALESCE keep the stored values, so a replayed request returns the existing row
-- unchanged rather than silently renaming a workspace someone has since renamed
-- deliberately. A missing id affects 0 rows -> pgx.ErrNoRows -> 404.
UPDATE workspaces
SET name = CASE WHEN onboarding_completed_at IS NULL THEN $2 ELSE name END,
    onboarding_completed_at = COALESCE(onboarding_completed_at, now())
WHERE id = $1
RETURNING *;
