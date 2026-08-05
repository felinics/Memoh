-- 0127_display_default_enabled
-- New bots default to the workspace desktop being on, so Browser Use and
-- Computer Use work without a manual toggle. Existing bots keep their stored
-- value (legacy behavior); this changes the column default only.

ALTER TABLE bots ALTER COLUMN display_enabled SET DEFAULT true;
