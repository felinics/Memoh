-- 0097_model_enable
-- Add a per-model enable flag so users can disable specific models within a
-- provider. Defaults to true so existing rows stay listed in chat pickers.

ALTER TABLE models
  ADD COLUMN IF NOT EXISTS enable BOOLEAN NOT NULL DEFAULT true;
