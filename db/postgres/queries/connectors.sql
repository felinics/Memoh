-- name: CreateConnector :one
INSERT INTO connectors (bot_id, connection_id, alias)
VALUES ($1, $2, $3)
RETURNING *;

-- name: GetConnectorByConnectionID :one
SELECT *
FROM connectors
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND connection_id = $2
LIMIT 1;

-- name: ListConnectorsByBotID :many
SELECT *
FROM connectors
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
ORDER BY connection_id ASC;

-- name: UpdateConnectorEnabled :execrows
UPDATE connectors
SET enabled = $3,
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND connection_id = $2;

-- name: DeleteConnector :exec
DELETE FROM connectors
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND connection_id = $2;
