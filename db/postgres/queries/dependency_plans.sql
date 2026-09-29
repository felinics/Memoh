-- name: SaveDependencyPlan :exec
INSERT INTO bot_dependency_plans (bot_id, id, plan) VALUES ($1, $2, $3)
ON CONFLICT (team_id, bot_id, id) DO NOTHING;

-- name: GetDependencyPlan :one
SELECT plan FROM bot_dependency_plans
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1 AND id = $2;

-- name: GetDependencyGraph :one
SELECT graph FROM bot_dependency_graphs
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1;

-- name: ClaimDependencyGraph :one
INSERT INTO bot_dependency_graphs (bot_id, owner, lease_until)
SELECT sqlc.arg(bot_id), sqlc.arg(owner), now() + interval '60 seconds'
WHERE NOT EXISTS (
    SELECT 1 FROM bot_dependency_installations
    WHERE team_id = public.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id)
      AND status IN ('installing', 'updating', 'removing')
)
ON CONFLICT (team_id, bot_id) DO UPDATE
SET owner = EXCLUDED.owner, lease_until = EXCLUDED.lease_until
WHERE bot_dependency_graphs.owner = '' OR bot_dependency_graphs.lease_until < now()
RETURNING graph;

-- name: RenewDependencyGraph :execrows
UPDATE bot_dependency_graphs SET lease_until = now() + interval '60 seconds'
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1 AND owner = $2 AND lease_until > now();

-- name: WriteDependencyGraph :execrows
UPDATE bot_dependency_graphs SET graph = $3
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1 AND owner = $2 AND lease_until > now();

-- name: ReleaseDependencyGraph :exec
UPDATE bot_dependency_graphs SET owner = '', lease_until = now()
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1 AND owner = $2;

-- name: ListDependencyAppUsers :many
SELECT app.id::text AS installation_id, app.app_id
FROM bot_app_dependency_refs AS ref
JOIN bot_app_installations AS app ON app.id = ref.installation_id
WHERE app.team_id = public.memoh_current_team_id() AND app.bot_id = $1 AND ref.dependency_id = $2;

-- name: EnsureDependencyGraph :exec
INSERT INTO bot_dependency_graphs (bot_id) VALUES ($1)
ON CONFLICT (team_id, bot_id) DO NOTHING;

-- name: LockDependencyGraph :one
SELECT owner FROM bot_dependency_graphs
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1
FOR UPDATE;
