-- 0021_add_model_id_tracking
-- Add model_id column to bot_history_messages and bot_heartbeat_logs for per-model usage tracking

ALTER TABLE bot_history_messages
  ADD COLUMN IF NOT EXISTS model_id UUID REFERENCES models(id) ON DELETE SET NULL;

ALTER TABLE bot_heartbeat_logs
  ADD COLUMN IF NOT EXISTS model_id UUID REFERENCES models(id) ON DELETE SET NULL;
