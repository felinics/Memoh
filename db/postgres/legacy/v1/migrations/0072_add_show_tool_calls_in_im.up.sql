-- 0072_add_show_tool_calls_in_im
-- Add show_tool_calls_in_im column to bots table to control whether tool call
-- status messages are surfaced in IM channels.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS show_tool_calls_in_im BOOLEAN NOT NULL DEFAULT false;
