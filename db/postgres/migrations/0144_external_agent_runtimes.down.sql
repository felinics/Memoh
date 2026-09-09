-- 0144_external_agent_runtimes (down)
-- Remove direct-runtime data before restoring the pre-direct schema. The old
-- built-in ACP Codex, Claude Code, and Hermes profiles are not recreated: this
-- rollback is intentionally destructive for Direct Agents and their sessions.

ALTER TABLE bot_agents NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_agents DISABLE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE schedule NO FORCE ROW LEVEL SECURITY;
ALTER TABLE schedule DISABLE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages DISABLE ROW LEVEL SECURITY;

UPDATE bots bot
SET default_bot_agent_id = NULL,
    chat_runtime = 'model',
    chat_acp_agent_id = NULL,
    updated_at = now()
WHERE bot.chat_runtime IN ('codex', 'claude-code')
   OR EXISTS (
     SELECT 1
     FROM bot_agents agent
     WHERE agent.team_id = bot.team_id
       AND agent.bot_id = bot.id
       AND agent.id = bot.default_bot_agent_id
       AND agent.runtime IN ('codex', 'claude-code')
   );

DELETE FROM schedule sched
WHERE sched.runtime_type IN ('codex', 'claude-code')
   OR EXISTS (
     SELECT 1
     FROM bot_agents agent
     WHERE agent.team_id = sched.team_id
       AND agent.bot_id = sched.bot_id
       AND agent.id = sched.bot_agent_id
       AND agent.runtime IN ('codex', 'claude-code')
   );

DELETE FROM bot_sessions session
WHERE session.runtime_type IN ('codex', 'claude-code')
   OR EXISTS (
     SELECT 1
     FROM bot_agents agent
     WHERE agent.team_id = session.team_id
       AND agent.bot_id = session.bot_id
       AND agent.id = session.bot_agent_id
       AND agent.runtime IN ('codex', 'claude-code')
   );

-- Normally the session delete cascades every direct-runtime history row. The
-- explicit sweep also handles historical or manually repaired rows whose
-- runtime marker no longer matches their parent session.
DELETE FROM bot_history_messages
WHERE runtime_type IN ('codex', 'claude-code');

DELETE FROM bot_agents
WHERE runtime IN ('codex', 'claude-code');

ALTER TABLE bot_history_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule FORCE ROW LEVEL SECURITY;
ALTER TABLE bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE bots FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_agents ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_agents FORCE ROW LEVEL SECURITY;

ALTER TABLE bots
  DROP CONSTRAINT IF EXISTS bots_chat_runtime_check;
ALTER TABLE bots
  ADD CONSTRAINT bots_chat_runtime_check
  CHECK (chat_runtime IN ('model', 'acp_agent'));

ALTER TABLE bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_runtime_type_check;
ALTER TABLE bot_sessions
  ADD CONSTRAINT bot_sessions_runtime_type_check
  CHECK (runtime_type IN ('model', 'acp_agent'));

ALTER TABLE bot_history_messages
  DROP CONSTRAINT IF EXISTS bot_history_messages_runtime_type_check;
ALTER TABLE bot_history_messages
  ADD CONSTRAINT bot_history_messages_runtime_type_check
  CHECK (runtime_type IN ('model', 'acp_agent'));

ALTER TABLE schedule
  DROP CONSTRAINT IF EXISTS schedule_runtime_type_check;
ALTER TABLE schedule
  ADD CONSTRAINT schedule_runtime_type_check
  CHECK (runtime_type IS NULL OR runtime_type IN ('model', 'acp_agent'));

DO $schedule_acp_fields_check$
BEGIN
  ALTER TABLE schedule
    DROP CONSTRAINT IF EXISTS schedule_acp_fields_check;
  ALTER TABLE schedule
    ADD CONSTRAINT schedule_acp_fields_check
    CHECK (
      run_target <> 'new_session'
      OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
      OR (COALESCE(runtime_type, 'model') = 'model' AND bot_agent_id IS NULL AND acp_agent_id IS NULL AND acp_model_id IS NULL)
    ) NOT VALID;

  BEGIN
    ALTER TABLE schedule VALIDATE CONSTRAINT schedule_acp_fields_check;
  EXCEPTION
    WHEN check_violation THEN NULL;
  END;
END
$schedule_acp_fields_check$;
