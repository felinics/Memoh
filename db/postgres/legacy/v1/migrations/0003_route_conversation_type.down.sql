-- 0003_route_conversation_type (down)
ALTER TABLE bot_channel_routes DROP COLUMN IF EXISTS conversation_type;
