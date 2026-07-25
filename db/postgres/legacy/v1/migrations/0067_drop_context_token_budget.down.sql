-- 0067_drop_context_token_budget (down)
-- Restore the context_token_budget column.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS context_token_budget INTEGER;
