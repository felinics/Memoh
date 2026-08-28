-- name: CreateMessage :one
INSERT INTO bot_history_messages (
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
  event_id,
  display_text,
  run_id
)
VALUES (
  sqlc.arg(bot_id),
  sqlc.narg(session_id)::uuid,
  sqlc.narg(sender_channel_identity_id)::uuid,
  sqlc.narg(sender_user_id)::uuid,
  sqlc.narg(external_message_id)::text,
  sqlc.narg(source_reply_to_message_id)::text,
  sqlc.arg(role),
  sqlc.arg(content),
  sqlc.arg(metadata),
  sqlc.arg(usage),
  sqlc.arg(session_mode),
  sqlc.arg(runtime_type),
  sqlc.narg(model_id)::uuid,
  sqlc.narg(event_id)::uuid,
  sqlc.narg(display_text)::text,
  sqlc.narg(run_id)::uuid
)
RETURNING
  id,
  bot_id,
  session_id,
  sender_channel_identity_id,
  sender_account_user_id AS sender_user_id,
  source_message_id AS external_message_id,
  source_reply_to_message_id,
  role,
  content,
  metadata,
  usage,
  session_mode,
  runtime_type,
  event_id,
  display_text,
  created_at;

-- name: CreateMessageWithTurn :one
WITH target AS (
  SELECT
    existing.turn_id,
    existing.session_id,
    existing.turn_position,
    bool_or(existing.turn_visible) AS turn_visible
  FROM bot_history_messages existing
  WHERE existing.team_id = public.memoh_current_team_id() AND existing.turn_id = sqlc.arg(turn_id)
    AND existing.session_id = sqlc.narg(session_id)::uuid
    AND existing.bot_id = sqlc.arg(bot_id)
    AND existing.turn_position IS NOT NULL
  GROUP BY existing.turn_id, existing.session_id, existing.turn_position
  LIMIT 1
)
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
  event_id,
  display_text,
  turn_id,
  turn_position,
  turn_message_seq,
  turn_visible
)
SELECT
  sqlc.arg(message_id),
  sqlc.arg(bot_id),
  target.session_id,
  sqlc.narg(sender_channel_identity_id)::uuid,
  sqlc.narg(sender_user_id)::uuid,
  sqlc.narg(external_message_id)::text,
  sqlc.narg(source_reply_to_message_id)::text,
  sqlc.arg(role),
  sqlc.arg(content),
  sqlc.arg(metadata),
  sqlc.arg(usage),
  sqlc.arg(session_mode),
  sqlc.arg(runtime_type),
  sqlc.narg(model_id)::uuid,
  sqlc.narg(event_id)::uuid,
  sqlc.narg(display_text)::text,
  target.turn_id,
  target.turn_position,
  sqlc.arg(turn_message_seq),
  target.turn_visible
FROM target
RETURNING
  id,
  bot_id,
  session_id,
  sender_channel_identity_id,
  sender_account_user_id AS sender_user_id,
  source_message_id AS external_message_id,
  source_reply_to_message_id,
  role,
  content,
  metadata,
  usage,
  session_mode,
  runtime_type,
  event_id,
  display_text,
  created_at;

-- name: AllocateSessionTurnPosition :one
UPDATE bot_sessions
SET next_turn_position = next_turn_position + 1
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(session_id)
RETURNING (next_turn_position - 1)::bigint AS position;

-- name: CreateMessageWithHistoryTurn :one
-- Writes the request message of a turn.
--
-- The turn position is allocated here only when the caller did not bring one.
-- A run admitted through session_runs already drew turn_id and turn_position
-- from this same counter (SR-TURN-001, SR-DUR-002), so bumping again would
-- spend a second position and file the message under a turn the client was
-- never told about. Entry points with no admission — channel inbound,
-- schedules — pass NULL and keep allocating here.
WITH next_position AS (
  UPDATE bot_sessions
  SET next_turn_position = next_turn_position + 1
  WHERE bot_sessions.team_id = public.memoh_current_team_id()
    AND bot_sessions.id = sqlc.arg(session_id)
    AND sqlc.narg(turn_position)::bigint IS NULL
  RETURNING (next_turn_position - 1)::bigint AS position
),
turn_slot AS (
  SELECT sqlc.narg(turn_position)::bigint AS position
  WHERE sqlc.narg(turn_position)::bigint IS NOT NULL
  UNION ALL
  SELECT next_position.position FROM next_position
),
inserted_message AS (
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
    event_id,
    display_text,
    run_id,
    turn_id,
    turn_position,
    turn_message_seq,
    turn_visible
  )
  SELECT
    sqlc.arg(message_id),
    sqlc.arg(bot_id),
    sqlc.arg(session_id),
    sqlc.narg(sender_channel_identity_id)::uuid,
    sqlc.narg(sender_user_id)::uuid,
    sqlc.narg(external_message_id)::text,
    sqlc.narg(source_reply_to_message_id)::text,
    sqlc.arg(role),
    sqlc.arg(content),
    sqlc.arg(metadata),
    sqlc.arg(usage),
    sqlc.arg(session_mode),
    sqlc.arg(runtime_type),
    sqlc.narg(model_id)::uuid,
    sqlc.narg(event_id)::uuid,
    sqlc.narg(display_text)::text,
    sqlc.narg(run_id)::uuid,
    sqlc.arg(turn_id),
    turn_slot.position,
    sqlc.arg(turn_message_seq),
    true
  FROM turn_slot
  RETURNING
    id,
    created_at
)
SELECT
  inserted_message.id,
  inserted_message.created_at
FROM inserted_message;

-- name: CreateMessageInHistoryTurnByRequest :one
WITH owner_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
  FOR UPDATE
),
target AS MATERIALIZED (
  SELECT
    turns.turn_id,
    turns.session_id,
    turns.turn_position,
    CASE
      WHEN sqlc.arg(role)::text = 'assistant' AND NOT EXISTS (
        SELECT 1
        FROM bot_history_messages assistant
        WHERE assistant.team_id = public.memoh_current_team_id() AND assistant.turn_id = turns.turn_id
          AND assistant.role = 'assistant'
          AND assistant.turn_message_seq = 2
          AND assistant.turn_visible = true
      ) THEN 2
      ELSE COALESCE((
        SELECT existing.turn_message_seq + 1
        FROM bot_history_messages existing
        WHERE existing.team_id = public.memoh_current_team_id() AND existing.turn_id = turns.turn_id
        ORDER BY existing.turn_message_seq DESC
        LIMIT 1
      ), 1)
    END AS turn_message_seq
  FROM bot_history_messages turns
  JOIN owner_session owner ON owner.id = turns.session_id
  WHERE turns.team_id = public.memoh_current_team_id()
    AND turns.session_id = sqlc.arg(session_id)
    AND turns.id = sqlc.arg(request_message_id)
    AND turns.turn_id IS NOT NULL
    AND turns.turn_position IS NOT NULL
    AND turns.turn_visible = true
  LIMIT 1
  FOR UPDATE OF turns
),
inserted AS (
  INSERT INTO bot_history_messages (
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
    event_id,
    display_text,
    run_id,
    turn_id,
    turn_position,
    turn_message_seq,
    turn_visible
  )
  SELECT
    sqlc.arg(bot_id),
    target.session_id,
    sqlc.narg(sender_channel_identity_id)::uuid,
    sqlc.narg(sender_user_id)::uuid,
    sqlc.narg(external_message_id)::text,
    sqlc.narg(source_reply_to_message_id)::text,
    sqlc.arg(role)::text,
    sqlc.arg(content),
    sqlc.arg(metadata),
    sqlc.arg(usage),
    sqlc.arg(session_mode),
    sqlc.arg(runtime_type),
    sqlc.narg(model_id)::uuid,
    sqlc.narg(event_id)::uuid,
    sqlc.narg(display_text)::text,
    sqlc.narg(run_id)::uuid,
    target.turn_id,
    target.turn_position,
    target.turn_message_seq,
    true
  FROM target
  RETURNING
    id,
    bot_id,
    session_id,
    sender_channel_identity_id,
    sender_account_user_id AS sender_user_id,
    source_message_id AS external_message_id,
    source_reply_to_message_id,
    role,
    content,
    metadata,
    usage,
    session_mode,
    runtime_type,
    event_id,
    display_text,
    created_at
)
SELECT
  inserted.id,
  inserted.bot_id,
  inserted.session_id,
  inserted.sender_channel_identity_id,
  inserted.sender_user_id,
  inserted.external_message_id,
  inserted.source_reply_to_message_id,
  inserted.role,
  inserted.content,
  inserted.metadata,
  inserted.usage,
  inserted.session_mode,
  inserted.runtime_type,
  inserted.event_id,
  inserted.display_text,
  inserted.created_at
FROM inserted;

-- name: CreateMessageInHistoryTurnByRequestAndBind :one
WITH owner_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
  FOR UPDATE
),
target AS MATERIALIZED (
  SELECT
    turns.turn_id,
    turns.session_id,
    turns.turn_position,
    CASE
      WHEN sqlc.arg(role)::text = 'assistant' AND NOT EXISTS (
        SELECT 1
        FROM bot_history_messages assistant
        WHERE assistant.team_id = public.memoh_current_team_id() AND assistant.turn_id = turns.turn_id
          AND assistant.role = 'assistant'
          AND assistant.turn_message_seq = 2
          AND assistant.turn_visible = true
      ) THEN 2
      ELSE COALESCE((
        SELECT existing.turn_message_seq + 1
        FROM bot_history_messages existing
        WHERE existing.team_id = public.memoh_current_team_id() AND existing.turn_id = turns.turn_id
        ORDER BY existing.turn_message_seq DESC
        LIMIT 1
      ), 1)
    END AS turn_message_seq
  FROM bot_history_messages turns
  JOIN owner_session owner ON owner.id = turns.session_id
  WHERE turns.team_id = public.memoh_current_team_id()
    AND turns.session_id = sqlc.arg(session_id)
    AND turns.id = sqlc.arg(request_message_id)
    AND turns.turn_id IS NOT NULL
    AND turns.turn_position IS NOT NULL
    AND turns.turn_visible = true
  LIMIT 1
  FOR UPDATE OF turns
),
inserted AS (
  INSERT INTO bot_history_messages (
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
    event_id,
    display_text,
    run_id,
    turn_id,
    turn_position,
    turn_message_seq,
    turn_visible
  )
  SELECT
    sqlc.arg(bot_id),
    target.session_id,
    sqlc.narg(sender_channel_identity_id)::uuid,
    sqlc.narg(sender_user_id)::uuid,
    sqlc.narg(external_message_id)::text,
    sqlc.narg(source_reply_to_message_id)::text,
    sqlc.arg(role)::text,
    sqlc.arg(content),
    sqlc.arg(metadata),
    sqlc.arg(usage),
    sqlc.arg(session_mode),
    sqlc.arg(runtime_type),
    sqlc.narg(model_id)::uuid,
    sqlc.narg(event_id)::uuid,
    sqlc.narg(display_text)::text,
    sqlc.narg(run_id)::uuid,
    target.turn_id,
    target.turn_position,
    target.turn_message_seq,
    true
  FROM target
  RETURNING
    id,
    role,
    created_at
)
SELECT
  inserted.id,
  inserted.created_at
