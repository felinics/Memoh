-- 0067_drop_context_token_budget
-- Remove the unused context_token_budget column from bots table.
-- Context trimming now derives the budget from the chat model's context_window.

ALTER TABLE bots DROP COLUMN IF EXISTS context_token_budget;
