-- name: ListConversationProjections :many
SELECT
  r.id AS route_id,
  r.channel_type AS channel,
  CASE
    WHEN LOWER(COALESCE(r.conversation_type, '')) IN ('thread', 'topic') THEN 'thread'
    WHEN LOWER(COALESCE(r.conversation_type, '')) IN ('p2p', 'private', 'direct', 'dm') THEN 'private'
    ELSE 'group'
  END AS conversation_type,
  r.external_conversation_id AS conversation_id,
  COALESCE(r.external_thread_id, '') AS thread_id,
  COALESCE(
    NULLIF(TRIM(COALESCE(r.metadata->>'conversation_name', '')), ''),
    NULLIF(TRIM(COALESCE(r.metadata->>'conversation_handle', '')), ''),
    ''
  )::text AS conversation_name,
  COALESCE(NULLIF(TRIM(COALESCE(r.metadata->>'conversation_avatar_url', '')), ''), '')::text AS conversation_avatar_url
FROM channel.bot_channel_routes r
WHERE r.team_id = iam.memoh_current_team_id()
  AND r.bot_id = sqlc.arg(bot_id)
  AND r.id = ANY(sqlc.arg(route_ids)::uuid[])
  AND (
    sqlc.narg(channel_type)::text IS NULL
    OR LOWER(TRIM(r.channel_type)) = LOWER(TRIM(sqlc.narg(channel_type)::text))
  )
ORDER BY array_position(sqlc.arg(route_ids)::uuid[], r.id);
