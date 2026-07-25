-- 0097_model_enable (down)
-- Drop the per-model enable flag.

ALTER TABLE models
  DROP COLUMN IF EXISTS enable;
