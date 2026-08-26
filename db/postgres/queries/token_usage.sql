-- name: GetTokenUsageByDayAndType :many
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
      COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END
      ),
      'chat'
    )
  END::text AS session_type,
  date_trunc('day', m.created_at)::date AS day,
  COALESCE(SUM((m.usage->>'inputTokens')::bigint), 0)::bigint AS input_tokens,
  COALESCE(SUM((m.usage->>'outputTokens')::bigint), 0)::bigint AS output_tokens,
  COALESCE(SUM((m.usage->'inputTokenDetails'->>'cacheReadTokens')::bigint), 0)::bigint AS cache_read_tokens,
  COALESCE(SUM((m.usage->'outputTokenDetails'->>'reasoningTokens')::bigint), 0)::bigint AS reasoning_tokens
FROM bot_history_messages m
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions ps ON ps.id = s.parent_session_id AND ps.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.bot_id = sqlc.arg(bot_id)
  AND m.usage IS NOT NULL
  AND m.created_at >= sqlc.arg(from_time)
  AND m.created_at < sqlc.arg(to_time)
  AND (sqlc.narg(model_id)::uuid IS NULL OR m.model_id = sqlc.narg(model_id)::uuid)
  AND (
    sqlc.narg(session_type)::text IS NULL
    OR (sqlc.narg(session_type)::text = 'acp_agent' AND COALESCE(
      NULLIF(m.runtime_type, ''),
      CASE
        WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
        ELSE NULLIF(s.runtime_type, '')
      END,
      CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
    ) = 'acp_agent')
    OR (sqlc.narg(session_type)::text <> 'acp_agent' AND COALESCE(
      NULLIF(m.runtime_type, ''),
      CASE
        WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
        ELSE NULLIF(s.runtime_type, '')
      END,
      CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
    ) <> 'acp_agent' AND COALESCE(
      COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END
      ),
      'chat'
    ) = sqlc.narg(session_type)::text)
  )
GROUP BY session_type, day
ORDER BY day, session_type;

-- name: GetTokenUsageByModel :many
SELECT
  m.model_id,
  COALESCE(mo.model_id, 'unknown') AS model_slug,
  COALESCE(mo.name, mo.model_id, 'Unknown') AS model_name,
  COALESCE(lp.name, 'Unknown') AS provider_name,
  COALESCE(SUM((m.usage->>'inputTokens')::bigint), 0)::bigint AS input_tokens,
  COALESCE(SUM((m.usage->>'outputTokens')::bigint), 0)::bigint AS output_tokens
FROM bot_history_messages m
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions ps ON ps.id = s.parent_session_id AND ps.team_id = public.memoh_current_team_id()
LEFT JOIN models mo ON mo.id = m.model_id AND mo.team_id = public.memoh_current_team_id()
LEFT JOIN providers lp ON lp.id = mo.provider_id AND lp.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.bot_id = sqlc.arg(bot_id)
  AND m.usage IS NOT NULL
  AND m.created_at >= sqlc.arg(from_time)
  AND m.created_at < sqlc.arg(to_time)
  AND (
    sqlc.narg(session_type)::text IS NULL
    OR (sqlc.narg(session_type)::text = 'acp_agent' AND COALESCE(
      NULLIF(m.runtime_type, ''),
      CASE
        WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
        ELSE NULLIF(s.runtime_type, '')
      END,
      CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
    ) = 'acp_agent')
    OR (sqlc.narg(session_type)::text <> 'acp_agent' AND COALESCE(
      NULLIF(m.runtime_type, ''),
      CASE
        WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
        ELSE NULLIF(s.runtime_type, '')
      END,
      CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
    ) <> 'acp_agent' AND COALESCE(
      COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END
      ),
      'chat'
    ) = sqlc.narg(session_type)::text)
  )
GROUP BY m.model_id, mo.model_id, mo.name, lp.name
ORDER BY input_tokens DESC;

