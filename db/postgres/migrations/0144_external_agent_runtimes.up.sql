-- 0144_external_agent_runtimes
-- Add direct Codex and Claude Code runtime vocabulary. The removed built-in
-- ACP profiles are deliberately retired rather than migrated: Direct Agents
-- are new instances and users reconnect them explicitly after upgrading.

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

-- 2) Retire removed built-in ACP profiles ------------------------------------

-- The new direct runtimes do not inherit the identity, sessions, schedules,
-- or credentials of the old built-in ACP profiles. Reset affected Bots to the
-- Native model runtime, remove scheduled execution, archive the old sessions,
-- and tombstone persisted Agent rows. Generic/custom ACP Agents are untouched.
--
-- These tables use FORCE RLS keyed on memoh.team_id, which a migration
-- connection never sets. Lift the policies for the cleanup and restore them
-- before leaving the migration.
ALTER TABLE bot_agents NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_agents DISABLE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE schedule NO FORCE ROW LEVEL SECURITY;
ALTER TABLE schedule DISABLE ROW LEVEL SECURITY;

UPDATE bots bot
SET default_bot_agent_id = NULL,
    chat_runtime = 'model',
    chat_acp_agent_id = NULL,
    updated_at = now()
WHERE (
    bot.chat_runtime = 'acp_agent'
    AND lower(btrim(COALESCE(bot.chat_acp_agent_id, ''))) IN ('codex', 'claude-code', 'hermes')
  )
  OR EXISTS (
    SELECT 1
    FROM bot_agents agent
    WHERE agent.team_id = bot.team_id
      AND agent.bot_id = bot.id
      AND agent.id = bot.default_bot_agent_id
      AND agent.runtime = 'acp'
      AND lower(btrim(COALESCE(agent.metadata->>'provider', ''))) IN ('codex', 'claude-code', 'hermes')
  );

DELETE FROM schedule sched
WHERE (
    sched.runtime_type = 'acp_agent'
    AND lower(btrim(COALESCE(sched.acp_agent_id, ''))) IN ('codex', 'claude-code', 'hermes')
  )
  OR EXISTS (
    SELECT 1
    FROM bot_agents agent
    WHERE agent.team_id = sched.team_id
      AND agent.bot_id = sched.bot_id
      AND agent.id = sched.bot_agent_id
      AND agent.runtime = 'acp'
      AND lower(btrim(COALESCE(agent.metadata->>'provider', ''))) IN ('codex', 'claude-code', 'hermes')
  );

UPDATE bot_sessions session
SET deleted_at = now(),
    updated_at = now(),
    runtime_config_epoch = session.runtime_config_epoch + 1,
    runtime_fencing_token = nextval('session_runtime_fencing_token_seq')::bigint
WHERE session.deleted_at IS NULL
  AND (
    (
      session.runtime_type = 'acp_agent'
      AND lower(btrim(COALESCE(
        session.runtime_metadata->>'acp_agent_id',
        session.metadata->>'acp_agent_id',
        ''
      ))) IN ('codex', 'claude-code', 'hermes')
    )
    OR EXISTS (
      SELECT 1
      FROM bot_agents agent
      WHERE agent.team_id = session.team_id
        AND agent.bot_id = session.bot_id
        AND agent.id = session.bot_agent_id
        AND agent.runtime = 'acp'
        AND lower(btrim(COALESCE(agent.metadata->>'provider', ''))) IN ('codex', 'claude-code', 'hermes')
    )
  );

UPDATE bot_agents
SET enabled = FALSE,
    deleted_at = now(),
    updated_at = now()
WHERE runtime = 'acp'
  AND lower(btrim(COALESCE(metadata->>'provider', ''))) IN ('codex', 'claude-code', 'hermes')
  AND deleted_at IS NULL;

ALTER TABLE schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule FORCE ROW LEVEL SECURITY;
ALTER TABLE bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE bots FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_agents FORCE ROW LEVEL SECURITY;
