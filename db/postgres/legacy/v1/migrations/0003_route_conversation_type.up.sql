-- 0003_route_conversation_type
-- Add conversation_type column to bot_channel_routes for conversation context.
ALTER TABLE bot_channel_routes ADD COLUMN IF NOT EXISTS conversation_type TEXT;
