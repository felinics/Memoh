-- name: CountMessagesBySession :one
SELECT COUNT(*)::bigint AS message_count
FROM bot_visible_history_messages
WHERE team_id = public.memoh_current_team_id() AND session_id = sqlc.arg(session_id);

-- name: GetLatestContextUsage :one
-- The session's runtime and its newest visible context state: a native
-- assistant message with usage, or the newest External Agent assistant
-- message, whose context_usage may be absent. An unknown state is returned as
-- is, never skipped for an older one.
SELECT
  s.runtime_type AS session_runtime_type,
  COALESCE(latest.runtime_type, '')::text AS message_runtime_type,
  latest.usage,
  latest.context_usage
FROM bot_sessions s
LEFT JOIN LATERAL (
  SELECT
    m.runtime_type,
    m.usage,
    (m.metadata->'context_usage')::jsonb AS context_usage
  FROM bot_visible_history_messages m
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.session_id = s.id
    AND m.role = 'assistant'
    AND (m.runtime_type <> 'model' OR m.usage IS NOT NULL)
  ORDER BY m.turn_position DESC, m.turn_message_seq DESC, m.created_at DESC, m.id DESC
  LIMIT 1
) latest ON true
WHERE s.team_id = public.memoh_current_team_id()
  AND s.id = sqlc.arg(session_id);

-- name: MarkLatestContextUsageStale :execrows
-- A runtime operation such as manual compaction replaced the context the
-- newest observation measured. The mark stays on that message, so a refresh
-- or restart still reads unknown until a later turn records a new state.
UPDATE bot_history_messages m
SET metadata = jsonb_set(m.metadata, '{context_usage,stale}', to_jsonb(sqlc.arg(reason)::text))
WHERE m.team_id = public.memoh_current_team_id()
  AND jsonb_typeof(m.metadata->'context_usage') = 'object'
  AND m.id = (
    SELECT v.id
    FROM bot_visible_history_messages v
    WHERE v.team_id = public.memoh_current_team_id()
      AND v.session_id = sqlc.arg(session_id)
      AND v.role = 'assistant'
      AND (v.runtime_type <> 'model' OR v.usage IS NOT NULL)
    ORDER BY v.turn_position DESC, v.turn_message_seq DESC, v.created_at DESC, v.id DESC
    LIMIT 1
  )
  AND EXISTS (
    SELECT 1
    FROM bot_sessions s
    WHERE s.team_id = public.memoh_current_team_id()
      AND s.id = m.session_id
      AND (sqlc.narg(fencing_token)::bigint IS NULL OR s.runtime_fencing_token <= sqlc.narg(fencing_token)::bigint)
  );

-- name: GetLatestSessionModelID :one
SELECT m.model_id
FROM bot_visible_history_messages m
WHERE m.session_id = sqlc.arg(session_id)
  AND m.model_id IS NOT NULL
ORDER BY m.created_at DESC
LIMIT 1;

-- name: GetSessionCacheStats :one
SELECT
  COALESCE(SUM((m.usage->>'inputTokens')::bigint), 0)::bigint AS total_input_tokens,
  COALESCE(SUM((m.usage->'inputTokenDetails'->>'cacheReadTokens')::bigint), 0)::bigint AS cache_read_tokens
FROM bot_visible_history_messages m
WHERE m.team_id = public.memoh_current_team_id()
  AND m.session_id = sqlc.arg(session_id)
  AND m.usage IS NOT NULL;

-- name: GetLatestSessionIDByBot :one
SELECT s.id
FROM bot_sessions s
WHERE s.team_id = public.memoh_current_team_id()
  AND s.bot_id = sqlc.arg(bot_id)
  AND s.type = 'chat'
  AND s.deleted_at IS NULL
ORDER BY s.updated_at DESC
LIMIT 1;

-- name: GetSessionUsedSkills :many
WITH requested AS (
  SELECT DISTINCT
    (item->>'name')::text AS skill_name,
    (
      'requested:' ||
      COALESCE(NULLIF(item->>'source_kind', ''), 'unknown') || ':' ||
      COALESCE(NULLIF(item->>'opaque_source_id', ''), '') || ':' ||
      (item->>'name')
    )::text AS skill_identity
  FROM bot_visible_history_messages m,
    jsonb_array_elements(
      CASE WHEN jsonb_typeof(m.metadata->'model_requested_skills') = 'array'
           THEN m.metadata->'model_requested_skills'
           ELSE '[]'::jsonb
      END
    ) AS item
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.session_id = sqlc.arg(session_id)
    AND m.role = 'user'
    AND item->>'name' IS NOT NULL
    AND item->>'name' != ''
),
tool_payloads AS (
  SELECT
    CASE WHEN jsonb_typeof(m.content->'content') = 'array'
         THEN m.content->'content'
         WHEN jsonb_typeof(m.content) = 'array'
         THEN m.content
         ELSE '[]'::jsonb
    END AS content_json
  FROM bot_visible_history_messages m
  WHERE m.team_id = public.memoh_current_team_id()
    AND m.session_id = sqlc.arg(session_id)
    AND m.role = 'assistant'
),
tool_used AS (
  SELECT DISTINCT
    COALESCE(
      part->'input'->>'skillName',
      part->'input'->>'skill_name',
      part->'input'->>'name'
    )::text AS skill_name,
    (
      'tool_call:' ||
      COALESCE(
        part->'input'->>'skillName',
        part->'input'->>'skill_name',
        part->'input'->>'name'
      )
    )::text AS skill_identity
  FROM tool_payloads p,
    jsonb_array_elements(p.content_json) AS part
  WHERE part->>'type' = 'tool-call'
    AND COALESCE(part->>'toolName', part->>'tool_name') = 'use_skill'
    AND COALESCE(
      part->'input'->>'skillName',
      part->'input'->>'skill_name',
      part->'input'->>'name'
    ) IS NOT NULL
    AND COALESCE(
      part->'input'->>'skillName',
      part->'input'->>'skill_name',
      part->'input'->>'name'
    ) != ''
),
skill_rows AS (
  SELECT skill_identity, skill_name FROM requested
  UNION ALL
  SELECT skill_identity, skill_name FROM tool_used
),
deduped AS (
  SELECT DISTINCT skill_identity, skill_name FROM skill_rows
)
SELECT DISTINCT skill_name FROM deduped
ORDER BY skill_name;
