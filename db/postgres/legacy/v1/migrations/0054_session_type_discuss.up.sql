-- 0054_session_type_discuss
-- Add 'discuss' to the bot_sessions.type CHECK constraint for the new discuss session mode.

ALTER TABLE bot_sessions DROP CONSTRAINT IF EXISTS bot_sessions_type_check;
ALTER TABLE bot_sessions ADD CONSTRAINT bot_sessions_type_check
  CHECK (type IN ('chat', 'heartbeat', 'schedule', 'subagent', 'discuss'));
