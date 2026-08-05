-- name: CreateSession :one
INSERT INTO bot_sessions (
  bot_id, route_id, channel_type, type, session_mode, runtime_type, runtime_metadata, title, metadata, parent_session_id, created_by_user_id
)
VALUES (
  sqlc.arg(bot_id),
  sqlc.narg(route_id)::uuid,
  sqlc.narg(channel_type)::text,
  sqlc.arg(type),
  sqlc.arg(session_mode),
  sqlc.arg(runtime_type),
  sqlc.arg(runtime_metadata),
  sqlc.arg(title),
  sqlc.arg(metadata),
  sqlc.narg(parent_session_id)::uuid,
  sqlc.narg(created_by_user_id)::uuid
)
RETURNING *;

-- name: ForkSessionFromAssistantMessage :one
WITH source_session AS (
  SELECT s.*
  FROM bot_sessions s
  WHERE s.team_id = public.memoh_current_team_id()
    AND s.id = sqlc.arg(session_id)
    AND s.bot_id = sqlc.arg(bot_id)
    AND s.type = 'chat'
    AND s.deleted_at IS NULL
),
target_turn AS (
  SELECT
    vm.turn_id AS id,
    vm.turn_position AS position,
    vm.id AS message_id
  FROM source_session s
  JOIN bot_visible_history_messages vm ON vm.team_id = public.memoh_current_team_id()
    AND vm.session_id = s.id
    AND vm.id = sqlc.arg(message_id)
    AND vm.role = 'assistant'
    AND vm.turn_id IS NOT NULL
    AND vm.turn_position IS NOT NULL
  LIMIT 1
),
copy_messages AS (
  SELECT
    vm.id AS old_message_id,
    gen_random_uuid() AS new_message_id,
    vm.sender_channel_identity_id,
    vm.sender_account_user_id,
    vm.source_message_id,
    vm.source_reply_to_message_id,
    vm.role,
    vm.content,
    vm.metadata,
    vm.usage,
    vm.session_mode,
    vm.runtime_type,
    vm.model_id,
    vm.display_text,
    vm.turn_id AS old_turn_id,
    vm.turn_position,
    vm.turn_message_seq,
    vm.created_at
  FROM bot_visible_history_messages vm
  JOIN target_turn tt ON (
    vm.turn_position < tt.position
    OR vm.turn_position = tt.position
  )
  WHERE vm.team_id = public.memoh_current_team_id()
    AND vm.session_id = sqlc.arg(session_id)
  ORDER BY vm.turn_position ASC, vm.turn_message_seq ASC, vm.created_at ASC, vm.id ASC
),
copy_turns AS (
  SELECT
    cm.old_turn_id,
    gen_random_uuid() AS new_turn_id,
    cm.turn_position AS old_position,
    ROW_NUMBER() OVER (ORDER BY cm.turn_position ASC)::bigint AS new_position
  FROM (
    SELECT DISTINCT old_turn_id, turn_position
    FROM copy_messages
  ) cm
  ORDER BY cm.turn_position ASC
),
next_turn_position AS (
  SELECT COALESCE(MAX(new_position), 0) + 1 AS value
  FROM copy_turns
),
fork_anchor_message AS (
  SELECT assistant_message.new_message_id
  FROM target_turn tt
  JOIN copy_messages assistant_message ON assistant_message.old_message_id = tt.message_id
  LIMIT 1
),
prepared_metadata AS (
  SELECT COALESCE(sqlc.arg(metadata)::jsonb, '{}'::jsonb) AS value
),
fork_plan AS (
  SELECT
    s.*,
    fam.new_message_id AS fork_message_id,
    ntp.value AS next_turn_position_value
  FROM source_session s
  JOIN fork_anchor_message fam ON true
  CROSS JOIN next_turn_position ntp
  WHERE EXISTS (SELECT 1 FROM copy_turns)
),
created_session AS (
  INSERT INTO bot_sessions (
    bot_id,
    channel_type,
    type,
    session_mode,
    runtime_type,
    runtime_metadata,
    title,
    metadata,
    next_turn_position,
    created_by_user_id
  )
  SELECT
    fp.bot_id,
    fp.channel_type,
    fp.type,
    fp.session_mode,
    fp.runtime_type,
    fp.runtime_metadata,
    sqlc.arg(title),
    jsonb_set(
      pm.value,
      '{forked_from}',
      COALESCE(pm.value->'forked_from', '{}'::jsonb) || jsonb_build_object('fork_message_id', fp.fork_message_id::text),
      true
    ),
    fp.next_turn_position_value,
    sqlc.narg(created_by_user_id)::uuid
  FROM fork_plan fp
  CROSS JOIN prepared_metadata pm
  RETURNING *
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
    cm.new_message_id,
    cs.bot_id,
    cs.id,
    cm.sender_channel_identity_id,
    cm.sender_account_user_id,
    cm.source_message_id,
    cm.source_reply_to_message_id,
    cm.role,
    cm.content,
    cm.metadata,
    cm.usage,
    cm.session_mode,
    cm.runtime_type,
    cm.model_id,
    cm.display_text,
    ct.new_turn_id,
    ct.new_position,
    cm.turn_message_seq,
    true,
    cm.created_at
  FROM copy_messages cm
  JOIN copy_turns ct ON ct.old_turn_id = cm.old_turn_id
  CROSS JOIN created_session cs
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
    cm.new_message_id,
    a.role,
    a.ordinal,
    a.content_hash,
    a.mime,
    a.size_bytes,
    a.storage_key,
    a.name,
    a.metadata
  FROM bot_history_message_assets a
  JOIN copy_messages cm ON cm.old_message_id = a.message_id
  JOIN inserted_messages im ON im.id = cm.new_message_id
  WHERE a.team_id = public.memoh_current_team_id()
  RETURNING id
)
SELECT cs.*
FROM created_session cs
CROSS JOIN (SELECT count(*) AS copied_asset_count FROM copied_assets) copied_asset_counts;

