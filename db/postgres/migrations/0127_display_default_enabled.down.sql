-- 0127_display_default_enabled
-- Restore the legacy opt-in workspace desktop default.

ALTER TABLE bots ALTER COLUMN display_enabled SET DEFAULT false;
