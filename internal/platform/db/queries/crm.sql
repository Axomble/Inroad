-- Every list query below is keyset-paginated: `seek = false` fetches the first
-- page, and a page's last row supplies the cursor arguments for the next one.
-- The row comparison uses exactly the columns of the ORDER BY, so the ordering
-- is total and no row can be skipped or repeated between pages.

-- name: ListCompanies :many
SELECT c.*, count(d.id)::bigint AS deal_count, lower(c.name) AS name_key
FROM companies c
LEFT JOIN deals d ON d.workspace_id = c.workspace_id AND d.company_id = c.id
WHERE c.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.arg(seek)::bool = false
       OR (lower(c.name), c.id) > (sqlc.arg(cursor_name)::text, sqlc.arg(cursor_id)::uuid))
GROUP BY c.id
ORDER BY lower(c.name), c.id
LIMIT sqlc.arg(page_limit);

-- name: GetCompany :one
SELECT c.*, count(d.id)::bigint AS deal_count
FROM companies c
LEFT JOIN deals d ON d.workspace_id = c.workspace_id AND d.company_id = c.id
WHERE c.workspace_id = $1 AND c.id = $2
GROUP BY c.id;

-- name: InsertCompany :one
INSERT INTO companies (
    workspace_id, name, domain, owner_user_id, annual_revenue_micros, currency)
VALUES (sqlc.arg(workspace_id), sqlc.arg(name), sqlc.narg(domain),
        sqlc.narg(owner_user_id), sqlc.narg(annual_revenue_micros), sqlc.arg(currency))
RETURNING *;

-- name: UpdateCompany :one
UPDATE companies SET
    name = sqlc.arg(name),
    domain = NULLIF(sqlc.arg(domain)::text, ''),
    owner_user_id = sqlc.narg(owner_user_id),
    annual_revenue_micros = sqlc.narg(annual_revenue_micros),
    currency = sqlc.arg(currency)
WHERE workspace_id = sqlc.arg(workspace_id) AND id = sqlc.arg(id)
RETURNING *;

-- name: DeleteCompany :execrows
DELETE FROM companies WHERE workspace_id = $1 AND id = $2;

-- name: ListCompanyContacts :many
SELECT c.id, c.email, c.first_name, c.last_name, c.job_title, c.linkedin_url,
       c.created_at, lower(c.email) AS email_key
FROM contacts c
WHERE c.workspace_id = sqlc.arg(workspace_id) AND c.company_id = sqlc.arg(company_id)
  AND (sqlc.arg(seek)::bool = false
       OR (lower(c.email), c.id) > (sqlc.arg(cursor_email)::text, sqlc.arg(cursor_id)::uuid))
ORDER BY lower(c.email), c.id
LIMIT sqlc.arg(page_limit);

-- ListCompanyDeals is ListDeals narrowed to one company. It is spelled out
-- rather than folded into ListDeals behind a nullable company filter: a
-- `$n IS NULL OR company_id = $n` guard survives into the plan and stops
-- Postgres from using idx_deals_company as an index condition, which is the
-- whole reason this query is separate. The projection is deliberately identical
-- so both lists map through one row converter.
-- name: ListCompanyDeals :many
SELECT d.*,
       p.name AS pipeline_name,
       s.label AS stage_label,
       s.color AS stage_color,
       s.is_won AS stage_is_won,
       s.is_lost AS stage_is_lost,
       s.position AS stage_position,
       d.position::text AS position_key,
       COALESCE(c.name, '') AS company_name,
       COALESCE(ct.email, '') AS contact_email
FROM deals d
JOIN pipelines p ON p.workspace_id = d.workspace_id AND p.id = d.pipeline_id
JOIN pipeline_stages s ON s.workspace_id = d.workspace_id AND s.id = d.stage_id
LEFT JOIN companies c ON c.workspace_id = d.workspace_id AND c.id = d.company_id
LEFT JOIN contacts ct ON ct.workspace_id = d.workspace_id AND ct.id = d.primary_contact_id
WHERE d.workspace_id = sqlc.arg(workspace_id) AND d.company_id = sqlc.arg(company_id)
  AND (sqlc.arg(seek)::bool = false
       OR (s.position, d.position, d.id) > (sqlc.arg(cursor_stage_position)::int,
                                            (sqlc.arg(cursor_position)::text)::numeric,
                                            sqlc.arg(cursor_id)::uuid))
