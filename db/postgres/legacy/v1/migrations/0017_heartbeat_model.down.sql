-- 0017_heartbeat_model (rollback)
-- Remove heartbeat_model_id column from bots.

ALTER TABLE bots DROP COLUMN IF EXISTS heartbeat_model_id;

