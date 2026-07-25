-- 0081_acp_agent_session_type
-- Remove acp_agent from the bot_sessions.type CHECK constraint.

DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM bot_sessions WHERE type = 'acp_agent') THEN
    RAISE EXCEPTION 'cannot remove acp_agent session type while acp_agent bot_sessions exist';
  END IF;
END $$;

ALTER TABLE bot_sessions DROP CONSTRAINT IF EXISTS bot_sessions_type_check;

ALTER TABLE bot_sessions ADD CONSTRAINT bot_sessions_type_check
  CHECK (type IN ('chat', 'heartbeat', 'schedule', 'subagent', 'discuss'));
