-- name: UpsertContainer :exec
INSERT INTO runtime.containers (
  bot_id, container_id, container_name, image, status, namespace, auto_start,
  container_path, workspace_backend, last_started_at, last_stopped_at
)
VALUES (
  sqlc.arg(bot_id),
  sqlc.arg(container_id),
  sqlc.arg(container_name),
  sqlc.arg(image),
  sqlc.arg(status),
  sqlc.arg(namespace),
  sqlc.arg(auto_start),
  sqlc.arg(container_path),
  sqlc.arg(workspace_backend),
  sqlc.arg(last_started_at),
  sqlc.arg(last_stopped_at)
)
ON CONFLICT (team_id, container_id) DO UPDATE SET
  bot_id = EXCLUDED.bot_id,
  container_name = EXCLUDED.container_name,
  image = EXCLUDED.image,
  status = EXCLUDED.status,
  namespace = EXCLUDED.namespace,
  auto_start = EXCLUDED.auto_start,
  container_path = EXCLUDED.container_path,
  workspace_backend = EXCLUDED.workspace_backend,
  last_started_at = EXCLUDED.last_started_at,
  last_stopped_at = EXCLUDED.last_stopped_at,
  updated_at = now();

-- name: GetContainerByBotID :one
SELECT * FROM runtime.containers WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id) ORDER BY updated_at DESC LIMIT 1;

-- name: DeleteContainerByBotID :exec
DELETE FROM runtime.containers WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id);

-- name: UpdateContainerStatus :exec
UPDATE runtime.containers
SET status = sqlc.arg(status), updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id);

-- name: UpdateContainerStarted :exec
UPDATE runtime.containers
SET status = 'running', last_started_at = now(), updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id);

-- name: UpdateContainerStopped :exec
UPDATE runtime.containers
SET status = 'stopped', last_stopped_at = now(), updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id);

-- name: ListAutoStartContainers :many
SELECT * FROM runtime.containers WHERE team_id = iam.memoh_current_team_id() AND auto_start = true ORDER BY updated_at DESC;
