-- name: ListMemoryProviders :many
SELECT * FROM memory.memory_providers WHERE team_id = iam.memoh_current_team_id() ORDER BY created_at ASC;

-- name: GetMemoryProviderByID :one
SELECT * FROM memory.memory_providers WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: GetDefaultMemoryProvider :one
SELECT * FROM memory.memory_providers WHERE team_id = iam.memoh_current_team_id() AND is_default = true LIMIT 1;

-- name: CreateMemoryProvider :one
INSERT INTO memory.memory_providers (name, provider, config, is_default)
VALUES ($1, $2, $3, $4)
RETURNING *;

-- name: UpdateMemoryProvider :one
UPDATE memory.memory_providers
SET name = $2,
    config = $3,
    updated_at = now()
WHERE team_id = iam.memoh_current_team_id() AND id = $1
RETURNING *;

-- name: DeleteMemoryProvider :exec
DELETE FROM memory.memory_providers WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: CountMemoryProvidersByDefault :one
SELECT COUNT(*) FROM memory.memory_providers WHERE team_id = iam.memoh_current_team_id() AND is_default = true;