FROM inserted
JOIN target ON true;

-- name: CreateToolTailRound :many
WITH input_rows(
  turn_message_seq,
  message_id,
  sender_channel_identity_id,
  sender_user_id,
  external_message_id,
  source_reply_to_message_id,
  role,
  content,
  metadata,
  usage,
  session_mode,
  runtime_type,
  model_id,
  event_id,
  display_text
) AS (
  VALUES
      (
        1::bigint,
        sqlc.arg(user_message_id)::uuid,
        sqlc.narg(user_sender_channel_identity_id)::uuid,
        sqlc.narg(user_sender_user_id)::uuid,
        sqlc.narg(user_external_message_id)::text,
        sqlc.narg(user_source_reply_to_message_id)::text,
        'user'::text,
        sqlc.arg(user_content)::jsonb,
        sqlc.arg(user_metadata)::jsonb,
        sqlc.arg(user_usage)::jsonb,
        sqlc.arg(user_session_mode)::text,
        sqlc.arg(user_runtime_type)::text,
        sqlc.narg(user_model_id)::uuid,
        sqlc.narg(user_event_id)::uuid,
        sqlc.narg(user_display_text)::text
      ),
      (
        2::bigint,
        sqlc.arg(tool_call_assistant_message_id)::uuid,
        sqlc.narg(tool_call_assistant_sender_channel_identity_id)::uuid,
        sqlc.narg(tool_call_assistant_sender_user_id)::uuid,
        sqlc.narg(tool_call_assistant_external_message_id)::text,
        sqlc.narg(tool_call_assistant_source_reply_to_message_id)::text,
        'assistant'::text,
        sqlc.arg(tool_call_assistant_content)::jsonb,
        sqlc.arg(tool_call_assistant_metadata)::jsonb,
        sqlc.arg(tool_call_assistant_usage)::jsonb,
        sqlc.arg(tool_call_assistant_session_mode)::text,
        sqlc.arg(tool_call_assistant_runtime_type)::text,
        sqlc.narg(tool_call_assistant_model_id)::uuid,
        sqlc.narg(tool_call_assistant_event_id)::uuid,
        sqlc.narg(tool_call_assistant_display_text)::text
      ),
      (
        3::bigint,
        sqlc.arg(tool_message_id)::uuid,
        sqlc.narg(tool_sender_channel_identity_id)::uuid,
        sqlc.narg(tool_sender_user_id)::uuid,
        sqlc.narg(tool_external_message_id)::text,
        sqlc.narg(tool_source_reply_to_message_id)::text,
        'tool'::text,
        sqlc.arg(tool_content)::jsonb,
        sqlc.arg(tool_metadata)::jsonb,
        sqlc.arg(tool_usage)::jsonb,
        sqlc.arg(tool_session_mode)::text,
        sqlc.arg(tool_runtime_type)::text,
        sqlc.narg(tool_model_id)::uuid,
        sqlc.narg(tool_event_id)::uuid,
        sqlc.narg(tool_display_text)::text
      ),
      (
        4::bigint,
        sqlc.arg(final_assistant_message_id)::uuid,
        sqlc.narg(final_assistant_sender_channel_identity_id)::uuid,
        sqlc.narg(final_assistant_sender_user_id)::uuid,
        sqlc.narg(final_assistant_external_message_id)::text,
        sqlc.narg(final_assistant_source_reply_to_message_id)::text,
        'assistant'::text,
        sqlc.arg(final_assistant_content)::jsonb,
        sqlc.arg(final_assistant_metadata)::jsonb,
        sqlc.arg(final_assistant_usage)::jsonb,
        sqlc.arg(final_assistant_session_mode)::text,
        sqlc.arg(final_assistant_runtime_type)::text,
        sqlc.narg(final_assistant_model_id)::uuid,
        sqlc.narg(final_assistant_event_id)::uuid,
        sqlc.narg(final_assistant_display_text)::text
      )
),
-- As in CreateMessageWithHistoryTurn: a run admitted through session_runs
-- already drew this turn's position, so bumping again would spend a second one
-- and file the round under a turn the client was never told about.
next_position AS (
  UPDATE bot_sessions
  SET next_turn_position = next_turn_position + 1
  WHERE bot_sessions.team_id = public.memoh_current_team_id()
    AND bot_sessions.id = sqlc.arg(session_id)
    AND sqlc.narg(turn_position)::bigint IS NULL
  RETURNING (next_turn_position - 1)::bigint AS position
),
turn_slot AS (
  SELECT sqlc.narg(turn_position)::bigint AS position
  WHERE sqlc.narg(turn_position)::bigint IS NOT NULL
  UNION ALL
  SELECT next_position.position FROM next_position
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
    event_id,
    display_text,
    run_id,
    turn_id,
    turn_position,
    turn_message_seq,
    turn_visible
  )
  SELECT
    input.message_id,
    sqlc.arg(bot_id),
    sqlc.arg(session_id),
    input.sender_channel_identity_id,
    input.sender_user_id,
    input.external_message_id,
    input.source_reply_to_message_id,
    input.role,
    input.content,
    input.metadata,
    input.usage,
    input.session_mode,
    input.runtime_type,
    input.model_id,
    input.event_id,
    input.display_text,
    sqlc.narg(run_id)::uuid,
    sqlc.arg(turn_id),
    turn_slot.position,
    input.turn_message_seq,
    true
  FROM input_rows input, turn_slot
  RETURNING id, created_at, turn_message_seq
)
SELECT inserted_messages.id, inserted_messages.created_at
FROM inserted_messages
ORDER BY inserted_messages.turn_message_seq;

-- name: CreateHistoryTurn :one
WITH next_position AS (
  UPDATE bot_sessions
  SET next_turn_position = next_turn_position + 1
  WHERE bot_sessions.team_id = public.memoh_current_team_id() AND bot_sessions.id = sqlc.arg(session_id)
  RETURNING next_turn_position - 1 AS position
),
input AS (
  SELECT
    gen_random_uuid() AS turn_id,
    sqlc.arg(bot_id)::uuid AS bot_id,
    sqlc.arg(session_id)::uuid AS session_id,
    next_position.position,
    sqlc.narg(request_message_id)::uuid AS request_message_id,
    sqlc.narg(assistant_message_id)::uuid AS assistant_message_id
  FROM next_position
),
linked_request AS (
  UPDATE bot_history_messages m
  SET turn_id = input.turn_id,
      turn_position = input.position,
      turn_message_seq = 1,
      turn_visible = true,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM input
  WHERE m.team_id = public.memoh_current_team_id() AND m.id = input.request_message_id
    AND m.session_id = input.session_id
    AND m.bot_id = input.bot_id
  RETURNING m.id, m.created_at
),
linked_assistant AS (
  UPDATE bot_history_messages m
  SET turn_id = input.turn_id,
      turn_position = input.position,
      turn_message_seq = 2,
      turn_visible = true,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM input
  WHERE m.team_id = public.memoh_current_team_id() AND m.id = input.assistant_message_id
    AND m.session_id = input.session_id
    AND m.bot_id = input.bot_id
  RETURNING m.id, m.created_at
),
linked_summary AS (
  SELECT COUNT(*) AS linked_count, MIN(created_at) AS created_at, MAX(created_at) AS updated_at
  FROM (
    SELECT id, created_at FROM linked_request
    UNION ALL
    SELECT id, created_at FROM linked_assistant
  ) linked
)
SELECT input.turn_id AS id, input.bot_id, input.session_id, input.position::bigint AS position,
  input.request_message_id, input.assistant_message_id,
  NULL::uuid AS superseded_by_turn_id,
  NULL::timestamptz AS superseded_at,
  NULL::text AS superseded_reason,
  linked_summary.created_at::timestamptz AS created_at,
  linked_summary.updated_at::timestamptz AS updated_at
FROM input
CROSS JOIN linked_summary
WHERE linked_summary.linked_count > 0;

-- name: CreateHistoryTurnWithID :one
WITH next_position AS (
  UPDATE bot_sessions
  SET next_turn_position = next_turn_position + 1
  WHERE bot_sessions.team_id = public.memoh_current_team_id() AND bot_sessions.id = sqlc.arg(session_id)
  RETURNING next_turn_position - 1 AS position
),
input AS (
  SELECT
    sqlc.arg(turn_id)::uuid AS turn_id,
    sqlc.arg(bot_id)::uuid AS bot_id,
    sqlc.arg(session_id)::uuid AS session_id,
    next_position.position,
    sqlc.narg(request_message_id)::uuid AS request_message_id,
    sqlc.narg(assistant_message_id)::uuid AS assistant_message_id
  FROM next_position
),
linked_request AS (
  UPDATE bot_history_messages m
  SET turn_id = input.turn_id,
      turn_position = input.position,
      turn_message_seq = 1,
      turn_visible = true,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM input
  WHERE m.team_id = public.memoh_current_team_id() AND m.id = input.request_message_id
    AND m.session_id = input.session_id
    AND m.bot_id = input.bot_id
  RETURNING m.id, m.created_at
),
linked_assistant AS (
  UPDATE bot_history_messages m
  SET turn_id = input.turn_id,
      turn_position = input.position,
      turn_message_seq = 2,
      turn_visible = true,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM input
  WHERE m.team_id = public.memoh_current_team_id() AND m.id = input.assistant_message_id
    AND m.session_id = input.session_id
    AND m.bot_id = input.bot_id
  RETURNING m.id, m.created_at
),
linked_summary AS (
  SELECT COUNT(*) AS linked_count, MIN(created_at) AS created_at, MAX(created_at) AS updated_at
  FROM (
    SELECT id, created_at FROM linked_request
    UNION ALL
    SELECT id, created_at FROM linked_assistant
  ) linked
)
SELECT input.turn_id AS id, input.bot_id, input.session_id, input.position::bigint AS position,
  input.request_message_id, input.assistant_message_id,
  NULL::uuid AS superseded_by_turn_id,
  NULL::timestamptz AS superseded_at,
  NULL::text AS superseded_reason,
  linked_summary.created_at::timestamptz AS created_at,
  linked_summary.updated_at::timestamptz AS updated_at
FROM input
CROSS JOIN linked_summary
WHERE linked_summary.linked_count > 0;

