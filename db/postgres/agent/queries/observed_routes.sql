-- name: ListObservedRoutes :many
SELECT
  s.route_id,
  MAX(m.created_at)::timestamptz AS last_observed_at
FROM agent.bot_visible_history_messages AS m
JOIN agent.bot_sessions AS s
  ON s.team_id = m.team_id
 AND s.bot_id = m.bot_id
 AND s.id = m.session_id
WHERE m.team_id = iam.memoh_current_team_id()
  AND m.bot_id = sqlc.arg(bot_id)
  AND s.route_id IS NOT NULL
  AND (
    sqlc.narg(channel_identity_id)::uuid IS NULL
    OR m.sender_channel_identity_id = sqlc.narg(channel_identity_id)::uuid
  )
GROUP BY s.route_id
ORDER BY last_observed_at DESC;
