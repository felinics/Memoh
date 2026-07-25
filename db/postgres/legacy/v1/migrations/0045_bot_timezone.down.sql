-- 0045_bot_timezone
-- Remove the optional timezone override from bots.

ALTER TABLE bots DROP COLUMN IF EXISTS timezone;