-- name: CreateHistoryTurnWithIDAtPosition :one
WITH input AS (
  SELECT
    sqlc.arg(turn_id)::uuid AS turn_id,
    sqlc.arg(bot_id)::uuid AS bot_id,
    sqlc.arg(session_id)::uuid AS session_id,
    sqlc.arg(turn_position)::bigint AS position,
    sqlc.narg(request_message_id)::uuid AS request_message_id,
    sqlc.narg(assistant_message_id)::uuid AS assistant_message_id
),
linked_request AS (
  UPDATE bot_history_messages m
  SET turn_id = input.turn_id,
      turn_position = input.position,
      turn_message_seq = 1,
      turn_visible = true,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM input
  WHERE m.team_id = public.memoh_current_team_id() AND m.id = input.request_message_id
    AND m.session_id = input.session_id
    AND m.bot_id = input.bot_id
  RETURNING m.id, m.created_at
),
linked_assistant AS (
  UPDATE bot_history_messages m
  SET turn_id = input.turn_id,
      turn_position = input.position,
      turn_message_seq = 2,
      turn_visible = true,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM input
  WHERE m.team_id = public.memoh_current_team_id() AND m.id = input.assistant_message_id
    AND m.session_id = input.session_id
    AND m.bot_id = input.bot_id
  RETURNING m.id, m.created_at
),
linked_summary AS (
  SELECT COUNT(*) AS linked_count, MIN(created_at) AS created_at, MAX(created_at) AS updated_at
  FROM (
    SELECT id, created_at FROM linked_request
    UNION ALL
    SELECT id, created_at FROM linked_assistant
  ) linked
)
SELECT input.turn_id AS id, input.bot_id, input.session_id, input.position::bigint AS position,
  input.request_message_id, input.assistant_message_id,
  NULL::uuid AS superseded_by_turn_id,
  NULL::timestamptz AS superseded_at,
  NULL::text AS superseded_reason,
  linked_summary.created_at::timestamptz AS created_at,
  linked_summary.updated_at::timestamptz AS updated_at
FROM input
CROSS JOIN linked_summary
WHERE linked_summary.linked_count > 0;

-- name: BindHistoryTurnAssistantByRequest :one
WITH target AS (
  SELECT
    request.turn_id,
    request.bot_id,
    request.session_id,
    request.turn_position,
    request.id AS request_message_id,
    request.created_at AS request_created_at
  FROM bot_history_messages request
  WHERE request.team_id = public.memoh_current_team_id() AND request.session_id = sqlc.arg(session_id)
    AND request.id = sqlc.arg(request_message_id)
    AND request.turn_id IS NOT NULL
    AND request.turn_visible = true
    AND NOT EXISTS (
      SELECT 1
      FROM bot_history_messages assistant
      WHERE assistant.team_id = public.memoh_current_team_id() AND assistant.turn_id = request.turn_id
        AND assistant.role = 'assistant'
        AND assistant.turn_message_seq = 2
        AND assistant.turn_visible = true
    )
  LIMIT 1
  FOR UPDATE
),
linked AS (
  UPDATE bot_history_messages assistant
  SET turn_id = target.turn_id,
      turn_position = target.turn_position,
      turn_message_seq = 2,
      turn_visible = true,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM target
  WHERE assistant.team_id = public.memoh_current_team_id() AND assistant.id = sqlc.arg(assistant_message_id)
    AND assistant.session_id = target.session_id
  RETURNING assistant.id AS assistant_message_id, assistant.created_at AS assistant_created_at
)
SELECT target.turn_id AS id, target.bot_id, target.session_id, target.turn_position::bigint AS position,
  target.request_message_id, linked.assistant_message_id,
  NULL::uuid AS superseded_by_turn_id,
  NULL::timestamptz AS superseded_at,
  NULL::text AS superseded_reason,
  LEAST(target.request_created_at, linked.assistant_created_at)::timestamptz AS created_at,
  GREATEST(target.request_created_at, linked.assistant_created_at)::timestamptz AS updated_at
FROM target
JOIN linked ON true;

-- name: BindLatestHistoryTurnAssistant :one
WITH target AS (
  SELECT
    request.turn_id,
    request.bot_id,
    request.session_id,
    request.turn_position,
    request.id AS request_message_id,
    request.created_at AS request_created_at
  FROM bot_history_messages request
  WHERE request.team_id = public.memoh_current_team_id() AND request.session_id = sqlc.arg(session_id)
    AND request.role = 'user'
    AND request.turn_message_seq = 1
    AND request.turn_id IS NOT NULL
    AND request.turn_visible = true
    AND NOT EXISTS (
      SELECT 1
      FROM bot_history_messages assistant
      WHERE assistant.team_id = public.memoh_current_team_id() AND assistant.turn_id = request.turn_id
        AND assistant.role = 'assistant'
        AND assistant.turn_message_seq = 2
        AND assistant.turn_visible = true
    )
  ORDER BY request.turn_position DESC
  LIMIT 1
  FOR UPDATE
),
linked AS (
  UPDATE bot_history_messages assistant
  SET turn_id = target.turn_id,
      turn_position = target.turn_position,
      turn_message_seq = 2,
      turn_visible = true,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM target
  WHERE assistant.team_id = public.memoh_current_team_id() AND assistant.id = sqlc.arg(assistant_message_id)
    AND assistant.session_id = target.session_id
  RETURNING assistant.id AS assistant_message_id, assistant.created_at AS assistant_created_at
)
SELECT target.turn_id AS id, target.bot_id, target.session_id, target.turn_position::bigint AS position,
  target.request_message_id, linked.assistant_message_id,
  NULL::uuid AS superseded_by_turn_id,
  NULL::timestamptz AS superseded_at,
  NULL::text AS superseded_reason,
  LEAST(target.request_created_at, linked.assistant_created_at)::timestamptz AS created_at,
  GREATEST(target.request_created_at, linked.assistant_created_at)::timestamptz AS updated_at
FROM target
JOIN linked ON true;

-- name: LinkMessageToHistoryTurn :one
WITH target AS (
  SELECT
    existing.turn_id,
    existing.session_id,
    existing.turn_position,
    bool_or(existing.turn_visible) AS turn_visible
  FROM bot_history_messages existing
  WHERE existing.team_id = public.memoh_current_team_id() AND existing.turn_id = sqlc.arg(turn_id)
    AND existing.turn_position IS NOT NULL
  GROUP BY existing.turn_id, existing.session_id, existing.turn_position
  LIMIT 1
)
UPDATE bot_history_messages m
SET turn_id = target.turn_id,
    turn_position = target.turn_position,
    turn_message_seq = sqlc.arg(turn_message_seq),
    turn_visible = target.turn_visible,
    turn_superseded_by_turn_id = NULL,
    turn_superseded_at = NULL,
    turn_superseded_reason = NULL
FROM target
WHERE m.team_id = public.memoh_current_team_id() AND m.id = sqlc.arg(message_id)
  AND m.session_id = target.session_id
RETURNING m.id;

-- name: LockHistoryTurnAppendByRequest :exec
SELECT pg_advisory_xact_lock(hashtextextended(m.turn_id::text, 0))
FROM bot_history_messages m
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.id = sqlc.arg(request_message_id)
  AND m.turn_id IS NOT NULL
LIMIT 1;

-- name: AppendMessageToHistoryTurnByRequest :one
WITH target AS (
  SELECT request.turn_id, request.session_id, request.turn_position
  FROM bot_history_messages request
  WHERE request.team_id = public.memoh_current_team_id() AND request.session_id = sqlc.arg(session_id)
    AND request.id = sqlc.arg(request_message_id)
    AND request.turn_id IS NOT NULL
    AND request.turn_position IS NOT NULL
    AND request.turn_visible = true
  LIMIT 1
  FOR UPDATE
),
next_seq AS (
  SELECT
    target.turn_id,
    target.session_id,
    target.turn_position,
    COALESCE(MAX(existing.turn_message_seq), 0) + 1 AS turn_message_seq
  FROM target
  JOIN bot_history_messages existing ON existing.turn_id = target.turn_id AND existing.team_id = public.memoh_current_team_id()
  GROUP BY target.turn_id, target.session_id, target.turn_position
)
UPDATE bot_history_messages m
SET turn_id = next_seq.turn_id,
    turn_position = next_seq.turn_position,
    turn_visible = true,
    turn_message_seq = next_seq.turn_message_seq,
    turn_superseded_by_turn_id = NULL,
    turn_superseded_at = NULL,
    turn_superseded_reason = NULL
FROM next_seq
WHERE m.team_id = public.memoh_current_team_id() AND m.id = sqlc.arg(message_id)
  AND m.session_id = next_seq.session_id
  AND m.turn_id IS NULL
RETURNING m.id;

-- name: AppendMessageToLatestHistoryTurn :one
WITH latest AS (
  SELECT visible.turn_id, visible.session_id, visible.turn_position
  FROM bot_history_messages visible
  WHERE visible.team_id = public.memoh_current_team_id() AND visible.session_id = sqlc.arg(session_id)
    AND visible.turn_id IS NOT NULL
    AND visible.turn_position IS NOT NULL
    AND visible.turn_message_seq IS NOT NULL
    AND visible.turn_visible = true
  ORDER BY visible.turn_position DESC, visible.turn_message_seq DESC
  LIMIT 1
  FOR UPDATE
),
next_seq AS (
  SELECT
    latest.turn_id,
    latest.session_id,
    latest.turn_position,
    COALESCE(MAX(existing.turn_message_seq), 0) + 1 AS turn_message_seq
  FROM latest
  JOIN bot_history_messages existing ON existing.turn_id = latest.turn_id AND existing.team_id = public.memoh_current_team_id()
  GROUP BY latest.turn_id, latest.session_id, latest.turn_position
)
UPDATE bot_history_messages m
SET turn_id = next_seq.turn_id,
    turn_position = next_seq.turn_position,
    turn_visible = true,
    turn_message_seq = next_seq.turn_message_seq,
    turn_superseded_by_turn_id = NULL,
    turn_superseded_at = NULL,
    turn_superseded_reason = NULL
FROM next_seq
WHERE m.team_id = public.memoh_current_team_id() AND m.id = sqlc.arg(message_id)
  AND m.session_id = next_seq.session_id
  AND m.turn_id IS NULL
RETURNING m.id;

-- name: LinkUnassignedMessagesAfterHistoryTurnAssistant :exec
WITH target AS (
  SELECT
    assistant.turn_id,
    assistant.session_id,
    assistant.turn_position,
    assistant.created_at AS assistant_created_at,
    assistant.id AS assistant_id
  FROM bot_history_messages assistant
  WHERE assistant.team_id = public.memoh_current_team_id() AND assistant.turn_id = sqlc.arg(turn_id)
    AND assistant.role = 'assistant'
    AND assistant.turn_message_seq = 2
    AND assistant.turn_visible = true
  LIMIT 1
),
tail AS (
  SELECT
    m.id AS message_id,
    target.turn_id,
    target.turn_position,
    2 + ROW_NUMBER() OVER (ORDER BY m.created_at, m.id) AS turn_message_seq
  FROM target
  JOIN bot_history_messages m
    ON m.session_id = target.session_id
   AND m.role IN ('assistant', 'tool')
  WHERE m.team_id = public.memoh_current_team_id() AND m.id <> target.assistant_id
    AND m.turn_id IS NULL
    AND (m.created_at, m.id) > (target.assistant_created_at, target.assistant_id)
)
UPDATE bot_history_messages m
SET turn_id = tail.turn_id,
    turn_position = tail.turn_position,
    turn_visible = true,
    turn_message_seq = tail.turn_message_seq
FROM tail
WHERE m.team_id = public.memoh_current_team_id() AND m.id = tail.message_id;

