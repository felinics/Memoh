-- name: CreateUserInputRequest :one
WITH locked_session AS (
  SELECT id
  FROM bot_sessions
  WHERE team_id = public.memoh_current_team_id()
    AND id = sqlc.arg(session_id)
  FOR UPDATE
),
next_short_id AS (
  SELECT COALESCE(MAX(user_input_requests.short_id), 0) + 1 AS short_id
  FROM locked_session
  LEFT JOIN user_input_requests ON user_input_requests.session_id = locked_session.id
    AND user_input_requests.team_id = public.memoh_current_team_id()
),
runtime_scope AS (
  SELECT session_runs.run_id, session_runs.turn_id
  FROM session_runs
  WHERE session_runs.team_id = public.memoh_current_team_id()
    AND session_runs.bot_id = sqlc.arg(bot_id)
    AND session_runs.session_id = sqlc.arg(session_id)
    AND session_runs.fencing_token = sqlc.narg(runtime_fencing_token)::bigint
    AND session_runs.state IN ('running', 'waiting_decision')
  UNION ALL
  SELECT NULL::uuid, NULL::uuid
  WHERE sqlc.narg(runtime_fencing_token)::bigint IS NULL
)
INSERT INTO user_input_requests (
  bot_id,
  session_id,
  run_id,
  turn_id,
  route_id,
  channel_identity_id,
  workspace_target_id,
  tool_call_id,
  tool_name,
  short_id,
  runtime_fencing_token,
  input_json,
  ui_payload_json,
  provider_metadata,
  requested_by_channel_identity_id,
  source_platform,
  reply_target,
  conversation_type,
  expires_at
) SELECT
  sqlc.arg(bot_id),
  sqlc.arg(session_id),
  runtime_scope.run_id,
  runtime_scope.turn_id,
  sqlc.narg(route_id),
  sqlc.narg(channel_identity_id),
  sqlc.arg(workspace_target_id),
  sqlc.arg(tool_call_id),
  sqlc.arg(tool_name),
  next_short_id.short_id,
  sqlc.narg(runtime_fencing_token),
  sqlc.arg(input_json),
  sqlc.arg(ui_payload_json),
  sqlc.arg(provider_metadata),
  sqlc.narg(requested_by_channel_identity_id),
  sqlc.arg(source_platform),
  sqlc.arg(reply_target),
  sqlc.arg(conversation_type),
  sqlc.narg(expires_at)
FROM locked_session
CROSS JOIN next_short_id
CROSS JOIN runtime_scope
ON CONFLICT (team_id, session_id, tool_call_id) DO UPDATE
SET requested_by_channel_identity_id = EXCLUDED.requested_by_channel_identity_id,
    source_platform = EXCLUDED.source_platform,
    reply_target = EXCLUDED.reply_target,
    conversation_type = EXCLUDED.conversation_type,
    expires_at = EXCLUDED.expires_at,
    updated_at = now()
WHERE user_input_requests.status = 'pending'
  AND user_input_requests.runtime_fencing_token IS NOT DISTINCT FROM EXCLUDED.runtime_fencing_token
  AND user_input_requests.run_id IS NOT DISTINCT FROM EXCLUDED.run_id
  AND user_input_requests.turn_id IS NOT DISTINCT FROM EXCLUDED.turn_id
  AND user_input_requests.input_json = EXCLUDED.input_json
  AND user_input_requests.ui_payload_json = EXCLUDED.ui_payload_json
  AND user_input_requests.provider_metadata = EXCLUDED.provider_metadata
  AND user_input_requests.workspace_target_id = EXCLUDED.workspace_target_id
  AND (user_input_requests.expires_at IS NULL OR user_input_requests.expires_at > now())
RETURNING *;

-- name: GetUserInputRequest :one
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id() AND id = $1;

-- name: ListPendingUserInputsByRun :many
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id()
  AND run_id = $1
  AND status = 'pending'
  AND (expires_at IS NULL OR expires_at > now())
ORDER BY created_at ASC, short_id ASC;

-- name: GetRespondableUserInputRequest :one
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND status = 'pending'
  AND (
    (
      sqlc.narg(runtime_fencing_token)::bigint IS NOT NULL
      AND runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint
    )
    OR (
      sqlc.narg(runtime_fencing_token)::bigint IS NULL
      AND runtime_fencing_token IS NULL
      AND (expires_at IS NULL OR expires_at > now())
    )
  );

-- name: ClaimUserInputRequestForRuntime :one
UPDATE user_input_requests
SET runtime_fencing_token = sqlc.arg(runtime_fencing_token),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending'
  AND (
    runtime_fencing_token = sqlc.arg(runtime_fencing_token)
    OR (
      (runtime_fencing_token IS NULL OR runtime_fencing_token < sqlc.arg(runtime_fencing_token))
      AND (expires_at IS NULL OR expires_at > now())
    )
  )
