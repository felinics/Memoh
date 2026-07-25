-- 0045_bot_timezone
-- Add an optional timezone override to bots.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS timezone TEXT;