-- name: GetVisibleHistoryTurnByMessage :one
WITH target AS (
  SELECT m.turn_id, m.session_id, m.turn_position
  FROM bot_history_messages m
  WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
    AND m.id = sqlc.arg(message_id)
    AND m.turn_id IS NOT NULL
    AND m.turn_position IS NOT NULL
    AND m.turn_visible = true
  LIMIT 1
)
SELECT
  target.turn_id AS id,
  ((ARRAY_AGG(m.bot_id ORDER BY m.turn_message_seq ASC, m.created_at ASC, m.id ASC))[1])::uuid AS bot_id,
  target.session_id,
  target.turn_position::bigint AS position,
  ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'user' AND m.turn_message_seq = 1))[1])::uuid AS request_message_id,
  ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'assistant' AND m.turn_message_seq = 2))[1])::uuid AS assistant_message_id,
  ((ARRAY_AGG(m.turn_superseded_by_turn_id ORDER BY m.turn_superseded_at DESC NULLS LAST, m.created_at DESC, m.id DESC) FILTER (WHERE m.turn_superseded_by_turn_id IS NOT NULL))[1])::uuid AS superseded_by_turn_id,
  MAX(m.turn_superseded_at)::timestamptz AS superseded_at,
  COALESCE(((ARRAY_AGG(m.turn_superseded_reason ORDER BY m.turn_superseded_at DESC NULLS LAST, m.created_at DESC, m.id DESC) FILTER (WHERE m.turn_superseded_reason IS NOT NULL))[1])::text, ''::text)::text AS superseded_reason,
  MIN(m.created_at)::timestamptz AS created_at,
  COALESCE(MAX(m.turn_superseded_at), MAX(m.created_at))::timestamptz AS updated_at
FROM target
JOIN bot_history_messages m
  ON m.team_id = public.memoh_current_team_id()
 AND m.turn_id = target.turn_id
 AND m.session_id = target.session_id
GROUP BY target.turn_id, target.session_id, target.turn_position;

-- name: GetLatestVisibleHistoryTurnBySession :one
WITH target AS (
  SELECT m.turn_id, m.session_id, m.turn_position
  FROM bot_history_messages m
  WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
    AND m.turn_id IS NOT NULL
    AND m.turn_position IS NOT NULL
    AND m.turn_visible = true
  ORDER BY m.turn_position DESC, m.turn_message_seq DESC
  LIMIT 1
)
SELECT
  target.turn_id AS id,
  ((ARRAY_AGG(m.bot_id ORDER BY m.turn_message_seq ASC, m.created_at ASC, m.id ASC))[1])::uuid AS bot_id,
  target.session_id,
  target.turn_position::bigint AS position,
  ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'user' AND m.turn_message_seq = 1))[1])::uuid AS request_message_id,
  ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'assistant' AND m.turn_message_seq = 2))[1])::uuid AS assistant_message_id,
  ((ARRAY_AGG(m.turn_superseded_by_turn_id ORDER BY m.turn_superseded_at DESC NULLS LAST, m.created_at DESC, m.id DESC) FILTER (WHERE m.turn_superseded_by_turn_id IS NOT NULL))[1])::uuid AS superseded_by_turn_id,
  MAX(m.turn_superseded_at)::timestamptz AS superseded_at,
  COALESCE(((ARRAY_AGG(m.turn_superseded_reason ORDER BY m.turn_superseded_at DESC NULLS LAST, m.created_at DESC, m.id DESC) FILTER (WHERE m.turn_superseded_reason IS NOT NULL))[1])::text, ''::text)::text AS superseded_reason,
  MIN(m.created_at)::timestamptz AS created_at,
  COALESCE(MAX(m.turn_superseded_at), MAX(m.created_at))::timestamptz AS updated_at
FROM target
JOIN bot_history_messages m
  ON m.team_id = public.memoh_current_team_id()
 AND m.turn_id = target.turn_id
 AND m.session_id = target.session_id
GROUP BY target.turn_id, target.session_id, target.turn_position;

-- name: GetHistoryTurnByID :one
WITH target AS (
  SELECT m.turn_id, m.session_id, m.turn_position
  FROM bot_history_messages m
  WHERE m.team_id = public.memoh_current_team_id() AND m.turn_id = sqlc.arg(old_turn_id)
    AND m.session_id = sqlc.arg(session_id)
    AND m.turn_position IS NOT NULL
    AND m.turn_visible = true
  LIMIT 1
)
SELECT
  target.turn_id AS id,
  ((ARRAY_AGG(m.bot_id ORDER BY m.turn_message_seq ASC, m.created_at ASC, m.id ASC))[1])::uuid AS bot_id,
  target.session_id,
  target.turn_position::bigint AS position,
  ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'user' AND m.turn_message_seq = 1))[1])::uuid AS request_message_id,
  ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'assistant' AND m.turn_message_seq = 2))[1])::uuid AS assistant_message_id,
  ((ARRAY_AGG(m.turn_superseded_by_turn_id ORDER BY m.turn_superseded_at DESC NULLS LAST, m.created_at DESC, m.id DESC) FILTER (WHERE m.turn_superseded_by_turn_id IS NOT NULL))[1])::uuid AS superseded_by_turn_id,
  MAX(m.turn_superseded_at)::timestamptz AS superseded_at,
  COALESCE(((ARRAY_AGG(m.turn_superseded_reason ORDER BY m.turn_superseded_at DESC NULLS LAST, m.created_at DESC, m.id DESC) FILTER (WHERE m.turn_superseded_reason IS NOT NULL))[1])::text, ''::text)::text AS superseded_reason,
  MIN(m.created_at)::timestamptz AS created_at,
  COALESCE(MAX(m.turn_superseded_at), MAX(m.created_at))::timestamptz AS updated_at
FROM target
JOIN bot_history_messages m
  ON m.team_id = public.memoh_current_team_id()
 AND m.turn_id = target.turn_id
 AND m.session_id = target.session_id
GROUP BY target.turn_id, target.session_id, target.turn_position;

-- name: ReplaceHistoryTurn :one
WITH owner_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
  FOR UPDATE
),
old_turn_target AS (
  SELECT m.turn_id, m.session_id, m.turn_position
  FROM bot_history_messages m
  JOIN owner_session owner ON owner.id = m.session_id
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.turn_id = sqlc.arg(old_turn_id)
    AND m.turn_position IS NOT NULL
    AND m.turn_visible = true
  LIMIT 1
),
old_turn AS (
  SELECT
    old_turn_target.turn_id AS id,
    ((ARRAY_AGG(m.bot_id ORDER BY m.turn_message_seq ASC, m.created_at ASC, m.id ASC))[1])::uuid AS bot_id,
    old_turn_target.session_id,
    old_turn_target.turn_position::bigint AS position,
    ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'user' AND m.turn_message_seq = 1))[1])::uuid AS request_message_id,
    ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'assistant' AND m.turn_message_seq = 2))[1])::uuid AS assistant_message_id,
    NULL::uuid AS superseded_by_turn_id,
    NULL::timestamptz AS superseded_at,
    NULL::text AS superseded_reason,
    MIN(m.created_at)::timestamptz AS created_at,
    MAX(m.created_at)::timestamptz AS updated_at
  FROM old_turn_target
  JOIN bot_history_messages m
    ON m.team_id = public.memoh_current_team_id()
   AND m.turn_id = old_turn_target.turn_id
   AND m.session_id = old_turn_target.session_id
  GROUP BY old_turn_target.turn_id, old_turn_target.session_id, old_turn_target.turn_position
),
old_lock AS (
  SELECT m.id, m.bot_id, m.session_id, m.compact_id
  FROM bot_history_messages m
  JOIN old_turn ON old_turn.id = m.turn_id
  WHERE m.team_id = public.memoh_current_team_id()
  FOR UPDATE
),
old_lock_guard AS (
  SELECT COUNT(*) AS count FROM old_lock
),
latest_turn AS (
  SELECT m.turn_id AS id
  FROM bot_history_messages m
  WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
    AND m.turn_id IS NOT NULL
    AND m.turn_position IS NOT NULL
    AND m.turn_visible = true
  ORDER BY m.turn_position DESC, m.turn_message_seq DESC
  LIMIT 1
),
replacement_input AS (
  SELECT
    old_turn.*,
    sqlc.narg(request_message_id)::uuid AS replacement_request_message_id,
    sqlc.narg(assistant_message_id)::uuid AS replacement_assistant_message_id
  FROM old_turn
  JOIN latest_turn ON latest_turn.id = old_turn.id
  CROSS JOIN old_lock_guard
  WHERE (
      sqlc.narg(request_message_id)::uuid IS NULL
      OR EXISTS (
        SELECT 1
        FROM bot_history_messages request_message
        WHERE request_message.team_id = public.memoh_current_team_id() AND request_message.id = sqlc.narg(request_message_id)::uuid
          AND request_message.session_id = old_turn.session_id
          AND request_message.role = 'user'
          AND (
            request_message.turn_id IS NULL
            OR request_message.turn_id = old_turn.id
            OR request_message.turn_id = sqlc.arg(replacement_turn_id)::uuid
          )
        FOR UPDATE
      )
    )
    AND (
      sqlc.narg(assistant_message_id)::uuid IS NULL
      OR EXISTS (
        SELECT 1
        FROM bot_history_messages assistant_message
        WHERE assistant_message.team_id = public.memoh_current_team_id() AND assistant_message.id = sqlc.narg(assistant_message_id)::uuid
          AND assistant_message.session_id = old_turn.session_id
          AND assistant_message.role = 'assistant'
          AND assistant_message.turn_id IS NULL
        FOR UPDATE
      )
    )
),
affected_compaction_sessions AS MATERIALIZED (
  SELECT DISTINCT locked.session_id
  FROM old_lock locked
  JOIN replacement_input ON replacement_input.session_id = locked.session_id
  JOIN bot_history_message_compacts compact
    ON compact.id = locked.compact_id
   AND compact.team_id = public.memoh_current_team_id()
  JOIN bot_sessions session
    ON session.id = locked.session_id
   AND session.team_id = public.memoh_current_team_id()
  WHERE locked.id IS DISTINCT FROM replacement_input.replacement_request_message_id
    AND locked.id IS DISTINCT FROM replacement_input.replacement_assistant_message_id
    AND compact.bot_id = locked.bot_id
    AND compact.session_id = locked.session_id
    AND compact.compaction_epoch = session.compaction_epoch
),
session_update AS (
  UPDATE bot_sessions s
  SET compaction_epoch = compaction_epoch + CASE
        WHEN EXISTS (
          SELECT 1
          FROM affected_compaction_sessions affected
          WHERE affected.session_id = s.id
        ) THEN 1
        ELSE 0
      END
  FROM replacement_input
  WHERE s.team_id = public.memoh_current_team_id() AND s.id = replacement_input.session_id
  RETURNING s.id
),
replacement AS (
  SELECT
    sqlc.arg(replacement_turn_id)::uuid AS id,
    replacement_input.bot_id,
    replacement_input.session_id,
    sqlc.arg(replacement_position)::bigint AS position,
    replacement_input.replacement_request_message_id AS request_message_id,
    replacement_input.replacement_assistant_message_id AS assistant_message_id,
    NULL::uuid AS superseded_by_turn_id,
    NULL::timestamptz AS superseded_at,
    NULL::text AS superseded_reason,
    now()::timestamptz AS created_at,
    now()::timestamptz AS updated_at
  FROM replacement_input
  CROSS JOIN session_update
  WHERE sqlc.arg(replacement_position)::bigint > 0
),
updated AS (
  UPDATE bot_history_messages old
  SET turn_superseded_by_turn_id = replacement.id,
      turn_superseded_at = sqlc.arg(superseded_at),
      turn_superseded_reason = sqlc.arg(superseded_reason),
      turn_visible = false
  FROM replacement
  WHERE old.team_id = public.memoh_current_team_id() AND old.turn_id = sqlc.arg(old_turn_id)
    AND old.session_id = sqlc.arg(session_id)
    AND old.turn_superseded_at IS NULL
    AND old.id IS DISTINCT FROM replacement.request_message_id
    AND old.id IS DISTINCT FROM replacement.assistant_message_id
  RETURNING old.turn_id, old.session_id, old.compact_id
),
updated_turn AS (
  SELECT DISTINCT turn_id AS id FROM updated
),
hidden_old_messages AS (
  UPDATE bot_history_messages m
  SET turn_visible = false
  FROM updated_turn, replacement
  WHERE m.team_id = public.memoh_current_team_id() AND m.turn_id = updated_turn.id
    AND m.id IS DISTINCT FROM replacement.request_message_id
    AND m.id IS DISTINCT FROM replacement.assistant_message_id
  RETURNING m.id
),
hidden_done AS (
  SELECT COUNT(*) AS count FROM hidden_old_messages
),
linked_anchors AS (
  UPDATE bot_history_messages m
  SET turn_id = replacement.id,
      turn_position = replacement.position,
      turn_message_seq = CASE
        WHEN m.id = replacement.request_message_id THEN 1
        WHEN m.id = replacement.assistant_message_id THEN 2
        ELSE m.turn_message_seq
      END,
      turn_visible = true,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM replacement
  CROSS JOIN hidden_done
  JOIN updated_turn ON true
  WHERE m.team_id = public.memoh_current_team_id()
    AND (
      (
        m.id = replacement.request_message_id
        AND m.role = 'user'
        AND (
          m.turn_id IS NULL
          OR m.turn_id = updated_turn.id
        )
      )
      OR (
        m.id = replacement.assistant_message_id
        AND m.role = 'assistant'
        AND m.turn_id IS NULL
      )
    )
  RETURNING m.id
),
linked_tail AS (
  UPDATE bot_history_messages m
  SET turn_id = tail.turn_id,
      turn_position = tail.turn_position,
      turn_visible = true,
      turn_message_seq = tail.turn_message_seq,
      turn_superseded_by_turn_id = NULL,
      turn_superseded_at = NULL,
      turn_superseded_reason = NULL
  FROM (
    SELECT
      m.id AS message_id,
      replacement.id AS turn_id,
      replacement.position AS turn_position,
      2 + ROW_NUMBER() OVER (ORDER BY m.created_at, m.id) AS turn_message_seq
    FROM replacement
    JOIN bot_history_messages assistant ON assistant.id = replacement.assistant_message_id AND assistant.team_id = public.memoh_current_team_id()
    JOIN bot_history_messages m
      ON m.session_id = replacement.session_id
     AND m.role IN ('assistant', 'tool')
    WHERE m.team_id = public.memoh_current_team_id() AND m.id <> replacement.assistant_message_id
      AND m.turn_id IS NULL
      AND (m.created_at, m.id) > (assistant.created_at, assistant.id)
  ) tail
  WHERE m.team_id = public.memoh_current_team_id() AND m.id = tail.message_id
  RETURNING m.id
),
linked_anchors_done AS (
  SELECT COUNT(*) AS count FROM linked_anchors
),
linked_tail_done AS (
  SELECT COUNT(*) AS count FROM linked_tail
)
SELECT replacement.id, replacement.bot_id, replacement.session_id, replacement.position::bigint AS position,
  replacement.request_message_id, replacement.assistant_message_id,
  replacement.superseded_by_turn_id, replacement.superseded_at,
  replacement.superseded_reason, replacement.created_at::timestamptz AS created_at, replacement.updated_at::timestamptz AS updated_at
