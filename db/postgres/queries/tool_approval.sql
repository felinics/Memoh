-- name: CreateToolApprovalRequest :one
WITH locked_session AS (
  SELECT id
  FROM bot_sessions
  WHERE team_id = public.memoh_current_team_id()
    AND id = sqlc.arg(session_id)
  FOR UPDATE
),
next_short_id AS (
  SELECT COALESCE(MAX(tool_approval_requests.short_id), 0) + 1 AS short_id
  FROM locked_session
  LEFT JOIN tool_approval_requests ON tool_approval_requests.session_id = locked_session.id
    AND tool_approval_requests.team_id = public.memoh_current_team_id()
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
INSERT INTO tool_approval_requests (
  bot_id,
  session_id,
  run_id,
  turn_id,
  route_id,
  channel_identity_id,
  workspace_target_id,
  tool_call_id,
  tool_name,
  operation,
  tool_input,
  options,
  short_id,
  runtime_fencing_token,
  requested_by_channel_identity_id,
  requested_message_id,
  source_platform,
  reply_target,
  conversation_type
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
  sqlc.arg(operation),
  sqlc.arg(tool_input),
  COALESCE(sqlc.arg(options)::jsonb, '[]'::jsonb),
  next_short_id.short_id,
  sqlc.narg(runtime_fencing_token),
  sqlc.narg(requested_by_channel_identity_id),
  sqlc.narg(requested_message_id),
  sqlc.arg(source_platform),
  sqlc.arg(reply_target),
  sqlc.arg(conversation_type)
FROM locked_session
CROSS JOIN next_short_id
CROSS JOIN runtime_scope
ON CONFLICT (team_id, session_id, tool_call_id) DO UPDATE
SET tool_input = tool_approval_requests.tool_input
WHERE tool_approval_requests.status = 'pending'
  AND tool_approval_requests.runtime_fencing_token IS NOT DISTINCT FROM EXCLUDED.runtime_fencing_token
  AND tool_approval_requests.run_id IS NOT DISTINCT FROM EXCLUDED.run_id
  AND tool_approval_requests.turn_id IS NOT DISTINCT FROM EXCLUDED.turn_id
  AND tool_approval_requests.tool_name = EXCLUDED.tool_name
  AND tool_approval_requests.operation = EXCLUDED.operation
  AND tool_approval_requests.tool_input = EXCLUDED.tool_input
  AND tool_approval_requests.options = EXCLUDED.options
  AND tool_approval_requests.workspace_target_id = EXCLUDED.workspace_target_id
RETURNING *;

-- name: GetToolApprovalRequest :one
SELECT *
FROM tool_approval_requests
WHERE team_id = public.memoh_current_team_id() AND id = $1;

-- name: ListPendingToolApprovalsByRun :many
SELECT *
FROM tool_approval_requests
WHERE team_id = public.memoh_current_team_id()
  AND run_id = $1
  AND status = 'pending'
ORDER BY created_at ASC, short_id ASC;

-- name: ClaimToolApprovalRequestForRuntime :one
UPDATE tool_approval_requests
SET runtime_fencing_token = sqlc.arg(runtime_fencing_token)
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending'
  AND (runtime_fencing_token IS NULL OR runtime_fencing_token <= sqlc.arg(runtime_fencing_token))
RETURNING *;

-- name: GetPendingToolApprovalBySessionShortID :one
SELECT *
FROM tool_approval_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND short_id = $3
  AND status = 'pending';

-- name: GetLatestPendingToolApprovalBySession :one
SELECT *
FROM tool_approval_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND status = 'pending'
ORDER BY created_at DESC, short_id DESC
LIMIT 1;

-- name: GetPendingToolApprovalByReplyMessage :one
SELECT *
FROM tool_approval_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND prompt_external_message_id = $3
  AND status = 'pending'
ORDER BY created_at DESC
LIMIT 1;

-- name: UpdateToolApprovalPromptMessage :one
UPDATE tool_approval_requests
SET prompt_message_id = sqlc.narg(prompt_message_id),
    prompt_external_message_id = sqlc.arg(prompt_external_message_id)
WHERE team_id = public.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: ApproveToolApprovalRequest :one
UPDATE tool_approval_requests
SET status = 'approved',
    decision_reason = sqlc.arg(reason),
    selected_option_id = sqlc.arg(selected_option_id),
    decided_by_channel_identity_id = sqlc.narg(decided_by_channel_identity_id),
    response_control_id = sqlc.narg(response_control_id)::text,
    response_payload_hash = sqlc.narg(response_payload_hash)::text,
    decided_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND status = 'pending'
  AND (runtime_fencing_token IS NULL OR runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint)
RETURNING *;

-- name: RejectToolApprovalRequest :one
UPDATE tool_approval_requests
SET status = 'rejected',
    decision_reason = sqlc.arg(reason),
    selected_option_id = sqlc.arg(selected_option_id),
    decided_by_channel_identity_id = sqlc.narg(decided_by_channel_identity_id),
    response_control_id = sqlc.narg(response_control_id)::text,
    response_payload_hash = sqlc.narg(response_payload_hash)::text,
    decided_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND status = 'pending'
  AND (runtime_fencing_token IS NULL OR runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint)
RETURNING *;

-- name: CancelPendingToolApprovalsBySession :many
UPDATE tool_approval_requests
SET status = 'cancelled',
    decision_reason = sqlc.arg(reason),
    decided_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending'
  AND (runtime_fencing_token IS NULL OR runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint)
RETURNING *;

-- name: SupersedePendingToolApprovalsBySession :many
UPDATE tool_approval_requests
SET status = 'cancelled',
    decision_reason = sqlc.arg(reason),
    decided_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending'
  AND runtime_fencing_token IS NOT NULL
  AND id != ALL(sqlc.arg(preserve_ids)::uuid[])
RETURNING *;

-- name: ListPendingToolApprovalsBySession :many
SELECT *
FROM tool_approval_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND status = 'pending'
ORDER BY created_at ASC, short_id ASC;

-- name: ListToolApprovalsBySession :many
SELECT *
FROM tool_approval_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
ORDER BY created_at ASC, short_id ASC;

-- name: ListToolApprovalsBySessionToolCalls :many
SELECT *
FROM tool_approval_requests
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND tool_call_id = ANY(sqlc.arg(tool_call_ids)::text[])
ORDER BY created_at ASC, short_id ASC;
