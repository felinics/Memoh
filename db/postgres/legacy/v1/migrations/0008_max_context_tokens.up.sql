-- 0008_max_context_tokens
-- Add max_context_tokens column to bots table for token-based context trimming

ALTER TABLE bots ADD COLUMN IF NOT EXISTS max_context_tokens INTEGER NOT NULL DEFAULT 0;
