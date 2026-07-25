-- name: CreateMessageAsset :one
WITH message_locator AS MATERIALIZED (
  SELECT message.id, message.session_id
  FROM agent.bot_history_messages message
  WHERE message.team_id = iam.memoh_current_team_id()
    AND message.id = sqlc.arg(message_id)
),
owner_session AS MATERIALIZED (
  SELECT session.id, session.bot_id, session.compaction_epoch
  FROM agent.bot_sessions session
  JOIN message_locator message
    ON message.session_id = session.id
   AND session.team_id = iam.memoh_current_team_id()
  FOR UPDATE
),
target_message AS MATERIALIZED (
  SELECT message.id, message.session_id, message.compact_id
  FROM agent.bot_history_messages message
  JOIN message_locator locator ON locator.id = message.id
  LEFT JOIN owner_session owner ON owner.id = locator.session_id
  WHERE message.team_id = iam.memoh_current_team_id()
    AND (locator.session_id IS NULL OR owner.id IS NOT NULL)
  FOR UPDATE OF message
),
changed_asset AS MATERIALIZED (
  SELECT target.id, target.session_id, target.compact_id
  FROM target_message target
  LEFT JOIN agent.bot_history_message_assets existing
    ON existing.message_id = target.id
   AND existing.team_id = iam.memoh_current_team_id()
   AND existing.content_hash = sqlc.arg(content_hash)
  WHERE existing.id IS NULL
     OR existing.role IS DISTINCT FROM sqlc.arg(role)
     OR existing.ordinal IS DISTINCT FROM sqlc.arg(ordinal)
     OR existing.name IS DISTINCT FROM sqlc.arg(name)
     OR existing.metadata IS DISTINCT FROM sqlc.arg(metadata)
),
invalidated_session AS (
  UPDATE agent.bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  FROM changed_asset changed
  JOIN owner_session owner ON owner.id = changed.session_id
  JOIN agent.bot_history_message_compacts compact
    ON compact.id = changed.compact_id
   AND compact.team_id = iam.memoh_current_team_id()
  WHERE session.team_id = iam.memoh_current_team_id()
    AND session.id = owner.id
    AND compact.bot_id = owner.bot_id
    AND compact.session_id = owner.id
    AND compact.compaction_epoch = owner.compaction_epoch
    AND compact.status IN ('pending', 'ok')
  RETURNING session.id
),
upserted_asset AS (
  INSERT INTO agent.bot_history_message_assets (message_id, role, ordinal, content_hash, name, metadata)
  SELECT
    target.id,
    sqlc.arg(role),
    sqlc.arg(ordinal),
    sqlc.arg(content_hash),
    sqlc.arg(name),
    sqlc.arg(metadata)
  FROM target_message target
  CROSS JOIN (SELECT count(*) FROM invalidated_session) invalidation_done
  ON CONFLICT (team_id, message_id, content_hash) DO UPDATE SET
    role = EXCLUDED.role,
    ordinal = EXCLUDED.ordinal,
    name = EXCLUDED.name,
    metadata = EXCLUDED.metadata
  RETURNING id, message_id, role, ordinal, content_hash, name, metadata, created_at
)
SELECT id, message_id, role, ordinal, content_hash, name, metadata, created_at
FROM upserted_asset;

-- name: ListMessageAssets :many
SELECT id AS rel_id, message_id, role, ordinal, content_hash, name, metadata
FROM agent.bot_history_message_assets
WHERE team_id = iam.memoh_current_team_id() AND message_id = sqlc.arg(message_id)
ORDER BY ordinal ASC;

-- name: ListMessageAssetsBatch :many
SELECT id AS rel_id, message_id, role, ordinal, content_hash, name, metadata
FROM agent.bot_history_message_assets
WHERE team_id = iam.memoh_current_team_id() AND message_id = ANY(sqlc.arg(message_ids)::uuid[])
ORDER BY message_id, ordinal ASC;

-- name: CountMessageAssetsByBot :one
SELECT COUNT(*)
FROM agent.bot_history_message_assets a
JOIN agent.bot_history_messages m ON m.id = a.message_id
WHERE a.team_id = iam.memoh_current_team_id() AND m.team_id = iam.memoh_current_team_id() AND m.bot_id = sqlc.arg(bot_id);

-- name: DeleteMessageAssets :exec
DELETE FROM agent.bot_history_message_assets WHERE team_id = iam.memoh_current_team_id() AND message_id = sqlc.arg(message_id);
