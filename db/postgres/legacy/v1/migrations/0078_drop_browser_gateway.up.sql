-- 0078_drop_browser_gateway
-- Remove external browser gateway context bindings from bots.

ALTER TABLE bots DROP COLUMN IF EXISTS browser_context_id;
DROP TABLE IF EXISTS browser_contexts;
