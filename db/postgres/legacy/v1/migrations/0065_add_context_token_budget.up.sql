-- 0065_add_context_token_budget
-- Add context token budget and tool result persistence settings for large task optimization.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS context_token_budget INTEGER;
ALTER TABLE bots ADD COLUMN IF NOT EXISTS persist_full_tool_results BOOLEAN NOT NULL DEFAULT false;
