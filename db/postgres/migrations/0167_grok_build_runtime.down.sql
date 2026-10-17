-- 0167_grok_build_runtime (down)
-- Restore the runtime vocabulary 0144 enumerated. The restored constraints
-- cannot hold a Grok Build row, so those rows are removed first, the same way
-- 0144's rollback removes direct-runtime data: this rollback is intentionally
-- destructive for Grok Build conversations, schedules and authorizations.
-- Their bot_agents rows stay; that table never enumerated runtimes.

-- These tables use FORCE RLS keyed on memoh.team_id, which a migration
-- connection never sets. Lift the policies for the cleanup and restore them
-- before leaving the migration.
ALTER TABLE bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE schedule NO FORCE ROW LEVEL SECURITY;
ALTER TABLE schedule DISABLE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages DISABLE ROW LEVEL SECURITY;
ALTER TABLE agent_authorizations NO FORCE ROW LEVEL SECURITY;
ALTER TABLE agent_authorizations DISABLE ROW LEVEL SECURITY;

-- Staged authorizations expire within 30 minutes; dropping one only makes the
-- user start the connection again.
DELETE FROM agent_authorizations
WHERE runtime = 'grok-build';

UPDATE bots
SET default_bot_agent_id = NULL,
    chat_runtime = 'model',
    chat_acp_agent_id = NULL,
    updated_at = now()
WHERE chat_runtime = 'grok-build';

DELETE FROM schedule
WHERE runtime_type = 'grok-build';

DELETE FROM bot_sessions
WHERE runtime_type = 'grok-build';

-- Deleting a session only clears session_id on its history rows, so they are
-- removed by their own runtime marker.
DELETE FROM bot_history_messages
WHERE runtime_type = 'grok-build';

ALTER TABLE agent_authorizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_authorizations FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_history_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE schedule ENABLE ROW LEVEL SECURITY;
ALTER TABLE schedule FORCE ROW LEVEL SECURITY;
ALTER TABLE bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE bots FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_sessions FORCE ROW LEVEL SECURITY;

ALTER TABLE bots
  DROP CONSTRAINT IF EXISTS bots_chat_runtime_check;
ALTER TABLE bots
  ADD CONSTRAINT bots_chat_runtime_check
  CHECK (chat_runtime IN ('model', 'acp_agent', 'codex', 'claude-code')) NOT VALID;
ALTER TABLE bots VALIDATE CONSTRAINT bots_chat_runtime_check;

ALTER TABLE bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_runtime_type_check;
ALTER TABLE bot_sessions
  ADD CONSTRAINT bot_sessions_runtime_type_check
  CHECK (runtime_type IN ('model', 'acp_agent', 'codex', 'claude-code')) NOT VALID;
ALTER TABLE bot_sessions VALIDATE CONSTRAINT bot_sessions_runtime_type_check;

ALTER TABLE bot_history_messages
  DROP CONSTRAINT IF EXISTS bot_history_messages_runtime_type_check;
ALTER TABLE bot_history_messages
  ADD CONSTRAINT bot_history_messages_runtime_type_check
  CHECK (runtime_type IN ('model', 'acp_agent', 'codex', 'claude-code')) NOT VALID;
ALTER TABLE bot_history_messages VALIDATE CONSTRAINT bot_history_messages_runtime_type_check;

ALTER TABLE schedule
  DROP CONSTRAINT IF EXISTS schedule_runtime_type_check;
ALTER TABLE schedule
  ADD CONSTRAINT schedule_runtime_type_check
  CHECK (runtime_type IS NULL OR runtime_type IN ('model', 'acp_agent', 'codex', 'claude-code')) NOT VALID;
ALTER TABLE schedule VALIDATE CONSTRAINT schedule_runtime_type_check;

ALTER TABLE agent_authorizations
  DROP CONSTRAINT IF EXISTS agent_authorizations_runtime_check;
ALTER TABLE agent_authorizations
  ADD CONSTRAINT agent_authorizations_runtime_check
  CHECK (runtime IN ('codex', 'claude-code')) NOT VALID;
ALTER TABLE agent_authorizations VALIDATE CONSTRAINT agent_authorizations_runtime_check;

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