ORDER BY s.position, d.position, d.id
LIMIT sqlc.arg(page_limit);

-- name: ListPipelines :many
SELECT * FROM pipelines
WHERE workspace_id = $1
ORDER BY is_default DESC, lower(name), id
LIMIT $2;

-- ListStagesForPipelines fetches every stage of a page of pipelines in one
-- round trip; listing pipelines must not issue a query per pipeline.
-- name: ListStagesForPipelines :many
SELECT * FROM pipeline_stages
WHERE workspace_id = $1 AND pipeline_id = ANY(sqlc.arg(pipeline_ids)::uuid[])
ORDER BY pipeline_id, position, id;

-- name: SeedPipelineStages :exec
SELECT seed_pipeline_stages(sqlc.arg(pipeline_id)::uuid, sqlc.arg(workspace_id)::uuid);

-- name: GetPipeline :one
SELECT * FROM pipelines WHERE workspace_id = $1 AND id = $2;

-- name: GetDefaultPipeline :one
SELECT * FROM pipelines WHERE workspace_id = $1 AND is_default;

-- name: InsertPipeline :one
INSERT INTO pipelines (workspace_id, name, is_default)
VALUES ($1, $2, $3)
RETURNING *;

-- name: UpdatePipeline :one
UPDATE pipelines SET name = $3
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- PipelineIsDefault separates "no such pipeline" (404) from "this pipeline may
-- not be deleted" (409); the DELETE keeps its own guard as a race backstop.
-- name: PipelineIsDefault :one
SELECT is_default FROM pipelines WHERE workspace_id = $1 AND id = $2;

-- name: DeletePipeline :execrows
DELETE FROM pipelines WHERE workspace_id = $1 AND id = $2 AND is_default = false;

-- name: ListPipelineStages :many
SELECT * FROM pipeline_stages
WHERE workspace_id = $1 AND pipeline_id = $2
ORDER BY position, id;

-- name: GetPipelineStage :one
SELECT * FROM pipeline_stages
WHERE workspace_id = $1 AND pipeline_id = $2 AND id = $3;

-- name: InsertPipelineStage :one
INSERT INTO pipeline_stages (
    workspace_id, pipeline_id, key, label, color, position, is_won, is_lost)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
RETURNING *;

-- name: UpdatePipelineStage :one
UPDATE pipeline_stages SET
    label = $4, color = $5, position = $6, is_won = $7, is_lost = $8
WHERE workspace_id = $1 AND pipeline_id = $2 AND id = $3
RETURNING *;

-- name: CountStageDeals :one
SELECT count(*)::bigint FROM deals WHERE workspace_id = $1 AND stage_id = $2;

-- name: DeletePipelineStage :execrows
DELETE FROM pipeline_stages s
WHERE s.workspace_id = $1 AND s.pipeline_id = $2 AND s.id = $3
  AND NOT EXISTS (
    SELECT 1 FROM deals d
    WHERE d.workspace_id = s.workspace_id AND d.stage_id = s.id
  );

-- name: ListDeals :many
SELECT d.*,
       p.name AS pipeline_name,
       s.label AS stage_label,
       s.color AS stage_color,
       s.is_won AS stage_is_won,
       s.is_lost AS stage_is_lost,
       s.position AS stage_position,
       d.position::text AS position_key,
       COALESCE(c.name, '') AS company_name,
       COALESCE(ct.email, '') AS contact_email
FROM deals d
JOIN pipelines p ON p.workspace_id = d.workspace_id AND p.id = d.pipeline_id
JOIN pipeline_stages s ON s.workspace_id = d.workspace_id AND s.id = d.stage_id
LEFT JOIN companies c ON c.workspace_id = d.workspace_id AND c.id = d.company_id
LEFT JOIN contacts ct ON ct.workspace_id = d.workspace_id AND ct.id = d.primary_contact_id
WHERE d.workspace_id = sqlc.arg(workspace_id)
  AND (sqlc.arg(seek)::bool = false
       OR (s.position, d.position, d.id) > (sqlc.arg(cursor_stage_position)::int,
                                            (sqlc.arg(cursor_position)::text)::numeric,
                                            sqlc.arg(cursor_id)::uuid))
