-- 0038_session_type_and_logs (rollback)
-- Remove schedule_logs table, session_id from heartbeat logs, and type from bot_sessions.

DROP INDEX IF EXISTS idx_schedule_logs_bot;
DROP INDEX IF EXISTS idx_schedule_logs_schedule;
DROP TABLE IF EXISTS schedule_logs;

ALTER TABLE bot_heartbeat_logs DROP COLUMN IF EXISTS session_id;

ALTER TABLE bot_sessions DROP CONSTRAINT IF EXISTS bot_sessions_type_check;
ALTER TABLE bot_sessions DROP COLUMN IF EXISTS type;
