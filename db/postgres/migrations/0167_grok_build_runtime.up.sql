-- 0167_grok_build_runtime
-- Admit the Grok Build direct runtime ('grok-build') to the runtime vocabulary
-- the CHECK constraints enumerate, as 0144 did for Codex and Claude Code.
-- Each constraint is added NOT VALID and validated afterwards: validation
-- scans the table under a lock that lets reads and writes continue, where a
-- plain ADD CONSTRAINT would hold the table exclusively for the whole scan.
-- Every existing row satisfies the wider enumeration, so validation cannot
-- fail.

ALTER TABLE bots
  DROP CONSTRAINT IF EXISTS bots_chat_runtime_check;
ALTER TABLE bots
  ADD CONSTRAINT bots_chat_runtime_check
  CHECK (chat_runtime IN ('model', 'acp_agent', 'codex', 'claude-code', 'grok-build')) NOT VALID;
ALTER TABLE bots VALIDATE CONSTRAINT bots_chat_runtime_check;

ALTER TABLE bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_runtime_type_check;
ALTER TABLE bot_sessions
  ADD CONSTRAINT bot_sessions_runtime_type_check
  CHECK (runtime_type IN ('model', 'acp_agent', 'codex', 'claude-code', 'grok-build')) NOT VALID;
ALTER TABLE bot_sessions VALIDATE CONSTRAINT bot_sessions_runtime_type_check;

ALTER TABLE bot_history_messages
  DROP CONSTRAINT IF EXISTS bot_history_messages_runtime_type_check;
ALTER TABLE bot_history_messages
  ADD CONSTRAINT bot_history_messages_runtime_type_check
  CHECK (runtime_type IN ('model', 'acp_agent', 'codex', 'claude-code', 'grok-build')) NOT VALID;
ALTER TABLE bot_history_messages VALIDATE CONSTRAINT bot_history_messages_runtime_type_check;

ALTER TABLE schedule
  DROP CONSTRAINT IF EXISTS schedule_runtime_type_check;
ALTER TABLE schedule
  ADD CONSTRAINT schedule_runtime_type_check
  CHECK (runtime_type IS NULL OR runtime_type IN ('model', 'acp_agent', 'codex', 'claude-code', 'grok-build')) NOT VALID;
ALTER TABLE schedule VALIDATE CONSTRAINT schedule_runtime_type_check;

ALTER TABLE agent_authorizations
  DROP CONSTRAINT IF EXISTS agent_authorizations_runtime_check;
ALTER TABLE agent_authorizations
  ADD CONSTRAINT agent_authorizations_runtime_check
  CHECK (runtime IN ('codex', 'claude-code', 'grok-build')) NOT VALID;
ALTER TABLE agent_authorizations VALIDATE CONSTRAINT agent_authorizations_runtime_check;

-- As in 0141 and 0144, rows that predate the execution-shape contract may not
-- satisfy this constraint; they must not block the upgrade, so a failed
-- validation leaves it NOT VALID.
DO $schedule_acp_fields_check$
BEGIN
  ALTER TABLE schedule
    DROP CONSTRAINT IF EXISTS schedule_acp_fields_check;
  ALTER TABLE schedule
    ADD CONSTRAINT schedule_acp_fields_check
    CHECK (
      run_target <> 'new_session'
      OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
      OR (runtime_type IN ('codex', 'claude-code', 'grok-build') AND bot_agent_id IS NOT NULL AND acp_agent_id IS NULL AND model_id IS NULL)
      OR (COALESCE(runtime_type, 'model') = 'model' AND bot_agent_id IS NULL AND acp_agent_id IS NULL AND acp_model_id IS NULL)
    ) NOT VALID;

  BEGIN
    ALTER TABLE schedule VALIDATE CONSTRAINT schedule_acp_fields_check;
  EXCEPTION
    WHEN check_violation THEN NULL;
  END;
END
$schedule_acp_fields_check$;
