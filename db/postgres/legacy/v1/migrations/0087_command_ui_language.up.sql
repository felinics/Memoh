-- 0087_command_ui_language
-- Add per-bot command-UI language (slash-command interface locale), independent
-- of `language` which controls the chat/agent reply language. 'auto' resolves to
-- the server default (English) at render time.

ALTER TABLE bots
  ADD COLUMN IF NOT EXISTS command_ui_language TEXT NOT NULL DEFAULT 'auto';
