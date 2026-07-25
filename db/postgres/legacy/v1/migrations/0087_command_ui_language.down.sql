-- 0087_command_ui_language
-- Remove per-bot command-UI language.

ALTER TABLE bots
  DROP COLUMN IF EXISTS command_ui_language;
