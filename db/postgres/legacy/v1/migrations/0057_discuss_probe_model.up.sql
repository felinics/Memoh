-- 0057_discuss_probe_model
-- Add discuss_probe_model_id column to bots table for probe gate configuration.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS discuss_probe_model_id UUID REFERENCES models(id) ON DELETE SET NULL;
