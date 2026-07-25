-- 0077_bot_display
-- Remove per-bot workspace display toggle.

ALTER TABLE bots
  DROP COLUMN IF EXISTS display_enabled;