-- name: ListTokenUsageRecords :many
SELECT
  m.id,
  m.created_at,
  m.session_id,
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
      COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END
      ),
      'chat'
    )
  END::text AS session_type,
  m.model_id,
  COALESCE(mo.model_id, 'unknown')::text AS model_slug,
  COALESCE(mo.name, mo.model_id, 'Unknown')::text AS model_name,
  COALESCE(lp.name, 'Unknown')::text AS provider_name,
  COALESCE((m.usage->>'inputTokens')::bigint, 0)::bigint AS input_tokens,
  COALESCE((m.usage->>'outputTokens')::bigint, 0)::bigint AS output_tokens,
  COALESCE((m.usage->'inputTokenDetails'->>'cacheReadTokens')::bigint, 0)::bigint AS cache_read_tokens,
  COALESCE((m.usage->'outputTokenDetails'->>'reasoningTokens')::bigint, 0)::bigint AS reasoning_tokens
FROM bot_history_messages m
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions ps ON ps.id = s.parent_session_id AND ps.team_id = public.memoh_current_team_id()
LEFT JOIN models mo ON mo.id = m.model_id AND mo.team_id = public.memoh_current_team_id()
LEFT JOIN providers lp ON lp.id = mo.provider_id AND lp.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.bot_id = sqlc.arg(bot_id)
  AND m.usage IS NOT NULL
  AND m.created_at >= sqlc.arg(from_time)
  AND m.created_at < sqlc.arg(to_time)
  AND (sqlc.narg(model_id)::uuid IS NULL OR m.model_id = sqlc.narg(model_id)::uuid)
  AND (
    sqlc.narg(session_type)::text IS NULL
    OR (sqlc.narg(session_type)::text = 'acp_agent' AND COALESCE(
      NULLIF(m.runtime_type, ''),
      CASE
        WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
        ELSE NULLIF(s.runtime_type, '')
      END,
      CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
    ) = 'acp_agent')
    OR (sqlc.narg(session_type)::text <> 'acp_agent' AND COALESCE(
      NULLIF(m.runtime_type, ''),
      CASE
        WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
        ELSE NULLIF(s.runtime_type, '')
      END,
      CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
    ) <> 'acp_agent' AND COALESCE(
      COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END
      ),
      'chat'
    ) = sqlc.narg(session_type)::text)
  )
ORDER BY m.created_at DESC, m.id DESC
LIMIT sqlc.arg(page_limit)
OFFSET sqlc.arg(page_offset);

-- name: CountTokenUsageRecords :one
SELECT COUNT(*)::bigint AS total
FROM bot_history_messages m
LEFT JOIN bot_sessions s ON s.id = m.session_id AND s.team_id = public.memoh_current_team_id()
LEFT JOIN bot_sessions ps ON ps.id = s.parent_session_id AND ps.team_id = public.memoh_current_team_id()
WHERE m.team_id = public.memoh_current_team_id() AND m.bot_id = sqlc.arg(bot_id)
  AND m.usage IS NOT NULL
  AND m.created_at >= sqlc.arg(from_time)
  AND m.created_at < sqlc.arg(to_time)
  AND (sqlc.narg(model_id)::uuid IS NULL OR m.model_id = sqlc.narg(model_id)::uuid)
  AND (
    sqlc.narg(session_type)::text IS NULL
    OR (sqlc.narg(session_type)::text = 'acp_agent' AND COALESCE(
      NULLIF(m.runtime_type, ''),
      CASE
        WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
        ELSE NULLIF(s.runtime_type, '')
      END,
      CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
    ) = 'acp_agent')
    OR (sqlc.narg(session_type)::text <> 'acp_agent' AND COALESCE(
      NULLIF(m.runtime_type, ''),
      CASE
        WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN NULLIF(ps.runtime_type, '')
        ELSE NULLIF(s.runtime_type, '')
      END,
      CASE WHEN s.type = 'acp_agent' THEN 'acp_agent' ELSE '' END
    ) <> 'acp_agent' AND COALESCE(
      COALESCE(
        NULLIF(m.session_mode, ''),
        CASE
          WHEN COALESCE(NULLIF(s.type, ''), '') = 'subagent' THEN COALESCE(NULLIF(ps.session_mode, ''), NULLIF(ps.type, ''), 'chat')
          ELSE COALESCE(NULLIF(s.session_mode, ''), NULLIF(s.type, ''), 'chat')
        END
      ),
      'chat'
    ) = sqlc.narg(session_type)::text)
  );
