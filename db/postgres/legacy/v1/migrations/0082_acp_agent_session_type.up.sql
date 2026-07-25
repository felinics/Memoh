-- 0081_acp_agent_session_type
-- Add acp_agent as a first-class bot session type for dedicated ACP sessions.

ALTER TABLE bot_sessions DROP CONSTRAINT IF EXISTS bot_sessions_type_check;

ALTER TABLE bot_sessions ADD CONSTRAINT bot_sessions_type_check
  CHECK (type IN ('chat', 'heartbeat', 'schedule', 'subagent', 'discuss', 'acp_agent'));
