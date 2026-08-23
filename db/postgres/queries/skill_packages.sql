-- name: GetBotSkillPackageInstallation :one
SELECT id, team_id, bot_id, workspace_target_id, registry_id, package_id,
       revision, installed_at, updated_at
FROM bot_skill_package_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND workspace_target_id = $2
  AND registry_id = $3
  AND package_id = $4
LIMIT 1;

-- name: GetBotSkillPackageInstallationByID :one
SELECT id, team_id, bot_id, workspace_target_id, registry_id, package_id,
       revision, installed_at, updated_at
FROM bot_skill_package_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND id = $2
LIMIT 1;

-- name: ListBotSkillPackageInstallations :many
SELECT id, team_id, bot_id, workspace_target_id, registry_id, package_id,
       revision, installed_at, updated_at
FROM bot_skill_package_installations
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1
ORDER BY registry_id, package_id, workspace_target_id;

-- name: UpsertBotSkillPackageInstallation :one
INSERT INTO bot_skill_package_installations (
  bot_id, workspace_target_id, registry_id, package_id, revision
)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (team_id, bot_id, workspace_target_id, registry_id, package_id)
DO UPDATE SET revision = EXCLUDED.revision,
              updated_at = now()
RETURNING id, team_id, bot_id, workspace_target_id, registry_id, package_id,
          revision, installed_at, updated_at;

-- name: DeleteBotSkillPackageInstallation :one
DELETE FROM bot_skill_package_installations
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND id = $2
RETURNING id, team_id, bot_id, workspace_target_id, registry_id, package_id,
          revision, installed_at, updated_at;
