-- 0081_add_bot_name
-- Reverse the bot name column and its hardening constraints.

ALTER TABLE bots DROP CONSTRAINT IF EXISTS bots_name_format_check;
DROP INDEX IF EXISTS idx_bots_name;
ALTER TABLE bots DROP COLUMN IF EXISTS name;
