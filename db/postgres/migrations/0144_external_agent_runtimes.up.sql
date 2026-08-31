-- 0144_external_agent_runtimes
-- Direct codex / claude-code runtimes: widen runtime_type constraints and cut
-- existing codex/claude-code ACP agents, sessions, and bindings over
-- to the new direct runtimes. ACP keeps serving generic/custom agents only.

-- 1) Constraint widening ------------------------------------------------------

ALTER TABLE bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_runtime_type_check;
ALTER TABLE bot_sessions
  ADD CONSTRAINT bot_sessions_runtime_type_check
  CHECK (runtime_type IN ('model', 'acp_agent', 'codex', 'claude-code'));

ALTER TABLE bot_history_messages
  DROP CONSTRAINT IF EXISTS bot_history_messages_runtime_type_check;
ALTER TABLE bot_history_messages
  ADD CONSTRAINT bot_history_messages_runtime_type_check
  CHECK (runtime_type IN ('model', 'acp_agent', 'codex', 'claude-code'));

ALTER TABLE bots
  DROP CONSTRAINT IF EXISTS bots_chat_runtime_check;
ALTER TABLE bots
  ADD CONSTRAINT bots_chat_runtime_check
  CHECK (chat_runtime IN ('model', 'acp_agent', 'codex', 'claude-code'));

ALTER TABLE schedule
  DROP CONSTRAINT IF EXISTS schedule_runtime_type_check;
ALTER TABLE schedule
  ADD CONSTRAINT schedule_runtime_type_check
  CHECK (runtime_type IS NULL OR runtime_type IN ('model', 'acp_agent', 'codex', 'claude-code'));

-- 0141 deliberately left this CHECK NOT VALID so historical rows that predate
-- the execution-shape contract do not block an upgrade. Add the widened form
-- the same way, then validate it when the existing data is clean. A legacy
-- violation keeps the constraint NOT VALID while every new or updated row is
-- still enforced.
DO $schedule_acp_fields_check$
BEGIN
  ALTER TABLE schedule
    DROP CONSTRAINT IF EXISTS schedule_acp_fields_check;
  ALTER TABLE schedule
    ADD CONSTRAINT schedule_acp_fields_check
    CHECK (
      run_target <> 'new_session'
      OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
      OR (runtime_type IN ('codex', 'claude-code') AND bot_agent_id IS NOT NULL AND acp_agent_id IS NULL AND model_id IS NULL)
      OR (COALESCE(runtime_type, 'model') = 'model' AND bot_agent_id IS NULL AND acp_agent_id IS NULL AND acp_model_id IS NULL)
    ) NOT VALID;

  BEGIN
    ALTER TABLE schedule VALIDATE CONSTRAINT schedule_acp_fields_check;
  EXCEPTION
    WHEN check_violation THEN NULL;
  END;
END
$schedule_acp_fields_check$;

-- 2) Bot agents: codex/claude-code ACP rows become direct runtimes. The
--    metadata.provider key stays in place (inert) so the down migration can
--    restore the ACP shape.

-- The data steps below scan tables under FORCE row level security keyed on
-- memoh.team_id, which a migration connection never sets. Lift the policies
-- for the data steps and restore them after, the same way 0125/0128 do.
ALTER TABLE bot_agents NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_agents DISABLE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages DISABLE ROW LEVEL SECURITY;
ALTER TABLE bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE schedule NO FORCE ROW LEVEL SECURITY;
ALTER TABLE schedule DISABLE ROW LEVEL SECURITY;

