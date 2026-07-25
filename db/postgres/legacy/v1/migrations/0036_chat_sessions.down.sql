-- 0036_chat_sessions (rollback)
-- Restore route_id and channel_type on messages, drop bot_sessions table.

-- 1) Remove active_session_id from routes
ALTER TABLE bot_channel_routes DROP COLUMN IF EXISTS active_session_id;

-- 2) Re-add route_id and channel_type to messages
ALTER TABLE bot_history_messages
  ADD COLUMN IF NOT EXISTS route_id UUID REFERENCES bot_channel_routes(id) ON DELETE SET NULL;
ALTER TABLE bot_history_messages
  ADD COLUMN IF NOT EXISTS channel_type TEXT;

-- 3) Restore data from sessions
UPDATE bot_history_messages m
SET route_id = s.route_id,
    channel_type = s.channel_type
FROM bot_sessions s
WHERE m.session_id = s.id;

-- 4) Drop new indexes
DROP INDEX IF EXISTS idx_bot_history_messages_session;
DROP INDEX IF EXISTS idx_bot_history_messages_session_source;
DROP INDEX IF EXISTS idx_bot_history_messages_session_reply;

-- 5) Remove session_id from messages
ALTER TABLE bot_history_messages DROP COLUMN IF EXISTS session_id;

-- 6) Restore old indexes
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_route ON bot_history_messages(route_id);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_source_lookup
  ON bot_history_messages(channel_type, source_message_id);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_reply_lookup
  ON bot_history_messages(channel_type, source_reply_to_message_id);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_identity_route_created
  ON bot_history_messages(bot_id, sender_channel_identity_id, route_id, created_at DESC);

-- 7) Drop bot_sessions table
DROP TABLE IF EXISTS bot_sessions;