-- name: GetSessionByID :one
SELECT *
FROM bot_sessions
WHERE team_id = public.memoh_current_team_id()
  AND id = $1
  AND deleted_at IS NULL;

-- name: NextSessionRuntimeFenceToken :one
SELECT nextval('session_runtime_fencing_token_seq')::bigint;

-- name: ActivateSessionRuntimeFence :one
UPDATE bot_sessions
SET runtime_fencing_token = sqlc.arg(runtime_fencing_token)
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(session_id)
  AND bot_id = sqlc.arg(bot_id)
  AND runtime_fencing_token <= sqlc.arg(runtime_fencing_token)
  AND deleted_at IS NULL
RETURNING runtime_fencing_token;

-- name: LockSessionRuntimeFenceForActivation :one
SELECT runtime_fencing_token
FROM bot_sessions
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(session_id)
  AND bot_id = sqlc.arg(bot_id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: LockSessionRuntimeFence :one
SELECT runtime_fencing_token
FROM bot_sessions
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(session_id)
  AND bot_id = sqlc.arg(bot_id)
  AND runtime_fencing_token = sqlc.arg(runtime_fencing_token)
  AND deleted_at IS NULL
FOR NO KEY UPDATE;

-- name: LockSessionDecisionSequence :one
SELECT id
FROM bot_sessions
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(session_id)
  AND bot_id = sqlc.arg(bot_id)
  AND deleted_at IS NULL
FOR UPDATE;

-- name: ListSessionsByBot :many
SELECT
  s.id, s.bot_id, s.route_id, s.channel_type, s.type, s.session_mode, s.runtime_type, s.runtime_metadata, s.title, s.metadata,
  s.parent_session_id, s.created_by_user_id, s.created_at, s.updated_at, s.deleted_at
FROM bot_sessions s
WHERE s.team_id = public.memoh_current_team_id()
  AND s.bot_id = sqlc.arg(bot_id)
  AND s.deleted_at IS NULL
ORDER BY s.updated_at DESC;

-- name: ListSessionsByBotAndCreatedByUser :many
SELECT
  s.id, s.bot_id, s.route_id, s.channel_type, s.type, s.session_mode, s.runtime_type, s.runtime_metadata, s.title, s.metadata,
  s.parent_session_id, s.created_by_user_id, s.created_at, s.updated_at, s.deleted_at
FROM bot_sessions s
WHERE s.team_id = public.memoh_current_team_id()
  AND s.bot_id = sqlc.arg(bot_id)
  AND s.created_by_user_id = sqlc.arg(created_by_user_id)
  AND s.deleted_at IS NULL
ORDER BY s.updated_at DESC;

-- name: ListSessionsByBotPaged :many
-- Cursor uses (updated_at, id) so pages stay stable when many rows share an
-- updated_at. Callers always pass an explicit types filter; to opt out of
-- filtering, pass every known type.
SELECT
  s.id, s.bot_id, s.route_id, s.channel_type, s.type, s.session_mode, s.runtime_type, s.runtime_metadata, s.title, s.metadata,
  s.parent_session_id, s.created_by_user_id, s.created_at, s.updated_at, s.deleted_at
FROM bot_sessions s
WHERE s.team_id = public.memoh_current_team_id()
  AND s.bot_id = sqlc.arg(bot_id)
  AND s.deleted_at IS NULL
  AND s.type = ANY(sqlc.arg(types)::text[])
  AND (
    NOT sqlc.arg(use_parent_session)::bool
    OR s.parent_session_id = sqlc.narg(parent_session_id)::uuid
  )
  AND (
    NOT sqlc.arg(use_cursor)::bool
    OR (s.updated_at, s.id) < (sqlc.arg(cursor_updated_at)::timestamptz, sqlc.arg(cursor_id)::uuid)
  )
ORDER BY s.updated_at DESC, s.id DESC
LIMIT sqlc.arg(limit_count)::int;

-- name: ListSessionsByBotAndCreatedByUserPaged :many
SELECT
  s.id, s.bot_id, s.route_id, s.channel_type, s.type, s.session_mode, s.runtime_type, s.runtime_metadata, s.title, s.metadata,
  s.parent_session_id, s.created_by_user_id, s.created_at, s.updated_at, s.deleted_at
FROM bot_sessions s
WHERE s.team_id = public.memoh_current_team_id()
  AND s.bot_id = sqlc.arg(bot_id)
  AND s.created_by_user_id = sqlc.arg(created_by_user_id)
  AND s.deleted_at IS NULL
  AND s.type = ANY(sqlc.arg(types)::text[])
  AND (
    NOT sqlc.arg(use_parent_session)::bool
    OR s.parent_session_id = sqlc.narg(parent_session_id)::uuid
  )
  AND (
    NOT sqlc.arg(use_cursor)::bool
    OR (s.updated_at, s.id) < (sqlc.arg(cursor_updated_at)::timestamptz, sqlc.arg(cursor_id)::uuid)
  )
ORDER BY s.updated_at DESC, s.id DESC
LIMIT sqlc.arg(limit_count)::int;

-- name: ListSessionsByRoute :many
SELECT *
FROM bot_sessions
WHERE team_id = public.memoh_current_team_id()
  AND route_id = sqlc.arg(route_id)
  AND deleted_at IS NULL
ORDER BY updated_at DESC;

-- name: UpdateSessionTitle :one
UPDATE bot_sessions
SET title = sqlc.arg(title), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING *;

-- name: UpdateSessionTitleWithRuntimeFence :one
UPDATE bot_sessions
SET title = sqlc.arg(title), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND bot_id = sqlc.arg(bot_id)
  AND runtime_fencing_token = sqlc.arg(runtime_fencing_token)
  AND deleted_at IS NULL
RETURNING *;

-- name: UpdateSessionMetadata :one
UPDATE bot_sessions
SET metadata = sqlc.arg(metadata), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING *;

-- name: UpdateSessionMetadataWithRuntimeFence :one
UPDATE bot_sessions
SET metadata = sqlc.arg(metadata), updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND bot_id = sqlc.arg(bot_id)
  AND runtime_fencing_token = sqlc.arg(runtime_fencing_token)
  AND deleted_at IS NULL
RETURNING *;

-- name: UpdateSessionTypeAndMetadata :one
UPDATE bot_sessions
SET type = sqlc.arg(type),
    session_mode = sqlc.arg(session_mode),
    runtime_type = sqlc.arg(runtime_type),
    runtime_metadata = sqlc.arg(runtime_metadata),
    metadata = sqlc.arg(metadata),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id) AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteSession :exec
