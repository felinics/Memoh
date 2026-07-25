-- 0051_drop_max_context_fields
-- Remove max_context_load_time and max_context_tokens columns from bots table

ALTER TABLE bots DROP COLUMN IF EXISTS max_context_load_time;
ALTER TABLE bots DROP COLUMN IF EXISTS max_context_tokens;
