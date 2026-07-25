-- name: GetTokenUsageByDayAndType :many
WITH scoped_usage AS (
  SELECT
    CASE
      WHEN COALESCE(
        NULLIF(m.runtime_type, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
          ELSE NULLIF(s.runtime_type, '')
        END,
        CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
      ) = 'acp_agent' THEN 'acp_agent'
      ELSE COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent'
            THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END,
        'chat'
      )
    END::text AS session_type,
    m.created_at,
    m.usage
  FROM agent.bot_history_messages m
  LEFT JOIN agent.bot_sessions s
    ON s.id = m.session_id
   AND s.team_id = iam.memoh_current_team_id()
  LEFT JOIN agent.bot_sessions ps
    ON ps.id = s.parent_session_id
   AND ps.team_id = iam.memoh_current_team_id()
  WHERE m.team_id = iam.memoh_current_team_id()
    AND m.bot_id = sqlc.arg(bot_id)
    AND m.usage IS NOT NULL
    AND m.created_at >= sqlc.arg(from_time)
    AND m.created_at < sqlc.arg(to_time)
    AND (sqlc.narg(model_id)::uuid IS NULL OR m.model_id = sqlc.narg(model_id)::uuid)
)
SELECT
  session_type,
  date_trunc('day', created_at)::date AS day,
  COALESCE(SUM((usage->>'inputTokens')::bigint), 0)::bigint AS input_tokens,
  COALESCE(SUM((usage->>'outputTokens')::bigint), 0)::bigint AS output_tokens,
  COALESCE(SUM((usage->'inputTokenDetails'->>'cacheReadTokens')::bigint), 0)::bigint AS cache_read_tokens,
  COALESCE(SUM((usage->'outputTokenDetails'->>'reasoningTokens')::bigint), 0)::bigint AS reasoning_tokens
FROM scoped_usage
WHERE sqlc.narg(session_type)::text IS NULL
   OR session_type = sqlc.narg(session_type)::text
GROUP BY session_type, day
ORDER BY day, session_type;

-- name: GetTokenUsageByModel :many
-- Keep the full model breakdown so consumers can retain complete filter options.
WITH scoped_usage AS (
  SELECT
    m.model_id,
    m.usage,
    CASE
      WHEN COALESCE(
        NULLIF(m.runtime_type, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
          ELSE NULLIF(s.runtime_type, '')
        END,
        CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
      ) = 'acp_agent' THEN 'acp_agent'
      ELSE COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent'
            THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END,
        'chat'
      )
    END::text AS session_type
  FROM agent.bot_history_messages m
  LEFT JOIN agent.bot_sessions s
    ON s.id = m.session_id
   AND s.team_id = iam.memoh_current_team_id()
  LEFT JOIN agent.bot_sessions ps
    ON ps.id = s.parent_session_id
   AND ps.team_id = iam.memoh_current_team_id()
  WHERE m.team_id = iam.memoh_current_team_id()
    AND m.bot_id = sqlc.arg(bot_id)
    AND m.usage IS NOT NULL
    AND m.created_at >= sqlc.arg(from_time)
    AND m.created_at < sqlc.arg(to_time)
)
SELECT
  model_id,
  COALESCE(SUM((usage->>'inputTokens')::bigint), 0)::bigint AS input_tokens,
  COALESCE(SUM((usage->>'outputTokens')::bigint), 0)::bigint AS output_tokens
FROM scoped_usage
WHERE sqlc.narg(session_type)::text IS NULL
   OR session_type = sqlc.narg(session_type)::text
GROUP BY model_id
ORDER BY input_tokens DESC;

-- name: ListTokenUsageRecords :many
WITH scoped_usage AS (
  SELECT
    m.id,
    m.created_at,
    m.session_id,
    m.model_id,
    m.usage,
    CASE
      WHEN COALESCE(
        NULLIF(m.runtime_type, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
          ELSE NULLIF(s.runtime_type, '')
        END,
        CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
      ) = 'acp_agent' THEN 'acp_agent'
      ELSE COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent'
            THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END,
        'chat'
      )
    END::text AS session_type
  FROM agent.bot_history_messages m
  LEFT JOIN agent.bot_sessions s
    ON s.id = m.session_id
   AND s.team_id = iam.memoh_current_team_id()
  LEFT JOIN agent.bot_sessions ps
    ON ps.id = s.parent_session_id
   AND ps.team_id = iam.memoh_current_team_id()
  WHERE m.team_id = iam.memoh_current_team_id()
    AND m.bot_id = sqlc.arg(bot_id)
    AND m.usage IS NOT NULL
    AND m.created_at >= sqlc.arg(from_time)
    AND m.created_at < sqlc.arg(to_time)
    AND (sqlc.narg(model_id)::uuid IS NULL OR m.model_id = sqlc.narg(model_id)::uuid)
)
SELECT
  id,
  created_at,
  session_id,
  session_type,
  model_id,
  COALESCE((usage->>'inputTokens')::bigint, 0)::bigint AS input_tokens,
  COALESCE((usage->>'outputTokens')::bigint, 0)::bigint AS output_tokens,
  COALESCE((usage->'inputTokenDetails'->>'cacheReadTokens')::bigint, 0)::bigint AS cache_read_tokens,
  COALESCE((usage->'outputTokenDetails'->>'reasoningTokens')::bigint, 0)::bigint AS reasoning_tokens
FROM scoped_usage
WHERE sqlc.narg(session_type)::text IS NULL
   OR session_type = sqlc.narg(session_type)::text
ORDER BY created_at DESC, id DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: CountTokenUsageRecords :one
WITH scoped_usage AS (
  SELECT
    CASE
      WHEN COALESCE(
        NULLIF(m.runtime_type, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
          ELSE NULLIF(s.runtime_type, '')
        END,
        CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
      ) = 'acp_agent' THEN 'acp_agent'
      ELSE COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent'
            THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END,
        'chat'
      )
    END::text AS session_type
  FROM agent.bot_history_messages m
  LEFT JOIN agent.bot_sessions s
    ON s.id = m.session_id
   AND s.team_id = iam.memoh_current_team_id()
  LEFT JOIN agent.bot_sessions ps
    ON ps.id = s.parent_session_id
   AND ps.team_id = iam.memoh_current_team_id()
  WHERE m.team_id = iam.memoh_current_team_id()
    AND m.bot_id = sqlc.arg(bot_id)
    AND m.usage IS NOT NULL
    AND m.created_at >= sqlc.arg(from_time)
    AND m.created_at < sqlc.arg(to_time)
    AND (sqlc.narg(model_id)::uuid IS NULL OR m.model_id = sqlc.narg(model_id)::uuid)
)
SELECT COUNT(*)::bigint AS total
FROM scoped_usage
WHERE sqlc.narg(session_type)::text IS NULL
   OR session_type = sqlc.narg(session_type)::text;