UPDATE bot_agents agent
SET runtime = 'codex',
    metadata = agent.metadata || jsonb_strip_nulls(jsonb_build_object(
      'auth', CASE
        WHEN lower(btrim(COALESCE(bot.metadata#>>'{acp,agents,codex,setup_mode}', ''))) = 'api_key' THEN 'api_key'
        ELSE 'chatgpt'
      END,
      'base_url', NULLIF(btrim(COALESCE(bot.metadata#>>'{acp,agents,codex,managed,base_url}', '')), '')
    )),
    updated_at = now()
FROM bots bot
WHERE agent.bot_id = bot.id
  AND agent.team_id = bot.team_id
  AND agent.runtime = 'acp'
  AND lower(btrim(COALESCE(agent.metadata->>'provider', ''))) = 'codex';

UPDATE bot_agents agent
SET runtime = 'claude-code',
    metadata = agent.metadata || jsonb_strip_nulls(jsonb_build_object(
      'auth', CASE lower(btrim(COALESCE(bot.metadata#>>'{acp,agents,claude-code,setup_mode}', '')))
        WHEN 'api_key' THEN 'api_key'
        WHEN 'oauth' THEN 'oauth_token'
        ELSE 'workspace'
      END,
      'base_url', NULLIF(btrim(COALESCE(bot.metadata#>>'{acp,agents,claude-code,managed,base_url}', '')), '')
    )),
    updated_at = now()
FROM bots bot
WHERE agent.bot_id = bot.id
  AND agent.team_id = bot.team_id
  AND agent.runtime = 'acp'
  AND lower(btrim(COALESCE(agent.metadata->>'provider', ''))) = 'claude-code';

-- The removed built-in profile has no successor. Tombstone its BotAgent rows
-- so it disappears from every active list while existing session/history FKs
-- remain valid. The marker records the previous enabled state for an exact
-- down migration; it is removed again on rollback.
UPDATE bot_agents
SET enabled = FALSE,
    deleted_at = now(),
    metadata = metadata || jsonb_build_object('_migration_0144_removed_profile_enabled', enabled),
    updated_at = now()
WHERE runtime = 'acp'
  AND lower(btrim(COALESCE(metadata->>'provider', ''))) = 'hermes'
  AND deleted_at IS NULL;

-- Every legacy direct-runtime reference needs a BotAgent binding. Collect the
-- providers implied by bot defaults, existing sessions, and schedules once so
-- a bot that uses Codex or Claude Code on only one surface is still upgraded.
WITH needed_agents AS (
  SELECT bot.team_id, bot.id AS bot_id, lower(btrim(bot.chat_acp_agent_id)) AS provider
  FROM bots bot
  WHERE bot.chat_runtime = 'acp_agent'
    AND lower(btrim(COALESCE(bot.chat_acp_agent_id, ''))) IN ('codex', 'claude-code')
  UNION
  SELECT session.team_id, session.bot_id,
    lower(btrim(COALESCE(session.runtime_metadata->>'acp_agent_id', session.metadata->>'acp_agent_id', '')))
  FROM bot_sessions session
  WHERE session.runtime_type = 'acp_agent'
    AND lower(btrim(COALESCE(session.runtime_metadata->>'acp_agent_id', session.metadata->>'acp_agent_id', ''))) IN ('codex', 'claude-code')
  UNION
  SELECT sched.team_id, sched.bot_id, lower(btrim(sched.acp_agent_id))
  FROM schedule sched
  WHERE sched.runtime_type = 'acp_agent'
    AND lower(btrim(COALESCE(sched.acp_agent_id, ''))) IN ('codex', 'claude-code')
    AND sched.run_target = 'new_session'
    AND sched.model_id IS NULL
)
INSERT INTO bot_agents (team_id, bot_id, name, runtime, enabled, metadata)
SELECT
  needed.team_id,
  needed.bot_id,
  selected_name.name,
  needed.provider,
  true,
  jsonb_strip_nulls(jsonb_build_object(
    'provider', needed.provider,
    'auth', CASE needed.provider
      WHEN 'codex' THEN CASE
        WHEN lower(btrim(COALESCE(bot.metadata#>>'{acp,agents,codex,setup_mode}', ''))) = 'api_key' THEN 'api_key'
        ELSE 'chatgpt'
      END
      ELSE CASE lower(btrim(COALESCE(bot.metadata#>>'{acp,agents,claude-code,setup_mode}', '')))
        WHEN 'api_key' THEN 'api_key'
        WHEN 'oauth' THEN 'oauth_token'
        ELSE 'workspace'
      END
    END,
    'base_url', NULLIF(btrim(COALESCE(
      CASE needed.provider
        WHEN 'codex' THEN bot.metadata#>>'{acp,agents,codex,managed,base_url}'
        ELSE bot.metadata#>>'{acp,agents,claude-code,managed,base_url}'
      END,
      ''
    )), '')
  ))
FROM needed_agents needed
JOIN bots bot ON bot.team_id = needed.team_id AND bot.id = needed.bot_id
CROSS JOIN LATERAL (
  SELECT CASE needed.provider WHEN 'codex' THEN 'Codex' ELSE 'Claude Code' END AS base_name
) name_base
CROSS JOIN LATERAL (
  -- There are N active names and N+1 deterministic candidates, so this always
  -- finds a free name without relying on one hard-coded fallback suffix.
  SELECT candidate
  FROM (
    SELECT candidate_number,
      CASE candidate_number
        WHEN 1 THEN name_base.base_name
        WHEN 2 THEN name_base.base_name || ' (migrated)'
        ELSE name_base.base_name || ' (migrated ' || (candidate_number - 1)::text || ')'
      END AS candidate
    FROM generate_series(
      1::bigint,
      (
        SELECT count(*) + 1
        FROM bot_agents existing
        WHERE existing.team_id = needed.team_id
          AND existing.bot_id = needed.bot_id
          AND existing.deleted_at IS NULL
      )
    ) candidate_numbers(candidate_number)
  ) candidates
  WHERE NOT EXISTS (
    SELECT 1
    FROM bot_agents named
    WHERE named.team_id = needed.team_id
      AND named.bot_id = needed.bot_id
      AND lower(btrim(named.name)) = lower(btrim(candidates.candidate))
      AND named.deleted_at IS NULL
  )
  ORDER BY candidate_number
  LIMIT 1
) selected_name(name)
WHERE NOT EXISTS (
  SELECT 1 FROM bot_agents agent
  WHERE agent.team_id = needed.team_id
    AND agent.bot_id = needed.bot_id
    AND agent.runtime = needed.provider
    AND agent.enabled
    AND agent.deleted_at IS NULL
);

-- 3) Sessions: runtime cutover. The acp_agent_id metadata key stays (inert)
--    for the down migration. Conversation continuity restarts on the direct
--    runtime: codex threads and claude sessions begin fresh on the next turn
--    while the visible Memoh history is preserved.

UPDATE bot_sessions
SET runtime_type = 'codex',
    bot_agent_id = COALESCE(bot_agent_id, (
      SELECT agent.id
      FROM bot_agents agent
      WHERE agent.team_id = bot_sessions.team_id
        AND agent.bot_id = bot_sessions.bot_id
        AND agent.runtime = 'codex'
        AND agent.enabled
        AND agent.deleted_at IS NULL
      ORDER BY agent.created_at, agent.id
      LIMIT 1
    )),
    type = CASE WHEN type = 'acp_agent' THEN 'chat' ELSE type END,
    updated_at = now()
WHERE runtime_type = 'acp_agent'
  AND lower(btrim(COALESCE(runtime_metadata->>'acp_agent_id', metadata->>'acp_agent_id', ''))) = 'codex';

UPDATE bot_sessions
SET runtime_type = 'claude-code',
    bot_agent_id = COALESCE(bot_agent_id, (
      SELECT agent.id
      FROM bot_agents agent
      WHERE agent.team_id = bot_sessions.team_id
        AND agent.bot_id = bot_sessions.bot_id
        AND agent.runtime = 'claude-code'
        AND agent.enabled
        AND agent.deleted_at IS NULL
      ORDER BY agent.created_at, agent.id
      LIMIT 1
    )),
    type = CASE WHEN type = 'acp_agent' THEN 'chat' ELSE type END,
    updated_at = now()
WHERE runtime_type = 'acp_agent'
  AND lower(btrim(COALESCE(runtime_metadata->>'acp_agent_id', metadata->>'acp_agent_id', ''))) = 'claude-code';

UPDATE bot_history_messages history
SET runtime_type = session.runtime_type
FROM bot_sessions session
WHERE history.session_id = session.id
  AND history.team_id = session.team_id
  AND history.runtime_type = 'acp_agent'
  AND session.runtime_type IN ('codex', 'claude-code');

-- 4) Default chat runtime bindings: legacy rows store a direct provider
--    under the acp_agent compatibility shape with no BotAgent binding
--    (chat_runtime='acp_agent', chat_acp_agent_id='codex'/'claude-code',
--    default_bot_agent_id NULL — 0141 deliberately created no rows). Bind the
--    Agent created by the shared source pass above so settings resolve the
--    default through the BotAgent path. Never downgrade a working direct
--    default to the model runtime.

UPDATE bots bot
SET default_bot_agent_id = (
      SELECT agent.id
      FROM bot_agents agent
      WHERE agent.bot_id = bot.id
        AND agent.team_id = bot.team_id
        AND agent.runtime = lower(btrim(bot.chat_acp_agent_id))
        AND agent.enabled
        AND agent.deleted_at IS NULL
      ORDER BY agent.created_at, agent.id
      LIMIT 1
    ),
    updated_at = now()
WHERE bot.default_bot_agent_id IS NULL
  AND bot.chat_runtime = 'acp_agent'
  AND lower(btrim(COALESCE(bot.chat_acp_agent_id, ''))) IN ('codex', 'claude-code')
  AND EXISTS (
    SELECT 1 FROM bot_agents agent
    WHERE agent.bot_id = bot.id
      AND agent.team_id = bot.team_id
      AND agent.runtime = lower(btrim(bot.chat_acp_agent_id))
      AND agent.enabled
      AND agent.deleted_at IS NULL
  );

--    Retire the borrowed shape: a direct default stores its real runtime in
--    chat_runtime, and needs no ACP agent id.
UPDATE bots
SET chat_runtime = lower(btrim(chat_acp_agent_id)),
    chat_acp_agent_id = NULL,
    updated_at = now()
WHERE chat_runtime = 'acp_agent'
  AND lower(btrim(COALESCE(chat_acp_agent_id, ''))) IN ('codex', 'claude-code');

-- 5) Schedules: existing codex/claude-code ACP schedules cut over to the
--    direct runtimes the same way sessions and defaults do. Bind the Agent
--    created by the shared source pass, then retire the ACP fields.
--    acp_model_id stays in place (inert) for the down migration.
--    Pre-0141 rows that violate its NOT VALID execution-shape checks stay in
--    the ACP shape: touching them would re-check the row and abort the upgrade.

UPDATE schedule sched
SET runtime_type = lower(btrim(sched.acp_agent_id)),
    bot_agent_id = COALESCE(sched.bot_agent_id, (
      SELECT agent.id
      FROM bot_agents agent
      WHERE agent.bot_id = sched.bot_id
        AND agent.team_id = sched.team_id
        AND agent.runtime = lower(btrim(sched.acp_agent_id))
        AND agent.enabled
        AND agent.deleted_at IS NULL
      ORDER BY agent.created_at, agent.id
      LIMIT 1
    )),
    acp_agent_id = NULL,
    updated_at = now()
WHERE sched.runtime_type = 'acp_agent'
  AND lower(btrim(COALESCE(sched.acp_agent_id, ''))) IN ('codex', 'claude-code')
  AND sched.run_target = 'new_session'
  AND sched.model_id IS NULL
  AND EXISTS (
    SELECT 1 FROM bot_agents agent
    WHERE agent.bot_id = sched.bot_id
      AND agent.team_id = sched.team_id
      AND agent.runtime = lower(btrim(sched.acp_agent_id))
      AND agent.enabled
      AND agent.deleted_at IS NULL
  );

ALTER TABLE schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule FORCE ROW LEVEL SECURITY;
ALTER TABLE bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE bots FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_agents FORCE ROW LEVEL SECURITY;
