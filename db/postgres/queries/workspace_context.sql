-- name: GetBotWorkspaceContextSnapshot :one
SELECT *
FROM public.bot_workspace_context_snapshots
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND target_id = $2
LIMIT 1;

-- name: InvalidateBotWorkspaceContextSnapshots :execrows
UPDATE public.bot_workspace_context_snapshots
SET requested_generation = requested_generation + 1,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1;

-- name: BeginBotWorkspaceContextRefresh :one
INSERT INTO public.bot_workspace_context_snapshots (
    bot_id,
    target_id,
    requested_generation,
    applied_generation,
    status
)
VALUES ($1, $2, 1, 0, 'refreshing')
ON CONFLICT (team_id, bot_id, target_id)
DO UPDATE SET
    requested_generation = bot_workspace_context_snapshots.requested_generation + 1,
    status = 'refreshing',
    last_refresh_error = NULL,
    updated_at = now()
RETURNING requested_generation;

-- name: CompleteBotWorkspaceContextRefresh :one
UPDATE public.bot_workspace_context_snapshots
SET applied_generation = $3,
    status = 'ready',
    payload = $4,
    content_hash = $5,
    last_refresh_error = NULL,
    refreshed_at = now(),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND target_id = $2
  AND requested_generation = $3
RETURNING *;

-- name: MarkBotWorkspaceContextSourceInvalid :execrows
UPDATE public.bot_workspace_context_snapshots
SET applied_generation = $3,
    status = 'source_invalid',
    last_refresh_error = $4,
    refreshed_at = now(),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND target_id = $2
  AND requested_generation = $3;

-- name: FailBotWorkspaceContextRefresh :execrows
UPDATE public.bot_workspace_context_snapshots
SET status = CASE WHEN payload IS NULL THEN 'empty' ELSE 'ready' END,
    last_refresh_error = $4,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND target_id = $2
  AND requested_generation = $3;
