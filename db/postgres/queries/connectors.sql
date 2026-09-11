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
-- Keep the local binding until every App reference and status change commits.
-- Any failure rolls back the entire statement, leaving a retryable binding.
WITH target AS MATERIALIZED (
    SELECT c.team_id, c.bot_id, c.connection_id FROM connectors c
    WHERE c.team_id = public.memoh_current_team_id()
      AND c.bot_id = $1 AND c.connection_id = $2
), unlinked AS (
    UPDATE bot_app_connector_refs r
    SET connection_id = '', updated_at = now()
    FROM bot_app_installations i, target t
    WHERE r.team_id = t.team_id AND i.team_id = t.team_id
      AND r.installation_id = i.id AND i.bot_id = t.bot_id
      AND r.connection_id = t.connection_id
    RETURNING r.team_id, r.installation_id, r.required
), demoted AS (
    UPDATE bot_app_installations i
    SET status = 'partial', updated_at = now()
    WHERE i.status = 'installed'
      AND EXISTS (
        SELECT 1 FROM unlinked u WHERE u.team_id = i.team_id
          AND u.installation_id = i.id AND u.required
      )
    RETURNING i.id
)
DELETE FROM connectors c USING target t
WHERE c.team_id = t.team_id AND c.bot_id = t.bot_id
  AND c.connection_id = t.connection_id;