RETURNING *;

-- name: GetUserInputRequestBySessionToolCall :one
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id()
  AND session_id = $1
  AND tool_call_id = $2;

-- name: GetPendingUserInputBySessionShortID :one
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND short_id = $3
  AND status = 'pending'
  AND (expires_at IS NULL OR expires_at > now());

-- name: GetLatestPendingUserInputBySession :one
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND status = 'pending'
  AND (expires_at IS NULL OR expires_at > now())
ORDER BY created_at DESC, short_id DESC
LIMIT 1;

-- name: GetPendingUserInputByReplyMessage :one
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND prompt_external_message_id = $3
  AND status = 'pending'
  AND (expires_at IS NULL OR expires_at > now())
ORDER BY created_at DESC
LIMIT 1;

-- name: UpdateUserInputPromptMessage :one
UPDATE user_input_requests
SET prompt_message_id = sqlc.narg(prompt_message_id),
    prompt_external_message_id = sqlc.arg(prompt_external_message_id),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: UpdateUserInputInteraction :one
UPDATE user_input_requests
SET interaction_json = sqlc.arg(interaction_json),
    interaction_revision = interaction_revision + 1,
    updated_at = now()
WHERE id = sqlc.arg(id)
  AND status = 'pending'
  AND interaction_revision = sqlc.arg(interaction_revision)
  AND (expires_at IS NULL OR expires_at > now())
RETURNING *;

-- name: UpdateUserInputAssistantMessage :one
UPDATE user_input_requests
SET assistant_message_id = sqlc.narg(assistant_message_id),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: UpdateUserInputToolResultMessage :one
UPDATE user_input_requests
SET tool_result_message_id = sqlc.narg(tool_result_message_id),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: SubmitUserInputRequest :one
UPDATE user_input_requests
SET status = 'submitted',
    result_json = sqlc.arg(result_json),
    responded_by_channel_identity_id = sqlc.narg(responded_by_channel_identity_id),
    response_control_id = sqlc.narg(response_control_id)::text,
    response_payload_hash = sqlc.narg(response_payload_hash)::text,
    responded_at = now(),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND status = 'pending'
  AND (
    runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint
    OR (runtime_fencing_token IS NULL AND (expires_at IS NULL OR expires_at > now()))
  )
RETURNING *;

-- name: CancelUserInputRequest :one
UPDATE user_input_requests
SET status = 'canceled',
    result_json = sqlc.arg(result_json),
    responded_by_channel_identity_id = sqlc.narg(responded_by_channel_identity_id),
    response_control_id = sqlc.narg(response_control_id)::text,
    response_payload_hash = sqlc.narg(response_payload_hash)::text,
    responded_at = now(),
    canceled_at = now(),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND status = 'pending'
  AND (
    runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint
    OR (runtime_fencing_token IS NULL AND (expires_at IS NULL OR expires_at > now()))
  )
RETURNING *;

-- name: CancelPendingUserInputsBySession :many
UPDATE user_input_requests
SET status = 'canceled',
    result_json = sqlc.arg(result_json),
    responded_at = now(),
    canceled_at = now(),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending'
  AND (
    runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint
    OR (runtime_fencing_token IS NULL AND (expires_at IS NULL OR expires_at > now()))
  )
RETURNING *;

-- name: SupersedePendingUserInputsBySession :many
UPDATE user_input_requests
SET status = 'canceled',
    result_json = sqlc.arg(result_json),
    responded_at = now(),
    canceled_at = now(),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending'
  AND runtime_fencing_token IS NOT NULL
  AND id != ALL(sqlc.arg(preserve_ids)::uuid[])
RETURNING *;

-- name: FailUserInputRequest :one
UPDATE user_input_requests
SET status = 'failed',
    result_json = sqlc.arg(result_json),
    updated_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND status = 'pending'
  AND (
    runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint
    OR (runtime_fencing_token IS NULL AND (expires_at IS NULL OR expires_at > now()))
  )
RETURNING *;

-- name: ListPendingUserInputsBySession :many
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND status = 'pending'
  AND (expires_at IS NULL OR expires_at > now())
ORDER BY created_at ASC, short_id ASC;

-- name: ListUserInputsBySession :many
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
ORDER BY created_at ASC, short_id ASC;

-- name: ListUserInputsBySessionToolCalls :many
SELECT *
FROM user_input_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND tool_call_id = ANY(sqlc.arg(tool_call_ids)::text[])
ORDER BY created_at ASC, short_id ASC;
