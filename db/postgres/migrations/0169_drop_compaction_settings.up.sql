-- 0167_drop_compaction_settings
-- Drop the per-bot compaction settings. A run now compacts only when its
-- request does not fit, using its own resolved model, so the toggle,
-- threshold, target percent and model override no longer have a consumer.

ALTER TABLE public.bots
  DROP COLUMN IF EXISTS compaction_model_id,
  DROP COLUMN IF EXISTS compaction_target_percent,
  DROP COLUMN IF EXISTS compaction_threshold,
  DROP COLUMN IF EXISTS compaction_enabled;
