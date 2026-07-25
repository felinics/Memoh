-- 0051_drop_max_context_fields
-- Re-add max_context_load_time and max_context_tokens columns to bots table

ALTER TABLE bots ADD COLUMN IF NOT EXISTS max_context_load_time INTEGER NOT NULL DEFAULT 1440;
ALTER TABLE bots ADD COLUMN IF NOT EXISTS max_context_tokens INTEGER NOT NULL DEFAULT 0;
