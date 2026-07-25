-- 0027_browser_contexts (rollback)
-- Remove browser_context_id from bots and drop browser_contexts table

ALTER TABLE bots DROP COLUMN IF EXISTS browser_context_id;
DROP TABLE IF EXISTS browser_contexts;
