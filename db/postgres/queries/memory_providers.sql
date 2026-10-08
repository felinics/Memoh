-- name: ListMemoryProviders :many
SELECT * FROM memory_providers WHERE team_id = public.memoh_current_team_id() ORDER BY created_at ASC;

-- name: GetMemoryProviderByID :one
SELECT * FROM memory_providers WHERE team_id = public.memoh_current_team_id() AND id = $1;

-- name: GetBuiltinMemoryProvider :one
-- The team's Built-in Memory configuration row: the earliest builtin row,
-- matching the one the memory settings page has always edited.
SELECT * FROM memory_providers
WHERE team_id = public.memoh_current_team_id() AND provider = 'builtin'
ORDER BY created_at ASC, id ASC
LIMIT 1;

-- name: CreateMemoryProvider :one
INSERT INTO memory_providers (name, provider, config, is_default)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateMemoryProvider :one
UPDATE memory_providers
SET name = $2,
    config = $3,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = $1
RETURNING *;

-- name: CountMemoryProvidersByDefault :one
SELECT COUNT(*) FROM memory_providers WHERE team_id = public.memoh_current_team_id() AND is_default = true;