FROM replacement
JOIN updated_turn ON true
CROSS JOIN linked_anchors_done
CROSS JOIN linked_tail_done;

-- name: SupersedeHistoryTurn :one
WITH owner_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
  FOR UPDATE
),
updated AS (
  UPDATE bot_history_messages m
  SET turn_superseded_by_turn_id = sqlc.arg(superseded_by_turn_id),
      turn_superseded_at = sqlc.arg(superseded_at),
      turn_superseded_reason = sqlc.arg(superseded_reason),
      turn_visible = false
  FROM owner_session owner
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.turn_id = sqlc.arg(old_turn_id)
    AND m.session_id = owner.id
    AND m.turn_superseded_at IS NULL
  RETURNING
    m.turn_id,
    m.bot_id,
    m.session_id,
    m.turn_position,
    m.id,
    m.role,
    m.turn_message_seq,
    m.turn_superseded_by_turn_id,
    m.turn_superseded_at,
    m.turn_superseded_reason,
    m.compact_id,
    m.created_at
),
compaction_epoch_bump AS (
  UPDATE bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  WHERE session.team_id = public.memoh_current_team_id()
    AND EXISTS (
    SELECT 1
    FROM updated changed
    JOIN bot_history_message_compacts compact
      ON compact.id = changed.compact_id
     AND compact.team_id = public.memoh_current_team_id()
    WHERE changed.session_id = session.id
      AND compact.bot_id = changed.bot_id
      AND compact.session_id = session.id
      AND compact.compaction_epoch = session.compaction_epoch
  )
  RETURNING session.id
),
compaction_epoch_done AS (
  SELECT COUNT(*) AS count FROM compaction_epoch_bump
)
SELECT
  updated.turn_id AS id,
  ((ARRAY_AGG(updated.bot_id ORDER BY updated.turn_message_seq ASC, updated.created_at ASC, updated.id ASC))[1])::uuid AS bot_id,
  updated.session_id,
  updated.turn_position::bigint AS position,
  ((ARRAY_AGG(updated.id ORDER BY updated.created_at ASC, updated.id ASC) FILTER (WHERE updated.role = 'user' AND updated.turn_message_seq = 1))[1])::uuid AS request_message_id,
  ((ARRAY_AGG(updated.id ORDER BY updated.created_at ASC, updated.id ASC) FILTER (WHERE updated.role = 'assistant' AND updated.turn_message_seq = 2))[1])::uuid AS assistant_message_id,
  ((ARRAY_AGG(updated.turn_superseded_by_turn_id ORDER BY updated.turn_superseded_at DESC NULLS LAST, updated.created_at DESC, updated.id DESC) FILTER (WHERE updated.turn_superseded_by_turn_id IS NOT NULL))[1])::uuid AS superseded_by_turn_id,
  MAX(updated.turn_superseded_at)::timestamptz AS superseded_at,
  COALESCE(((ARRAY_AGG(updated.turn_superseded_reason ORDER BY updated.turn_superseded_at DESC NULLS LAST, updated.created_at DESC, updated.id DESC) FILTER (WHERE updated.turn_superseded_reason IS NOT NULL))[1])::text, ''::text)::text AS superseded_reason,
  MIN(updated.created_at)::timestamptz AS created_at,
  COALESCE(MAX(updated.turn_superseded_at), MAX(updated.created_at))::timestamptz AS updated_at
FROM updated
CROSS JOIN compaction_epoch_done
GROUP BY updated.turn_id, updated.session_id, updated.turn_position;

-- name: HideMessagesByHistoryTurn :exec
WITH session_locator AS MATERIALIZED (
  SELECT DISTINCT message.session_id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.turn_id = sqlc.arg(turn_id)
    AND message.turn_visible = true
    AND message.session_id IS NOT NULL
),
owner_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  JOIN session_locator locator ON locator.session_id = session.id
  WHERE session.team_id = public.memoh_current_team_id()
  ORDER BY session.id
  FOR UPDATE
),
hidden AS (
  UPDATE bot_history_messages
  SET turn_visible = false
  WHERE team_id = public.memoh_current_team_id()
    AND turn_id = sqlc.arg(turn_id)
    AND turn_visible = true
    AND (SELECT count(*) FROM owner_sessions) >= 0
  RETURNING session_id, compact_id
),
compaction_epoch_bump AS (
  UPDATE bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  WHERE session.team_id = public.memoh_current_team_id()
    AND EXISTS (
    SELECT 1
    FROM hidden changed
    JOIN bot_history_message_compacts compact
      ON compact.id = changed.compact_id
     AND compact.team_id = public.memoh_current_team_id()
    WHERE changed.session_id = session.id
      AND compact.bot_id = session.bot_id
      AND compact.session_id = session.id
      AND compact.compaction_epoch = session.compaction_epoch
  )
  RETURNING session.id
)
SELECT (SELECT count(*) FROM hidden) + (SELECT count(*) * 0 FROM compaction_epoch_bump);

-- name: ListMessages :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.bot_id = sqlc.arg(bot_id)
ORDER BY m.created_at ASC, m.id ASC
LIMIT 10000;

-- name: ListAllMessagesForBackup :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  m.turn_id,
  m.turn_position,
  m.turn_message_seq,
  m.turn_visible,
  m.turn_superseded_by_turn_id,
  m.turn_superseded_at,
  m.turn_superseded_reason,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.bot_id = sqlc.arg(bot_id)
ORDER BY m.created_at ASC, m.id ASC;

-- name: ListHistoryTurnsByBot :many
SELECT
  m.turn_id AS id,
  ((ARRAY_AGG(m.bot_id ORDER BY m.turn_message_seq ASC, m.created_at ASC, m.id ASC))[1])::uuid AS bot_id,
  m.session_id,
  m.turn_position::bigint AS position,
  ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'user' AND m.turn_message_seq = 1))[1])::uuid AS request_message_id,
  ((ARRAY_AGG(m.id ORDER BY m.created_at ASC, m.id ASC) FILTER (WHERE m.role = 'assistant' AND m.turn_message_seq = 2))[1])::uuid AS assistant_message_id,
  ((ARRAY_AGG(m.turn_superseded_by_turn_id ORDER BY m.turn_superseded_at DESC NULLS LAST, m.created_at DESC, m.id DESC) FILTER (WHERE m.turn_superseded_by_turn_id IS NOT NULL))[1])::uuid AS superseded_by_turn_id,
  MAX(m.turn_superseded_at)::timestamptz AS superseded_at,
  COALESCE(((ARRAY_AGG(m.turn_superseded_reason ORDER BY m.turn_superseded_at DESC NULLS LAST, m.created_at DESC, m.id DESC) FILTER (WHERE m.turn_superseded_reason IS NOT NULL))[1])::text, ''::text)::text AS superseded_reason,
  MIN(m.created_at)::timestamptz AS created_at,
  COALESCE(MAX(m.turn_superseded_at), MAX(m.created_at))::timestamptz AS updated_at
