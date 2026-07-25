-- 0021_add_model_id_tracking (rollback)
-- Remove model_id column from bot_history_messages and bot_heartbeat_logs

ALTER TABLE bot_heartbeat_logs
  DROP COLUMN IF EXISTS model_id;

ALTER TABLE bot_history_messages
  DROP COLUMN IF EXISTS model_id;
