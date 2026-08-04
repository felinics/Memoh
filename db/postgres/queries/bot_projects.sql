-- name: CreateBotProject :one
INSERT INTO bot_projects (
  bot_id, name, target_kind, remote_binding_id, path, created_by_user_id
)
VALUES (
  sqlc.arg(bot_id),
  sqlc.arg(name),
  sqlc.arg(target_kind),
  sqlc.narg(remote_binding_id)::uuid,
  sqlc.arg(path),
  sqlc.narg(created_by_user_id)::uuid
)
RETURNING *;

-- name: ListBotProjects :many
SELECT *
FROM bot_projects
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND (sqlc.arg(include_archived)::bool OR archived_at IS NULL)
ORDER BY created_at ASC, id ASC;

-- name: GetBotProject :one
-- Archived rows are returned on purpose: sessions bound to an archived
-- project keep resolving their working directory; callers that must refuse
-- archived projects (session creation) check archived_at themselves.
SELECT *
FROM bot_projects
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(project_id);

-- name: RenameBotProject :one
UPDATE bot_projects
SET name = sqlc.arg(name), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(project_id)
  AND archived_at IS NULL
RETURNING *;

-- name: ArchiveBotProject :one
UPDATE bot_projects
SET archived_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(project_id)
  AND archived_at IS NULL
RETURNING id;