FROM bot_history_messages m
WHERE m.team_id = public.memoh_current_team_id() AND m.bot_id = sqlc.arg(bot_id)
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.session_id IS NOT NULL
GROUP BY m.turn_id, m.session_id, m.turn_position
ORDER BY m.session_id ASC, m.turn_position ASC, m.turn_id ASC;

-- name: ListMessagesBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC
LIMIT 10000;

-- name: ListMessagesSince :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.bot_id = sqlc.arg(bot_id)
  AND m.created_at >= sqlc.arg(created_at)
ORDER BY m.created_at ASC, m.id ASC;

-- name: ListMessagesSinceBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND m.created_at >= sqlc.arg(created_at)
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: ListActiveMessagesSince :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.compact_id,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.bot_id = sqlc.arg(bot_id)
  AND m.created_at >= sqlc.arg(created_at)
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
ORDER BY m.created_at ASC, m.id ASC;

-- name: ListActiveMessagesSinceBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.compact_id,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND m.created_at >= sqlc.arg(created_at)
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: MeasureActiveMessagesBySession :one
-- Metadata-only context admission measure (CM-ADM-001): counts and content
-- bytes aggregate on the database side, so admission can size a session's
-- history without shipping any payload into the process.
SELECT
  COUNT(*)::BIGINT AS message_count,
  COALESCE(SUM(octet_length(m.content::text)), 0)::BIGINT AS content_bytes
FROM bot_visible_history_messages m
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND m.created_at >= sqlc.arg(created_at)
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync');

-- name: ListActiveMessagesSinceBySessionWithinBytes :many
-- Byte-budgeted variant of ListActiveMessagesSinceBySession (CM-ADM-001):
-- rows are admitted newest-first until their running content byte total
-- crosses max_bytes (the crossing row is kept, so the newest row always
-- loads), then returned in ascending order. Process memory is bounded by
-- max_bytes regardless of total history size.
SELECT
  ranked.id,
  ranked.bot_id,
  ranked.session_id,
  ranked.sender_channel_identity_id,
  ranked.sender_user_id,
  ranked.external_message_id,
  ranked.source_reply_to_message_id,
  ranked.role,
  ranked.content,
  ranked.metadata,
  ranked.usage,
  ranked.session_mode,
  ranked.runtime_type,
  ranked.event_id,
  ranked.display_text,
  ranked.compact_id,
  ranked.created_at,
  ranked.sender_display_name,
  ranked.sender_avatar_url,
  ranked.platform
FROM (
  SELECT
    m.id,
    m.bot_id,
    m.session_id,
    m.sender_channel_identity_id,
    m.sender_account_user_id AS sender_user_id,
    m.source_message_id AS external_message_id,
    m.source_reply_to_message_id,
    m.role,
    m.content,
    m.metadata,
    m.usage,
    m.session_mode,
    m.runtime_type,
    m.event_id,
    m.display_text,
    m.compact_id,
    m.created_at,
    m.turn_position,
    m.turn_message_seq,
    ci.display_name AS sender_display_name,
    ci.avatar_url AS sender_avatar_url,
    s.channel_type AS platform,
    (SUM(octet_length(m.content::text)) OVER (
      ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
    ) - octet_length(m.content::text))::BIGINT AS preceding_bytes
  FROM bot_visible_history_messages m
  LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
  LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.session_id = sqlc.arg(session_id)
    AND m.created_at >= sqlc.arg(created_at)
    AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
) ranked
WHERE ranked.preceding_bytes < sqlc.arg(max_bytes)::BIGINT
ORDER BY ranked.turn_position ASC, ranked.turn_message_seq ASC, ranked.created_at ASC, ranked.id ASC;

-- name: ListActiveMessagesSinceWithinBytes :many
-- Byte-budgeted variant of ListActiveMessagesSince (CM-ADM-001), same
-- newest-first admission semantics scoped to a bot instead of a session.
SELECT
  ranked.id,
  ranked.bot_id,
  ranked.session_id,
  ranked.sender_channel_identity_id,
  ranked.sender_user_id,
  ranked.external_message_id,
  ranked.source_reply_to_message_id,
  ranked.role,
  ranked.content,
  ranked.metadata,
  ranked.usage,
  ranked.session_mode,
  ranked.runtime_type,
  ranked.event_id,
  ranked.display_text,
  ranked.compact_id,
  ranked.created_at,
  ranked.sender_display_name,
  ranked.sender_avatar_url,
  ranked.platform
FROM (
  SELECT
    m.id,
    m.bot_id,
    m.session_id,
    m.sender_channel_identity_id,
    m.sender_account_user_id AS sender_user_id,
    m.source_message_id AS external_message_id,
    m.source_reply_to_message_id,
    m.role,
    m.content,
    m.metadata,
    m.usage,
    m.session_mode,
    m.runtime_type,
    m.event_id,
    m.display_text,
    m.compact_id,
    m.created_at,
    ci.display_name AS sender_display_name,
    ci.avatar_url AS sender_avatar_url,
    s.channel_type AS platform,
    (SUM(octet_length(m.content::text)) OVER (
      ORDER BY m.created_at DESC, m.id DESC
    ) - octet_length(m.content::text))::BIGINT AS preceding_bytes
  FROM bot_visible_history_messages m
  LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
  LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.bot_id = sqlc.arg(bot_id)
    AND m.created_at >= sqlc.arg(created_at)
    AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
) ranked
WHERE ranked.preceding_bytes < sqlc.arg(max_bytes)::BIGINT
ORDER BY ranked.created_at ASC, ranked.id ASC;

-- name: ListMessagesBefore :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.bot_id = sqlc.arg(bot_id)
  AND m.created_at < sqlc.arg(created_at)
ORDER BY m.created_at DESC, m.id DESC
LIMIT sqlc.arg(max_count);

-- name: ListMessagesBeforeBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.turn_id,
  m.turn_position,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND m.created_at < sqlc.arg(created_at)
ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
LIMIT sqlc.arg(max_count);

-- name: ListMessagesBeforeMessageBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
    < (
      SELECT c.turn_position, c.turn_message_seq, c.created_at, c.id
      FROM bot_history_messages c
      WHERE c.team_id = public.memoh_current_team_id() AND c.session_id = sqlc.arg(session_id)
        AND c.turn_visible = true
        AND c.turn_id IS NOT NULL
        AND c.turn_position IS NOT NULL
        AND c.turn_message_seq IS NOT NULL
        AND c.id = sqlc.arg(before_message_id)
      LIMIT 1
    )
ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
LIMIT sqlc.arg(max_count);

-- name: ListMessagesLatest :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.bot_id = sqlc.arg(bot_id)
ORDER BY m.created_at DESC, m.id DESC
LIMIT sqlc.arg(max_count);

-- name: ListMessagesLatestBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.turn_id,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
LIMIT sqlc.arg(max_count);

-- name: ListMessagesLatestUIBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.turn_id,
  m.turn_position,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
LIMIT sqlc.arg(max_count);

-- name: GetMessageByIDBySession :one
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.id = sqlc.arg(message_id)
LIMIT 1;

-- name: GetMessageByExternalIDBySession :one
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND m.source_message_id = sqlc.arg(external_message_id)
ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
LIMIT 1;

-- name: GetVisibleMessageCursorByExternalIDBySession :one
SELECT
  m.id,
  m.turn_position,
  m.turn_message_seq,
  m.created_at
FROM bot_history_messages m
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND m.source_message_id = sqlc.arg(external_message_id)
ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
LIMIT 1;

-- name: GetVisibleMessageCursorByIDBySession :one
SELECT
  m.id,
  m.turn_position,
  m.turn_message_seq,
  m.created_at
FROM bot_history_messages m
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND m.id = sqlc.arg(message_id)
LIMIT 1;

-- name: GetLocatedMessageByExternalIDBySession :one
SELECT
  m.id,
  m.turn_position,
  m.turn_message_seq,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND m.source_message_id = sqlc.arg(external_message_id)
ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
LIMIT 1;

-- name: LocateMessagesWindowByExternalIDBySession :many
WITH target AS (
  SELECT
    m.id,
    m.turn_position,
    m.turn_message_seq,
    m.created_at
  FROM bot_history_messages m
  WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
    AND m.turn_visible = true
    AND m.turn_id IS NOT NULL
    AND m.turn_position IS NOT NULL
    AND m.turn_message_seq IS NOT NULL
    AND m.source_message_id = sqlc.arg(external_message_id)
  ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
  LIMIT 1
),
before_rows AS (
  SELECT m.id
  FROM bot_history_messages m
  CROSS JOIN target
  WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
    AND m.turn_visible = true
    AND m.turn_id IS NOT NULL
    AND m.turn_position IS NOT NULL
    AND m.turn_message_seq IS NOT NULL
    AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
      < (target.turn_position, target.turn_message_seq, target.created_at, target.id)
  ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
  LIMIT sqlc.arg(before_limit)
),
after_rows AS (
  SELECT m.id
  FROM bot_history_messages m
  CROSS JOIN target
  WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
    AND m.turn_visible = true
    AND m.turn_id IS NOT NULL
    AND m.turn_position IS NOT NULL
    AND m.turn_message_seq IS NOT NULL
    AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
      > (target.turn_position, target.turn_message_seq, target.created_at, target.id)
  ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC
  LIMIT sqlc.arg(after_limit)
),
window_rows AS (
  SELECT id FROM before_rows
  UNION ALL
  SELECT id FROM target
  UNION ALL
  SELECT id FROM after_rows
)
SELECT
  target.id AS target_id,
  target.turn_message_seq AS target_turn_message_seq,
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.turn_id,
  m.turn_position,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM window_rows w
CROSS JOIN target
JOIN bot_history_messages m ON m.id = w.id
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: ListMessagesAfterBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND m.created_at > sqlc.arg(created_at)
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC
LIMIT sqlc.arg(max_count);

-- name: ListMessagesAfterMessageBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
    > (
      SELECT c.turn_position, c.turn_message_seq, c.created_at, c.id
      FROM bot_history_messages c
      WHERE c.team_id = public.memoh_current_team_id() AND c.session_id = sqlc.arg(session_id)
        AND c.turn_visible = true
        AND c.turn_id IS NOT NULL
        AND c.turn_position IS NOT NULL
        AND c.turn_message_seq IS NOT NULL
        AND c.id = sqlc.arg(after_message_id)
      LIMIT 1
    )
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC
LIMIT sqlc.arg(max_count);

-- name: ListMessagesBeforeCursorBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.turn_id,
  m.turn_position,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
    < (
      sqlc.arg(cursor_turn_position)::bigint,
      sqlc.arg(cursor_turn_message_seq)::bigint,
      sqlc.arg(cursor_created_at)::timestamptz,
      sqlc.arg(cursor_message_id)::uuid
    )
ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
LIMIT sqlc.arg(max_count);

-- name: ListMessagesAfterCursorBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND (m.turn_position, m.turn_message_seq, m.created_at, m.id)
    > (
      sqlc.arg(cursor_turn_position)::bigint,
      sqlc.arg(cursor_turn_message_seq)::bigint,
      sqlc.arg(cursor_created_at)::timestamptz,
      sqlc.arg(cursor_message_id)::uuid
    )
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC
LIMIT sqlc.arg(max_count);

