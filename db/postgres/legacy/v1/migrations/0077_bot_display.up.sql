-- 0077_bot_display
-- Add per-bot workspace display toggle.

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS display_enabled BOOLEAN NOT NULL DEFAULT false;
