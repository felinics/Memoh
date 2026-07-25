-- name: CreateToolApprovalRequest :one
WITH locked_session AS (
  SELECT id
  FROM agent.bot_sessions
  WHERE team_id = iam.memoh_current_team_id()
    AND id = sqlc.arg(session_id)
  FOR UPDATE
),
next_short_id AS (
  SELECT COALESCE(MAX(agent.tool_approval_requests.short_id), 0) + 1 AS short_id
  FROM locked_session
  LEFT JOIN agent.tool_approval_requests ON agent.tool_approval_requests.session_id = locked_session.id
    AND agent.tool_approval_requests.team_id = iam.memoh_current_team_id()
)
INSERT INTO agent.tool_approval_requests (
  bot_id,
  session_id,
  route_id,
  channel_identity_id,
  workspace_target_id,
  tool_call_id,
  tool_name,
  operation,
  tool_input,
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
  sqlc.narg(route_id),
  sqlc.narg(channel_identity_id),
  sqlc.arg(workspace_target_id),
  sqlc.arg(tool_call_id),
  sqlc.arg(tool_name),
  sqlc.arg(operation),
  sqlc.arg(tool_input),
  next_short_id.short_id,
  sqlc.narg(runtime_fencing_token),
  sqlc.narg(requested_by_channel_identity_id),
  sqlc.narg(requested_message_id),
  sqlc.arg(source_platform),
  sqlc.arg(reply_target),
  sqlc.arg(conversation_type)
FROM locked_session
CROSS JOIN next_short_id
ON CONFLICT (team_id, session_id, tool_call_id) DO UPDATE
SET tool_input = agent.tool_approval_requests.tool_input
WHERE agent.tool_approval_requests.status = 'pending'
  AND agent.tool_approval_requests.runtime_fencing_token IS NOT DISTINCT FROM EXCLUDED.runtime_fencing_token
  AND agent.tool_approval_requests.tool_name = EXCLUDED.tool_name
  AND agent.tool_approval_requests.operation = EXCLUDED.operation
  AND agent.tool_approval_requests.tool_input = EXCLUDED.tool_input
  AND agent.tool_approval_requests.workspace_target_id = EXCLUDED.workspace_target_id
RETURNING *;

-- name: GetToolApprovalRequest :one
SELECT *
FROM agent.tool_approval_requests
WHERE team_id = iam.memoh_current_team_id() AND id = $1;

-- name: ClaimToolApprovalRequestForRuntime :one
UPDATE agent.tool_approval_requests
SET runtime_fencing_token = sqlc.arg(runtime_fencing_token)
WHERE team_id = iam.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending'
  AND (runtime_fencing_token IS NULL OR runtime_fencing_token <= sqlc.arg(runtime_fencing_token))
RETURNING *;

-- name: GetPendingToolApprovalBySessionShortID :one
SELECT *
FROM agent.tool_approval_requests
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND short_id = $3
  AND status = 'pending';

-- name: GetLatestPendingToolApprovalBySession :one
SELECT *
FROM agent.tool_approval_requests
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND status = 'pending'
ORDER BY created_at DESC, short_id DESC
LIMIT 1;

-- name: GetPendingToolApprovalByReplyMessage :one
SELECT *
FROM agent.tool_approval_requests
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND prompt_external_message_id = $3
  AND status = 'pending'
ORDER BY created_at DESC
LIMIT 1;

-- name: UpdateToolApprovalPromptMessage :one
UPDATE agent.tool_approval_requests
SET prompt_message_id = sqlc.narg(prompt_message_id),
    prompt_external_message_id = sqlc.arg(prompt_external_message_id)
WHERE team_id = iam.memoh_current_team_id() AND id = sqlc.arg(id)
RETURNING *;

-- name: ApproveToolApprovalRequest :one
UPDATE agent.tool_approval_requests
SET status = 'approved',
    decision_reason = sqlc.arg(reason),
    decided_by_channel_identity_id = sqlc.narg(decided_by_channel_identity_id),
    decided_at = now()
WHERE team_id = iam.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND status = 'pending'
  AND (runtime_fencing_token IS NULL OR runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint)
RETURNING *;

-- name: RejectToolApprovalRequest :one
UPDATE agent.tool_approval_requests
SET status = 'rejected',
    decision_reason = sqlc.arg(reason),
    decided_by_channel_identity_id = sqlc.narg(decided_by_channel_identity_id),
    decided_at = now()
WHERE team_id = iam.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND status = 'pending'
  AND (runtime_fencing_token IS NULL OR runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint)
RETURNING *;

-- name: CancelPendingToolApprovalsBySession :many
UPDATE agent.tool_approval_requests
SET status = 'cancelled',
    decision_reason = sqlc.arg(reason),
    decided_at = now()
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending'
  AND (runtime_fencing_token IS NULL OR runtime_fencing_token = sqlc.narg(runtime_fencing_token)::bigint)
RETURNING *;

-- name: SupersedePendingToolApprovalsBySession :many
UPDATE agent.tool_approval_requests
SET status = 'cancelled',
    decision_reason = sqlc.arg(reason),
    decided_at = now()
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending'
  AND runtime_fencing_token IS NOT NULL
  AND id IS DISTINCT FROM sqlc.narg(preserve_id)::uuid
RETURNING *;

-- name: ListPendingToolApprovalsBySession :many
SELECT *
FROM agent.tool_approval_requests
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
  AND status = 'pending'
ORDER BY created_at ASC, short_id ASC;

-- name: ListToolApprovalsBySession :many
SELECT *
FROM agent.tool_approval_requests
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = $1
  AND session_id = $2
ORDER BY created_at ASC, short_id ASC;

-- name: ListToolApprovalsBySessionToolCalls :many
SELECT *
FROM agent.tool_approval_requests
WHERE team_id = iam.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND tool_call_id = ANY(sqlc.arg(tool_call_ids)::text[])
ORDER BY created_at ASC, short_id ASC;
