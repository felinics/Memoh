-- name: ListRecentAssistantMessagesBySession :many
SELECT
  id,
  run_id,
  role,
  (metadata #- '{context_lifecycle,selection_decisions}'::text[])::jsonb AS metadata,
  created_at
FROM bot_history_messages
WHERE session_id = sqlc.arg(session_id)
  AND role = 'assistant'
  AND metadata ? 'context_lifecycle'
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(max_count);

-- name: CreateContextLifecycle :one
INSERT INTO context_lifecycles (
  run_id,
  bot_id,
  session_id,
  status,
  error_code,
  snapshot,
  selection_decisions
)
VALUES (
  sqlc.arg(run_id),
  sqlc.arg(bot_id),
  sqlc.arg(session_id),
  sqlc.arg(status),
  sqlc.narg(error_code)::text,
  sqlc.arg(snapshot)::jsonb - 'selection_decisions',
  sqlc.arg(snapshot)::jsonb -> 'selection_decisions'
)
RETURNING run_id, bot_id, session_id, status, error_code, created_at;

-- name: GetContextLifecycleByRunID :one
SELECT run_id, team_id, bot_id, session_id, status, error_code, snapshot, created_at
FROM context_lifecycles
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id);

-- name: GetContextLifecycleSelectionDecisionsByRunID :one
SELECT selection_decisions
FROM context_lifecycles
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id);

-- name: UpdateAbortedContextLifecycleSnapshot :one
UPDATE context_lifecycles
SET snapshot = sqlc.arg(snapshot)::jsonb - 'selection_decisions',
    selection_decisions = COALESCE(sqlc.arg(snapshot)::jsonb -> 'selection_decisions', selection_decisions)
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'aborted'
RETURNING run_id, bot_id, session_id, status, error_code, created_at;

-- name: UpsertAbortedContextLifecycle :one
INSERT INTO context_lifecycles (
  run_id,
  bot_id,
  session_id,
  status,
  error_code,
  snapshot,
  selection_decisions
)
VALUES (
  sqlc.arg(run_id),
  sqlc.arg(bot_id),
  sqlc.arg(session_id),
  'aborted',
  NULL,
  sqlc.arg(snapshot)::jsonb - 'selection_decisions',
  sqlc.arg(snapshot)::jsonb -> 'selection_decisions'
)
ON CONFLICT (run_id) DO UPDATE
SET
  status = 'aborted',
  error_code = NULL
WHERE context_lifecycles.team_id = public.memoh_current_team_id()
  AND context_lifecycles.bot_id = EXCLUDED.bot_id
  AND context_lifecycles.session_id = EXCLUDED.session_id
RETURNING run_id, bot_id, session_id, status, error_code, created_at;

-- name: UpsertTerminalContextLifecycle :one
INSERT INTO context_lifecycles (
  run_id,
  bot_id,
  session_id,
  status,
  error_code,
  snapshot,
  selection_decisions
)
VALUES (
  sqlc.arg(run_id),
  sqlc.arg(bot_id),
  sqlc.arg(session_id),
  sqlc.arg(status),
  sqlc.narg(error_code)::text,
  sqlc.arg(snapshot)::jsonb - 'selection_decisions',
  sqlc.arg(snapshot)::jsonb -> 'selection_decisions'
)
ON CONFLICT (run_id) DO UPDATE
SET
  status = EXCLUDED.status,
  error_code = CASE
    WHEN sqlc.arg(replace_error_code)::boolean
      OR context_lifecycles.status IS DISTINCT FROM EXCLUDED.status
      THEN EXCLUDED.error_code
    ELSE COALESCE(context_lifecycles.error_code, EXCLUDED.error_code)
  END,
  snapshot = CASE
    WHEN sqlc.arg(replace_snapshot)::boolean THEN EXCLUDED.snapshot
    ELSE context_lifecycles.snapshot
  END,
  selection_decisions = CASE
    WHEN sqlc.arg(replace_snapshot)::boolean
      THEN COALESCE(EXCLUDED.selection_decisions, context_lifecycles.selection_decisions)
    ELSE context_lifecycles.selection_decisions
  END
WHERE context_lifecycles.team_id = public.memoh_current_team_id()
  AND context_lifecycles.team_id = EXCLUDED.team_id
  AND context_lifecycles.bot_id = EXCLUDED.bot_id
  AND context_lifecycles.session_id = EXCLUDED.session_id
RETURNING run_id, bot_id, session_id, status, error_code, created_at;

-- name: GetLatestAssistantContextLifecycleByRunID :one
SELECT id, metadata
FROM bot_history_messages
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND role = 'assistant'
  AND metadata ? 'context_lifecycle'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: GetLatestAssistantContextLifecycleMetadataByRunID :one
SELECT metadata
FROM bot_history_messages
WHERE team_id = public.memoh_current_team_id()
  AND run_id = sqlc.arg(run_id)
  AND role = 'assistant'
  AND metadata ? 'context_lifecycle'
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListRecentContextLifecyclesBySession :many
SELECT
  run_id,
  status,
  error_code,
  created_at,
  (snapshot - 'selection_decisions'::text)::jsonb AS snapshot
FROM context_lifecycles
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
ORDER BY created_at DESC, run_id DESC
LIMIT sqlc.arg(max_count);

-- name: GetLatestContextLifecycleBySession :one
SELECT (snapshot - 'selection_decisions'::text)::jsonb AS snapshot
FROM context_lifecycles
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
ORDER BY created_at DESC, run_id DESC
LIMIT 1;

-- name: ListTerminalSessionRunsNeedingContextLifecycle :many
SELECT
  session_runs.run_id,
  session_runs.bot_id,
  session_runs.session_id,
  session_runs.fencing_token,
  session_runs.state,
  session_runs.error_code
FROM session_runs
LEFT JOIN context_lifecycles
  ON context_lifecycles.team_id = session_runs.team_id
 AND context_lifecycles.run_id = session_runs.run_id
WHERE session_runs.team_id = public.memoh_current_team_id()
  AND session_runs.state IN ('completed', 'aborted', 'failed', 'lost')
  AND (
    context_lifecycles.run_id IS NULL
    OR NOT (
      (session_runs.state = 'completed' AND context_lifecycles.status IN ('completed', 'fallback'))
      OR (session_runs.state = 'failed' AND context_lifecycles.status IN ('failed_provider', 'failed_budget'))
      OR (session_runs.state = 'aborted' AND context_lifecycles.status = 'aborted')
      OR (session_runs.state = 'lost' AND context_lifecycles.status = 'failed_provider')
    )
  )
ORDER BY session_runs.updated_at, session_runs.run_id
LIMIT sqlc.arg(batch_size);

-- name: HasUnmaterializedContextLifecycleMetadataBySession :one
SELECT EXISTS (
  SELECT 1
  FROM bot_history_messages AS messages
  WHERE messages.session_id = sqlc.arg(session_id)
    AND messages.role = 'assistant'
    AND messages.metadata ? 'context_lifecycle'
    AND NOT EXISTS (
      SELECT 1
      FROM context_lifecycles AS lifecycles
      WHERE lifecycles.team_id = public.memoh_current_team_id()
        AND lifecycles.run_id = messages.run_id
    )
);