UPDATE bot_sessions
SET deleted_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: TouchSession :exec
UPDATE bot_sessions
SET updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id) AND deleted_at IS NULL;

-- name: SetSessionNextTurnPosition :exec
UPDATE bot_sessions
SET next_turn_position = sqlc.arg(next_turn_position)::bigint
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(session_id);

-- name: GetSessionDiscussCursor :one
SELECT *
FROM bot_session_discuss_cursors
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
  AND scope_key = sqlc.arg(scope_key);

-- name: ListSessionDiscussCursorsByBot :many
SELECT c.*
FROM bot_session_discuss_cursors c
JOIN bot_sessions s ON s.id = c.session_id
WHERE c.team_id = public.memoh_current_team_id()
  AND s.team_id = public.memoh_current_team_id()
  AND s.bot_id = sqlc.arg(bot_id)
ORDER BY c.updated_at ASC, c.session_id ASC, c.scope_key ASC;

-- name: DeleteSessionDiscussCursorsByBot :exec
DELETE FROM bot_session_discuss_cursors
WHERE team_id = public.memoh_current_team_id()
  AND session_id IN (
  SELECT id
  FROM bot_sessions
  WHERE team_id = public.memoh_current_team_id()
    AND bot_id = sqlc.arg(bot_id)
);

