-- 0037_title_model
-- Add title_model_id to bots for automatic session title generation.

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS title_model_id UUID REFERENCES models(id) ON DELETE SET NULL;