ORDER BY s.position, d.position, d.id
LIMIT sqlc.arg(page_limit);

-- name: GetDeal :one
SELECT d.*,
       p.name AS pipeline_name,
       s.label AS stage_label,
       s.color AS stage_color,
       s.is_won AS stage_is_won,
       s.is_lost AS stage_is_lost,
       d.position::text AS position_key,
       COALESCE(c.name, '') AS company_name,
       COALESCE(ct.email, '') AS contact_email
FROM deals d
JOIN pipelines p ON p.workspace_id = d.workspace_id AND p.id = d.pipeline_id
JOIN pipeline_stages s ON s.workspace_id = d.workspace_id AND s.id = d.stage_id
LEFT JOIN companies c ON c.workspace_id = d.workspace_id AND c.id = d.company_id
LEFT JOIN contacts ct ON ct.workspace_id = d.workspace_id AND ct.id = d.primary_contact_id
WHERE d.workspace_id = $1 AND d.id = $2;

-- NextDealPosition appends past the current tail. The explicit numeric cast is
-- load-bearing: positions are fractional (a board drag writes the midpoint of
-- its neighbours into ONE row), so narrowing this to an integer would both
-- lose the fraction and fail to scan an existing fractional tail.
-- name: NextDealPosition :one
SELECT (COALESCE(max(position), 0) + 1000)::numeric AS position
FROM deals WHERE workspace_id = $1 AND pipeline_id = $2 AND stage_id = $3;

-- name: InsertDeal :one
INSERT INTO deals (
    workspace_id, pipeline_id, stage_id, company_id, primary_contact_id,
    owner_user_id, name, amount_micros, currency, close_date, position,
    source, source_campaign_id, source_thread_ref, created_by_actor)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
RETURNING *;

-- name: UpdateDeal :one
UPDATE deals SET
    pipeline_id = $3, stage_id = $4, company_id = $5,
    primary_contact_id = $6, owner_user_id = $7, name = $8,
    amount_micros = $9, currency = $10, close_date = $11, position = $12
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteDeal :execrows
DELETE FROM deals WHERE workspace_id = $1 AND id = $2;

-- name: ListContactEmails :many
SELECT * FROM contact_emails
WHERE workspace_id = $1 AND contact_id = $2
ORDER BY is_primary DESC, created_at, id;

-- name: InsertContactEmail :one
INSERT INTO contact_emails (workspace_id, contact_id, email, is_primary)
VALUES ($1, $2, $3, false)
RETURNING *;

-- name: ClearPrimaryContactEmails :exec
UPDATE contact_emails SET is_primary = false
WHERE workspace_id = $1 AND contact_id = $2;

-- name: SetPrimaryContactEmail :one
UPDATE contact_emails SET is_primary = true
WHERE workspace_id = $1 AND contact_id = $2 AND id = $3
RETURNING email;

-- name: UpdateContactPrimaryEmail :execrows
UPDATE contacts SET email = $3
WHERE workspace_id = $1 AND id = $2;

-- name: InsertNote :one
INSERT INTO notes (workspace_id, title, body, created_by_actor)
VALUES ($1,$2,$3,$4)
RETURNING *;

-- name: InsertNoteTarget :exec
INSERT INTO note_targets (workspace_id, note_id, contact_id, company_id, deal_id)
VALUES ($1,$2,$3,$4,$5);

