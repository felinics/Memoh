-- 0037_title_model (rollback)
-- Remove title_model_id from bots.

ALTER TABLE bots
  DROP COLUMN IF EXISTS title_model_id;
