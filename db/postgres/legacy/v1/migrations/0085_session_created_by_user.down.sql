-- 0085_session_created_by_user
-- Remove user ownership tracking from bot sessions.

DROP INDEX IF EXISTS idx_bot_sessions_bot_created_by;
DROP INDEX IF EXISTS idx_bot_sessions_created_by_user_id;

ALTER TABLE bot_sessions
  DROP COLUMN IF EXISTS created_by_user_id;