-- name: ListNotesForContact :many
SELECT n.* FROM notes n
JOIN note_targets t ON t.workspace_id = n.workspace_id AND t.note_id = n.id
WHERE n.workspace_id = sqlc.arg(workspace_id) AND t.contact_id = sqlc.arg(contact_id)
  AND (sqlc.arg(seek)::bool = false
       OR (n.created_at, n.id) < (sqlc.arg(cursor_time)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY n.created_at DESC, n.id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListNotesForCompany :many
SELECT n.* FROM notes n
JOIN note_targets t ON t.workspace_id = n.workspace_id AND t.note_id = n.id
WHERE n.workspace_id = sqlc.arg(workspace_id) AND t.company_id = sqlc.arg(company_id)
  AND (sqlc.arg(seek)::bool = false
       OR (n.created_at, n.id) < (sqlc.arg(cursor_time)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY n.created_at DESC, n.id DESC
LIMIT sqlc.arg(page_limit);

-- name: ListNotesForDeal :many
SELECT n.* FROM notes n
JOIN note_targets t ON t.workspace_id = n.workspace_id AND t.note_id = n.id
WHERE n.workspace_id = sqlc.arg(workspace_id) AND t.deal_id = sqlc.arg(deal_id)
  AND (sqlc.arg(seek)::bool = false
       OR (n.created_at, n.id) < (sqlc.arg(cursor_time)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY n.created_at DESC, n.id DESC
LIMIT sqlc.arg(page_limit);

-- name: UpdateNote :one
UPDATE notes SET title = $3, body = $4
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteNote :execrows
DELETE FROM notes WHERE workspace_id = $1 AND id = $2;

-- name: InsertTask :one
INSERT INTO tasks (
    workspace_id, title, body, due_at, status, assignee_user_id, created_by_actor)
VALUES ($1,$2,$3,$4,$5,$6,$7)
RETURNING *;

-- name: InsertTaskTarget :exec
INSERT INTO task_targets (workspace_id, task_id, contact_id, company_id, deal_id)
VALUES ($1,$2,$3,$4,$5);

-- Open tasks first, soonest due first, undated last. `NULLS LAST` is spelled as
-- COALESCE to infinity so the sort key has no NULLs and can appear verbatim in
-- the keyset comparison; task.id closes the ordering.
-- name: ListTasksForContact :many
SELECT task.* FROM tasks task
JOIN task_targets t ON t.workspace_id = task.workspace_id AND t.task_id = task.id
WHERE task.workspace_id = sqlc.arg(workspace_id) AND t.contact_id = sqlc.arg(contact_id)
  AND (sqlc.arg(seek)::bool = false
       OR ((task.status IN ('done','cancelled')), COALESCE(task.due_at, 'infinity'::timestamptz), task.id)
          > (sqlc.arg(cursor_done)::bool, sqlc.arg(cursor_due)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY (task.status IN ('done','cancelled')), COALESCE(task.due_at, 'infinity'::timestamptz), task.id
LIMIT sqlc.arg(page_limit);

-- name: ListTasksForCompany :many
SELECT task.* FROM tasks task
JOIN task_targets t ON t.workspace_id = task.workspace_id AND t.task_id = task.id
WHERE task.workspace_id = sqlc.arg(workspace_id) AND t.company_id = sqlc.arg(company_id)
  AND (sqlc.arg(seek)::bool = false
       OR ((task.status IN ('done','cancelled')), COALESCE(task.due_at, 'infinity'::timestamptz), task.id)
          > (sqlc.arg(cursor_done)::bool, sqlc.arg(cursor_due)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY (task.status IN ('done','cancelled')), COALESCE(task.due_at, 'infinity'::timestamptz), task.id
LIMIT sqlc.arg(page_limit);

-- name: ListTasksForDeal :many
SELECT task.* FROM tasks task
JOIN task_targets t ON t.workspace_id = task.workspace_id AND t.task_id = task.id
WHERE task.workspace_id = sqlc.arg(workspace_id) AND t.deal_id = sqlc.arg(deal_id)
  AND (sqlc.arg(seek)::bool = false
       OR ((task.status IN ('done','cancelled')), COALESCE(task.due_at, 'infinity'::timestamptz), task.id)
          > (sqlc.arg(cursor_done)::bool, sqlc.arg(cursor_due)::timestamptz, sqlc.arg(cursor_id)::uuid))
ORDER BY (task.status IN ('done','cancelled')), COALESCE(task.due_at, 'infinity'::timestamptz), task.id
LIMIT sqlc.arg(page_limit);

-- name: UpdateTask :one
UPDATE tasks SET
    title = $3, body = $4, due_at = $5, status = $6, assignee_user_id = $7
WHERE workspace_id = $1 AND id = $2
RETURNING *;

-- name: DeleteTask :execrows
DELETE FROM tasks WHERE workspace_id = $1 AND id = $2;
