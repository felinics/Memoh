-- 0016_heartbeat (rollback)
-- Remove heartbeat configuration from bots and drop heartbeat log table.

DROP INDEX IF EXISTS idx_heartbeat_logs_bot_started;
DROP TABLE IF EXISTS bot_heartbeat_logs;

ALTER TABLE bots DROP COLUMN IF EXISTS heartbeat_prompt;
ALTER TABLE bots DROP COLUMN IF EXISTS heartbeat_interval;
ALTER TABLE bots DROP COLUMN IF EXISTS heartbeat_enabled;
