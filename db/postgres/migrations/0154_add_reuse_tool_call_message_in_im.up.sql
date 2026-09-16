-- 0154_add_reuse_tool_call_message_in_im
-- Add reuse_tool_call_message_in_im column to bots table so IM channels that can
-- edit messages fold consecutive tool calls into one live status message.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS reuse_tool_call_message_in_im BOOLEAN NOT NULL DEFAULT false;
