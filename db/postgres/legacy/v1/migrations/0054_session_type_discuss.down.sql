-- 0054_session_type_discuss (rollback)
-- Revert to original CHECK constraint without 'discuss'.

ALTER TABLE bot_sessions DROP CONSTRAINT IF EXISTS bot_sessions_type_check;
ALTER TABLE bot_sessions ADD CONSTRAINT bot_sessions_type_check
  CHECK (type IN ('chat', 'heartbeat', 'schedule', 'subagent'));