-- name: ListVisibleMessagesFromBySession :many
WITH cursor_message AS (
  SELECT m.turn_position
  FROM bot_history_messages m
  WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
    AND m.turn_visible = true
    AND m.turn_id IS NOT NULL
    AND m.turn_position IS NOT NULL
    AND m.id = sqlc.arg(message_id)
  LIMIT 1
)
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.turn_id,
  m.usage,
  m.session_mode,
  m.runtime_type,
  m.event_id,
  m.display_text,
  m.compact_id,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform
FROM bot_history_messages m
CROSS JOIN cursor_message cursor
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.session_id = sqlc.arg(session_id)
  AND m.turn_visible = true
  AND m.turn_id IS NOT NULL
  AND m.turn_position IS NOT NULL
  AND m.turn_message_seq IS NOT NULL
  AND m.turn_position >= cursor.turn_position
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: CountMessagesByBot :one
SELECT COUNT(*) FROM bot_visible_history_messages
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id);

-- name: ClearHistoryByBot :exec
WITH target_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.bot_id = sqlc.arg(target_bot_id)
  ORDER BY session.id
  FOR UPDATE
),
invalidated_sessions AS (
  UPDATE bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1,
      runtime_fencing_token = nextval('session_runtime_fencing_token_seq')::bigint
  FROM target_sessions target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
  RETURNING session.id
),
deleted_acp_states AS (
  DELETE FROM acp_session_states state
  USING invalidated_sessions invalidated
  WHERE state.team_id = public.memoh_current_team_id()
    AND state.session_id = invalidated.id
  RETURNING state.session_id
),
deleted_acp_lines AS (
  DELETE FROM acp_session_state_lines line
  USING invalidated_sessions invalidated
  WHERE line.team_id = public.memoh_current_team_id()
    AND line.session_id = invalidated.id
  RETURNING line.session_id
),
deleted_acp_publications AS (
  DELETE FROM acp_session_publications publication
  USING invalidated_sessions invalidated
  WHERE publication.team_id = public.memoh_current_team_id()
    AND publication.session_id = invalidated.id
  RETURNING publication.session_id
),
target_compaction_artifacts AS MATERIALIZED (
  SELECT compact.id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.bot_id = sqlc.arg(target_bot_id)
    AND (SELECT count(*) FROM invalidated_sessions) >= 0
    AND (SELECT count(*) FROM deleted_acp_states) >= 0
    AND (SELECT count(*) FROM deleted_acp_lines) >= 0
    AND (SELECT count(*) FROM deleted_acp_publications) >= 0
  ORDER BY compact.id
  FOR UPDATE
),
deleted_compaction_artifacts AS (
  DELETE FROM bot_history_message_compacts AS compact
  USING target_compaction_artifacts target
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id = target.id
  RETURNING compact.id
),
target_messages AS MATERIALIZED (
  SELECT message.id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.bot_id = sqlc.arg(target_bot_id)
    AND (SELECT count(*) FROM target_sessions) >= 0
    AND (SELECT count(*) FROM deleted_compaction_artifacts) >= 0
  ORDER BY message.id
  FOR UPDATE
)
DELETE FROM bot_history_messages AS message
USING target_messages target
WHERE message.team_id = public.memoh_current_team_id()
  AND message.id = target.id;

-- name: ClearHistoryBySession :exec
WITH target_session AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(target_session_id)
  FOR UPDATE
),
invalidated_session AS (
  UPDATE bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1,
      runtime_fencing_token = nextval('session_runtime_fencing_token_seq')::bigint
  FROM target_session target
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = target.id
  RETURNING session.id
),
deleted_acp_state AS (
  DELETE FROM acp_session_states state
  USING invalidated_session invalidated
  WHERE state.team_id = public.memoh_current_team_id()
    AND state.session_id = invalidated.id
  RETURNING state.session_id
),
deleted_acp_lines AS (
  DELETE FROM acp_session_state_lines line
  USING invalidated_session invalidated
  WHERE line.team_id = public.memoh_current_team_id()
    AND line.session_id = invalidated.id
  RETURNING line.session_id
),
deleted_acp_publications AS (
  DELETE FROM acp_session_publications publication
  USING invalidated_session invalidated
  WHERE publication.team_id = public.memoh_current_team_id()
    AND publication.session_id = invalidated.id
  RETURNING publication.session_id
),
target_compaction_artifacts AS MATERIALIZED (
  SELECT compact.id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.session_id = sqlc.arg(target_session_id)
    AND (SELECT count(*) FROM invalidated_session) >= 0
    AND (SELECT count(*) FROM deleted_acp_state) >= 0
    AND (SELECT count(*) FROM deleted_acp_lines) >= 0
    AND (SELECT count(*) FROM deleted_acp_publications) >= 0
  ORDER BY compact.id
  FOR UPDATE
),
deleted_compaction_artifacts AS (
  DELETE FROM bot_history_message_compacts AS compact
  USING target_compaction_artifacts target
  WHERE compact.team_id = public.memoh_current_team_id()
    AND compact.id = target.id
  RETURNING compact.id
),
target_messages AS MATERIALIZED (
  SELECT message.id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.session_id = sqlc.arg(target_session_id)
    AND (SELECT count(*) FROM target_session) >= 0
    AND (SELECT count(*) FROM deleted_compaction_artifacts) >= 0
  ORDER BY message.id
  FOR UPDATE
)
DELETE FROM bot_history_messages AS message
USING target_messages target
WHERE message.team_id = public.memoh_current_team_id()
  AND message.id = target.id;

-- name: DeleteMessagesByIDs :exec
WITH session_locator AS MATERIALIZED (
  SELECT DISTINCT message.session_id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.id = ANY(sqlc.arg(ids)::uuid[])
    AND message.session_id IS NOT NULL
),
owner_sessions AS MATERIALIZED (
  SELECT session.id
  FROM bot_sessions session
  JOIN session_locator locator ON locator.session_id = session.id
  WHERE session.team_id = public.memoh_current_team_id()
  ORDER BY session.id
  FOR UPDATE
),
target_messages AS MATERIALIZED (
  SELECT message.id
  FROM bot_history_messages message
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.id = ANY(sqlc.arg(ids)::uuid[])
    AND (SELECT count(*) FROM owner_sessions) >= 0
  ORDER BY message.id
  FOR UPDATE
),
deleted AS (
  DELETE FROM bot_history_messages message
  USING target_messages target
  WHERE message.team_id = public.memoh_current_team_id()
    AND message.id = target.id
  RETURNING message.session_id, message.compact_id
),
compaction_epoch_bump AS (
  UPDATE bot_sessions session
  SET compaction_epoch = session.compaction_epoch + 1
  WHERE session.team_id = public.memoh_current_team_id()
    AND EXISTS (
    SELECT 1
    FROM deleted changed
    JOIN bot_history_message_compacts compact
      ON compact.id = changed.compact_id
     AND compact.team_id = public.memoh_current_team_id()
    WHERE changed.session_id = session.id
      AND compact.bot_id = session.bot_id
      AND compact.session_id = session.id
      AND compact.compaction_epoch = session.compaction_epoch
  )
  RETURNING session.id
)
SELECT (SELECT count(*) FROM deleted) + (SELECT count(*) * 0 FROM compaction_epoch_bump);

-- name: ListObservedConversationsByChannelIdentity :many
WITH observed_routes AS (
  SELECT
    s.route_id,
    MAX(m.created_at)::timestamptz AS last_observed_at
  FROM bot_visible_history_messages m
  JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.bot_id = sqlc.arg(bot_id)
    AND m.sender_channel_identity_id = sqlc.arg(channel_identity_id)::uuid
    AND s.route_id IS NOT NULL
  GROUP BY s.route_id
)
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
  COALESCE(NULLIF(TRIM(COALESCE(r.metadata->>'conversation_avatar_url', '')), ''), '')::text AS conversation_avatar_url,
  rr.last_observed_at
FROM observed_routes rr
JOIN bot_channel_routes r ON r.id = rr.route_id AND r.team_id = public.memoh_current_team_id()
GROUP BY
  r.id,
  r.channel_type,
  r.conversation_type,
  r.external_conversation_id,
  r.external_thread_id,
  r.metadata,
  rr.last_observed_at
ORDER BY rr.last_observed_at DESC;

-- name: ListObservedConversationsByChannelType :many
-- Routes on this platform type where the bot has seen at least one message (any sender).
WITH observed_routes AS (
  SELECT
    s.route_id,
    MAX(m.created_at)::timestamptz AS last_observed_at
  FROM bot_visible_history_messages m
  JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
  JOIN bot_channel_routes r ON r.id = s.route_id AND r.team_id = public.memoh_current_team_id()
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.bot_id = sqlc.arg(bot_id)
    AND LOWER(TRIM(r.channel_type)) = LOWER(TRIM(sqlc.arg(channel_type)))
    AND s.route_id IS NOT NULL
  GROUP BY s.route_id
)
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
  COALESCE(NULLIF(TRIM(COALESCE(r.metadata->>'conversation_avatar_url', '')), ''), '')::text AS conversation_avatar_url,
  rr.last_observed_at
FROM observed_routes rr
JOIN bot_channel_routes r ON r.id = rr.route_id AND r.team_id = public.memoh_current_team_id()
GROUP BY
  r.id,
  r.channel_type,
  r.conversation_type,
  r.external_conversation_id,
  r.external_thread_id,
  r.metadata,
  rr.last_observed_at
ORDER BY rr.last_observed_at DESC;

-- name: SearchMessages :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.role,
  m.content,
  m.created_at,
  ci.display_name AS sender_display_name,
  s.channel_type AS platform
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci ON ci.id = m.sender_channel_identity_id AND ci.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.bot_id = sqlc.arg(bot_id)
  AND m.session_id = ANY(sqlc.arg(session_ids)::uuid[])
  AND (sqlc.narg(session_id)::uuid IS NULL OR m.session_id = sqlc.narg(session_id)::uuid)
  AND (sqlc.narg(contact_id)::uuid IS NULL OR m.sender_channel_identity_id = sqlc.narg(contact_id)::uuid)
  AND (sqlc.narg(start_time)::timestamptz IS NULL OR m.created_at >= sqlc.narg(start_time)::timestamptz)
  AND (sqlc.narg(end_time)::timestamptz IS NULL OR m.created_at <= sqlc.narg(end_time)::timestamptz)
  AND (sqlc.narg(role)::text IS NULL OR m.role = sqlc.narg(role)::text)
  AND (sqlc.narg(keyword)::text IS NULL OR (
    CASE
      WHEN jsonb_typeof(m.content->'content') = 'string'
        THEN m.content->>'content'
      WHEN jsonb_typeof(m.content->'content') = 'array'
        THEN (SELECT COALESCE(string_agg(elem->>'text', ' '), '')
              FROM jsonb_array_elements(m.content->'content') AS elem
              WHERE elem->>'type' = 'text')
      ELSE ''
    END
  ) ILIKE '%' || sqlc.narg(keyword)::text || '%')
ORDER BY m.created_at DESC, m.id DESC
LIMIT sqlc.arg(max_count);