-- name: UpsertSessionDiscussCursor :one
INSERT INTO bot_session_discuss_cursors (
  session_id, scope_key, route_id, source, consumed_cursor, consumed_event_cursor
)
VALUES (
  sqlc.arg(session_id),
  sqlc.arg(scope_key),
  sqlc.narg(route_id)::uuid,
  sqlc.arg(source),
  sqlc.arg(consumed_cursor),
  sqlc.arg(consumed_event_cursor)
)
ON CONFLICT (team_id, session_id, scope_key) DO UPDATE
SET route_id = COALESCE(EXCLUDED.route_id, bot_session_discuss_cursors.route_id),
    source = EXCLUDED.source,
    consumed_cursor = GREATEST(bot_session_discuss_cursors.consumed_cursor, EXCLUDED.consumed_cursor),
    consumed_event_cursor = GREATEST(bot_session_discuss_cursors.consumed_event_cursor, EXCLUDED.consumed_event_cursor),
    updated_at = now()
RETURNING *;

-- name: ListSubagentSessionsByParent :many
SELECT *
FROM bot_sessions
WHERE team_id = public.memoh_current_team_id()
  AND parent_session_id = sqlc.arg(parent_session_id)
  AND type = 'subagent'
  AND deleted_at IS NULL
  AND EXISTS (
    SELECT 1
    FROM subagent_configs config
    WHERE config.team_id = public.memoh_current_team_id()
      AND config.session_id = bot_sessions.id
  )
ORDER BY created_at DESC;

-- name: SoftDeleteSessionsByBot :exec
UPDATE bot_sessions
SET deleted_at = now(), updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id) AND deleted_at IS NULL;
