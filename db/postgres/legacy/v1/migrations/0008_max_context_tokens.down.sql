-- 0008_max_context_tokens
-- Remove max_context_tokens column from bots table

ALTER TABLE bots DROP COLUMN IF EXISTS max_context_tokens;