-- name: MarkMessagesCompacted :execrows
WITH expected_claims AS MATERIALIZED (
  SELECT ids.message_id, claims.expected_compact_id
  FROM UNNEST(sqlc.arg(message_ids)::uuid[]) WITH ORDINALITY AS ids(message_id, ordinal)
  JOIN UNNEST(sqlc.arg(expected_compact_ids)::uuid[]) WITH ORDINALITY AS claims(expected_compact_id, ordinal)
    USING (ordinal)
), artifact_locator AS MATERIALIZED (
  SELECT compact.id, compact.session_id
  FROM bot_history_message_compacts compact
  WHERE compact.team_id = public.memoh_current_team_id()
    AND (
      compact.id = sqlc.arg(compact_id)
      OR compact.id = ANY(sqlc.arg(expected_compact_ids)::uuid[])
    )
), owner_sessions AS MATERIALIZED (
  SELECT session.id, session.bot_id, session.compaction_epoch
  FROM bot_sessions session
  JOIN (
    SELECT DISTINCT locator.session_id
    FROM artifact_locator locator
    WHERE locator.session_id IS NOT NULL
  ) owner ON owner.session_id = session.id
  WHERE session.team_id = public.memoh_current_team_id()
  ORDER BY session.id
  FOR UPDATE OF session
), locked_artifacts AS MATERIALIZED (
  SELECT
    compact.id,
    compact.bot_id,
    compact.session_id,
    compact.compaction_epoch,
    compact.status,
    compact.summary,
    compact.started_at
  FROM bot_history_message_compacts compact
  JOIN artifact_locator locator ON locator.id = compact.id
  WHERE compact.team_id = public.memoh_current_team_id()
    AND (SELECT count(*) FROM owner_sessions) >= 0
    AND (
      compact.session_id IS NULL
      OR EXISTS (
        SELECT 1
        FROM owner_sessions owner
        WHERE owner.id = compact.session_id
      )
    )
  ORDER BY compact.id
  FOR UPDATE OF compact
), target_compact AS MATERIALIZED (
  SELECT compact.id, compact.bot_id, compact.session_id, compact.compaction_epoch
  FROM locked_artifacts compact
  JOIN owner_sessions owner
    ON owner.id = compact.session_id
   AND owner.bot_id = compact.bot_id
   AND owner.compaction_epoch = compact.compaction_epoch
  WHERE compact.id = sqlc.arg(compact_id)
    AND compact.status = 'pending'
), foreign_pending_claims AS MATERIALIZED (
  SELECT current_claim.id
  FROM locked_artifacts current_claim
  CROSS JOIN target_compact target
  WHERE current_claim.id = ANY(sqlc.arg(expected_compact_ids)::uuid[])
    AND current_claim.status = 'pending'
    AND (
      current_claim.bot_id IS DISTINCT FROM target.bot_id
      OR current_claim.session_id IS DISTINCT FROM target.session_id
    )
), reclaimable_pending_claims AS MATERIALIZED (
  SELECT current_claim.id
  FROM locked_artifacts current_claim
  CROSS JOIN target_compact target
  WHERE current_claim.id = ANY(sqlc.arg(expected_compact_ids)::uuid[])
    AND current_claim.status = 'pending'
    AND CARDINALITY(sqlc.arg(message_ids)::uuid[]) = CARDINALITY(sqlc.arg(expected_compact_ids)::uuid[])
    AND current_claim.bot_id IS NOT DISTINCT FROM target.bot_id
    AND current_claim.session_id IS NOT DISTINCT FROM target.session_id
    AND (
      current_claim.compaction_epoch IS DISTINCT FROM target.compaction_epoch
      OR current_claim.started_at <= now() - INTERVAL '15 minutes'
    )
  ORDER BY current_claim.id
), stable_claims AS MATERIALIZED (
  SELECT current_claim.id
  FROM locked_artifacts current_claim
  CROSS JOIN target_compact target
  WHERE current_claim.id = ANY(sqlc.arg(expected_compact_ids)::uuid[])
    AND current_claim.status <> 'pending'
    AND (
      current_claim.bot_id IS DISTINCT FROM target.bot_id
      OR current_claim.session_id IS DISTINCT FROM target.session_id
      OR current_claim.compaction_epoch IS DISTINCT FROM target.compaction_epoch
      OR current_claim.status <> 'ok'
      OR NULLIF(BTRIM(current_claim.summary, E' \t\n\r\f\x0B'), '') IS NULL
    )
), locked_messages AS MATERIALIZED (
  SELECT message.id, claim.expected_compact_id
  FROM bot_history_messages message
  JOIN expected_claims claim ON claim.message_id = message.id
  CROSS JOIN target_compact target
  WHERE message.team_id = public.memoh_current_team_id()
    AND CARDINALITY(sqlc.arg(message_ids)::uuid[]) = CARDINALITY(sqlc.arg(expected_compact_ids)::uuid[])
    AND message.compact_id IS NOT DISTINCT FROM claim.expected_compact_id
    AND message.bot_id = target.bot_id
    AND message.session_id = target.session_id
    AND message.turn_visible = true
    AND message.turn_id IS NOT NULL
    AND message.turn_position IS NOT NULL
    AND message.turn_message_seq IS NOT NULL
    AND (
      claim.expected_compact_id IS NULL
      OR EXISTS (
        SELECT 1
        FROM reclaimable_pending_claims reclaimable
        WHERE reclaimable.id = claim.expected_compact_id
      )
      OR EXISTS (
        SELECT 1
        FROM foreign_pending_claims foreign_claim
        WHERE foreign_claim.id = claim.expected_compact_id
      )
      OR EXISTS (
        SELECT 1
        FROM stable_claims stable
        WHERE stable.id = claim.expected_compact_id
      )
    )
  ORDER BY message.id
  FOR UPDATE OF message
), claims_to_reclaim AS MATERIALIZED (
  SELECT DISTINCT reclaimable.id
  FROM reclaimable_pending_claims reclaimable
  JOIN locked_messages message
    ON message.expected_compact_id = reclaimable.id
), reclaimed_claims AS MATERIALIZED (
  UPDATE bot_history_message_compacts current_claim
  SET status = 'error',
      error_message = 'source lease reclaimed',
      completed_at = now()
  FROM claims_to_reclaim reclaimable
  WHERE current_claim.team_id = public.memoh_current_team_id()
    AND current_claim.id = reclaimable.id
    AND current_claim.status = 'pending'
  RETURNING current_claim.id
)
UPDATE bot_history_messages message
SET compact_id = target.id
FROM locked_messages locked,
     target_compact target
WHERE message.team_id = public.memoh_current_team_id()
  AND message.id = locked.id
  AND message.compact_id IS NOT DISTINCT FROM locked.expected_compact_id
  AND message.bot_id = target.bot_id
  AND message.session_id = target.session_id
  AND message.turn_visible = true
  AND message.turn_id IS NOT NULL
  AND message.turn_position IS NOT NULL
  AND message.turn_message_seq IS NOT NULL
  AND (
    locked.expected_compact_id IS NULL
    OR EXISTS (
      SELECT 1
      FROM reclaimed_claims reclaimed
      WHERE reclaimed.id = locked.expected_compact_id
    )
    OR EXISTS (
      SELECT 1
      FROM foreign_pending_claims foreign_claim
      WHERE foreign_claim.id = locked.expected_compact_id
    )
    OR EXISTS (
      SELECT 1
      FROM stable_claims stable
      WHERE stable.id = locked.expected_compact_id
    )
  );

-- name: ListUncompactedMessagesBySession :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.event_id,
  m.display_text,
  m.compact_id,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform,
  s.compaction_epoch,
  r.conversation_type AS conversation_type,
  COALESCE(
    NULLIF(TRIM(COALESCE(r.metadata->>'conversation_name', '')), ''),
    NULLIF(TRIM(COALESCE(r.metadata->>'conversation_handle', '')), ''),
    ''
  )::text AS conversation_name,
  r.default_reply_target AS reply_target
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci
  ON ci.id = m.sender_channel_identity_id
 AND ci.team_id = public.memoh_current_team_id()
JOIN bot_sessions s
  ON s.id = m.session_id
 AND s.team_id = public.memoh_current_team_id()
LEFT JOIN bot_channel_routes r
  ON r.id = s.route_id
 AND r.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = $1
  -- A fresh pending claim is a 15-minute cross-process lease. Stale pending,
  -- error, deleted, and blank-summary claims remain reclaimable; current OK
  -- summaries stay ineligible exactly as on the read path.
  AND (m.compact_id IS NULL OR NOT EXISTS (
    SELECT 1 FROM bot_history_message_compacts c
    WHERE c.team_id = public.memoh_current_team_id()
      AND c.id = m.compact_id
      AND c.bot_id = m.bot_id
      AND c.session_id = s.id
      AND c.compaction_epoch = s.compaction_epoch
      AND (
        (c.status = 'ok' AND NULLIF(BTRIM(c.summary, E' \t\n\r\f\x0B'), '') IS NOT NULL)
        OR (c.status = 'pending' AND c.started_at > now() - INTERVAL '15 minutes')
      )
  ))
  AND (m.metadata->>'trigger_mode' IS NULL OR m.metadata->>'trigger_mode' != 'passive_sync')
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: ListMessagesByCompactID :many
SELECT
  m.id,
  m.bot_id,
  m.session_id,
  m.sender_channel_identity_id,
  m.sender_account_user_id AS sender_user_id,
  m.source_message_id AS external_message_id,
  m.source_reply_to_message_id,
  m.role,
  m.content,
  m.metadata,
  m.usage,
  m.event_id,
  m.display_text,
  m.compact_id,
  m.created_at,
  ci.display_name AS sender_display_name,
  ci.avatar_url AS sender_avatar_url,
  s.channel_type AS platform,
  s.compaction_epoch,
  r.conversation_type AS conversation_type,
  COALESCE(
    NULLIF(TRIM(COALESCE(r.metadata->>'conversation_name', '')), ''),
    NULLIF(TRIM(COALESCE(r.metadata->>'conversation_handle', '')), ''),
    ''
  )::text AS conversation_name,
  r.default_reply_target AS reply_target
FROM bot_visible_history_messages m
LEFT JOIN channel_identities ci
  ON ci.id = m.sender_channel_identity_id
 AND ci.team_id = public.memoh_current_team_id()
JOIN bot_sessions s
  ON s.id = m.session_id
 AND s.team_id = public.memoh_current_team_id()
LEFT JOIN bot_channel_routes r
  ON r.id = s.route_id
 AND r.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id()
  AND m.compact_id = sqlc.arg(compact_id)
ORDER BY m.turn_position ASC, m.turn_message_seq ASC, m.created_at ASC, m.id ASC;

-- name: ListMessageRefsByCompactID :many
-- Backfills coverage for summaries that predate persisted artifact coverage
-- without pulling every compacted row's full content/usage.
SELECT
  m.id,
  m.bot_id,
  m.session_id
FROM bot_history_messages m
WHERE m.team_id = public.memoh_current_team_id()
  AND m.compact_id = $1
ORDER BY m.created_at ASC, m.id ASC;
