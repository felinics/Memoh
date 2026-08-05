-- name: CreateSubagentConfig :one
INSERT INTO subagent_configs (
  session_id,
  model_uuid,
  model_id,
  provider_name,
  forked
) VALUES (
  $1, $2, $3, $4, $5
)
RETURNING *;

-- name: GetSubagentConfig :one
SELECT *
FROM subagent_configs
WHERE session_id = $1;

-- name: CreateSubagentForkContext :one
WITH target_session AS MATERIALIZED (
  SELECT child.id, child.bot_id, child.team_id
  FROM bot_sessions child
  JOIN bot_sessions parent
    ON parent.team_id = public.memoh_current_team_id()
   AND parent.id = sqlc.arg(parent_session_id)
   AND parent.id = child.parent_session_id
   AND parent.bot_id = child.bot_id
   AND parent.deleted_at IS NULL
  WHERE child.team_id = public.memoh_current_team_id()
    AND child.id = sqlc.arg(session_id)
    AND child.type = 'subagent'
    AND child.deleted_at IS NULL
),
context_entries AS MATERIALIZED (
  SELECT entry, ordinality::bigint AS position
  FROM jsonb_array_elements(sqlc.arg(context_messages)::jsonb)
       WITH ORDINALITY AS items(entry, ordinality)
),
prepared_messages AS MATERIALIZED (
  SELECT
    gen_random_uuid() AS id,
    gen_random_uuid() AS turn_id,
    entries.position,
    source.id AS source_id,
    target.id AS session_id,
    target.bot_id,
    source.sender_channel_identity_id,
    source.sender_account_user_id,
    source.source_message_id,
    source.source_reply_to_message_id,
    COALESCE(source.role, entries.entry->>'role') AS role,
    COALESCE(source.content, entries.entry->'message') AS content,
    COALESCE(source.metadata, '{}'::jsonb)
      || jsonb_build_object(
        'context_scope', 'subagent_fork',
        'context_version', 1,
        'source_session_id', sqlc.arg(parent_session_id)::text
      )
      || CASE
        WHEN source.id IS NULL THEN '{}'::jsonb
        ELSE jsonb_build_object('source_message_id', source.id::text)
      END AS metadata,
    COALESCE(source.runtime_type, 'model') AS runtime_type,
    source.model_id,
    source.display_text,
    COALESCE(source.created_at, now()) AS created_at
  FROM context_entries entries
  CROSS JOIN target_session target
  LEFT JOIN bot_history_messages source
    ON source.team_id = public.memoh_current_team_id()
   AND source.id = NULLIF(entries.entry->>'source_message_id', '')::uuid
   AND source.session_id = sqlc.arg(parent_session_id)
   AND source.bot_id = target.bot_id
),
updated_session AS (
  UPDATE bot_sessions session
  SET next_turn_position = (SELECT count(*)::bigint + 1 FROM context_entries)
  FROM target_session target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
  RETURNING session.id
),
inserted_messages AS (
  INSERT INTO bot_history_messages (
    id,
    bot_id,
    session_id,
    sender_channel_identity_id,
    sender_account_user_id,
    source_message_id,
    source_reply_to_message_id,
    role,
    content,
    metadata,
    usage,
    session_mode,
    runtime_type,
    model_id,
    display_text,
    turn_id,
    turn_position,
    turn_message_seq,
    turn_visible,
    created_at
  )
  SELECT
    prepared.id,
    prepared.bot_id,
    prepared.session_id,
    prepared.sender_channel_identity_id,
    prepared.sender_account_user_id,
    prepared.source_message_id,
    prepared.source_reply_to_message_id,
    prepared.role,
    prepared.content,
    prepared.metadata,
    NULL,
    'subagent',
    prepared.runtime_type,
    prepared.model_id,
    prepared.display_text,
    prepared.turn_id,
    prepared.position,
    1,
    false,
    prepared.created_at
  FROM prepared_messages prepared
  JOIN updated_session updated ON updated.id = prepared.session_id
  RETURNING id
),
copied_assets AS (
  INSERT INTO bot_history_message_assets (
    message_id,
    role,
    ordinal,
    content_hash,
    mime,
    size_bytes,
    storage_key,
    name,
    metadata
  )
  SELECT
    prepared.id,
    asset.role,
    asset.ordinal,
    asset.content_hash,
    asset.mime,
    asset.size_bytes,
    asset.storage_key,
    asset.name,
    asset.metadata
  FROM prepared_messages prepared
  JOIN inserted_messages inserted ON inserted.id = prepared.id
  JOIN bot_history_message_assets asset
    ON asset.team_id = public.memoh_current_team_id()
   AND asset.message_id = prepared.source_id
  RETURNING id
)
SELECT
  (SELECT count(*)::bigint FROM inserted_messages) AS inserted_count,
  (SELECT count(*)::bigint FROM copied_assets) AS copied_asset_count;

-- name: ListSubagentForkContext :many
SELECT role, content
FROM bot_history_messages
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
  AND session_mode = 'subagent'
  AND turn_visible = false
  AND metadata->>'context_scope' = 'subagent_fork'
ORDER BY turn_position ASC, turn_message_seq ASC, created_at ASC, id ASC;
