-- 0017_heartbeat_model
-- Add heartbeat_model_id column to bots for independent heartbeat model selection.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS heartbeat_model_id UUID REFERENCES models(id) ON DELETE SET NULL;

