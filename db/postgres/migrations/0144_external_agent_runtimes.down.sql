-- 0144_external_agent_runtimes (down)
-- Restore Codex and Claude Code Bot Agents and sessions to the ACP runtime.
-- Bindings and runtime metadata created after the cutover are intentionally
-- preserved; reused Agent names may require manual reconciliation on rollback.

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

UPDATE bots
SET chat_acp_agent_id = chat_runtime,
    chat_runtime = 'acp_agent',
    updated_at = now()
WHERE chat_runtime IN ('codex', 'claude-code');

ALTER TABLE bots
  DROP CONSTRAINT IF EXISTS bots_chat_runtime_check;
ALTER TABLE bots
  ADD CONSTRAINT bots_chat_runtime_check
  CHECK (chat_runtime IN ('model', 'acp_agent'));

UPDATE bot_agents
SET runtime = 'acp', updated_at = now()
WHERE runtime IN ('codex', 'claude-code');

UPDATE bot_agents
SET enabled = COALESCE((metadata->>'_migration_0144_removed_profile_enabled')::boolean, FALSE),
    deleted_at = NULL,
    metadata = metadata - '_migration_0144_removed_profile_enabled',
    updated_at = now()
WHERE runtime = 'acp'
  AND lower(btrim(COALESCE(metadata->>'provider', ''))) = 'hermes'
  AND metadata ? '_migration_0144_removed_profile_enabled';

UPDATE bot_sessions
SET metadata = jsonb_set(metadata, '{acp_agent_id}', to_jsonb(runtime_type), true),
    runtime_metadata = jsonb_set(runtime_metadata, '{acp_agent_id}', to_jsonb(runtime_type), true),
    updated_at = now()
WHERE runtime_type IN ('codex', 'claude-code')
  AND COALESCE(runtime_metadata->>'acp_agent_id', metadata->>'acp_agent_id', '') = '';

UPDATE bot_sessions
SET runtime_type = 'acp_agent',
    type = CASE WHEN session_mode = 'chat' THEN 'acp_agent' ELSE type END,
    updated_at = now()
WHERE runtime_type IN ('codex', 'claude-code');

UPDATE schedule
SET acp_agent_id = runtime_type,
    runtime_type = 'acp_agent',
    updated_at = now()
WHERE runtime_type IN ('codex', 'claude-code');

UPDATE bot_history_messages
SET runtime_type = 'acp_agent'
WHERE runtime_type IN ('codex', 'claude-code');

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
