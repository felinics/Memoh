-- name: CreateBotWorkdir :one
INSERT INTO bot_workdirs (
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

-- name: ListBotWorkdirs :many
SELECT *
FROM bot_workdirs
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND (sqlc.arg(include_archived)::bool OR archived_at IS NULL)
ORDER BY created_at ASC, id ASC;

-- name: GetBotWorkdir :one
-- Archived rows are returned on purpose: sessions bound to an archived
-- workdir keep resolving their working directory; callers that must refuse
-- archived workdirs (session creation) check archived_at themselves.
SELECT *
FROM bot_workdirs
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(workdir_id);

-- name: RenameBotWorkdir :one
UPDATE bot_workdirs
SET name = sqlc.arg(name), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(workdir_id)
  AND archived_at IS NULL
RETURNING *;

-- name: ArchiveBotWorkdir :one
UPDATE bot_workdirs
SET archived_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND id = sqlc.arg(workdir_id)
  AND archived_at IS NULL
RETURNING id;
