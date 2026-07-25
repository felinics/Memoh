-- 0036_chat_sessions
-- Introduce bot_sessions: multi-session chat support per bot.
-- Messages move from (route_id, channel_type) to session_id.
-- Routes gain an active_session_id pointer.

-- 1) Create bot_sessions table
CREATE TABLE IF NOT EXISTS bot_sessions (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  route_id UUID REFERENCES bot_channel_routes(id) ON DELETE SET NULL,
  channel_type TEXT,
  title TEXT NOT NULL DEFAULT '',
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_id ON bot_sessions(bot_id);
CREATE INDEX IF NOT EXISTS idx_bot_sessions_route_id ON bot_sessions(route_id);
CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_active ON bot_sessions(bot_id, deleted_at);

-- 2) Add session_id column to messages (nullable for migration)
ALTER TABLE bot_history_messages
  ADD COLUMN IF NOT EXISTS session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL;

-- 3-7) Data migration: only needed when upgrading from old schema that has route_id.
-- On fresh databases 0001_init.up.sql already reflects the final state, so skip.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'bot_history_messages' AND column_name = 'route_id'
  ) THEN
    -- 3) Create sessions from existing routed messages
    INSERT INTO bot_sessions (bot_id, route_id, channel_type, title, created_at, updated_at)
    SELECT DISTINCT
      m.bot_id,
      m.route_id,
      COALESCE(r.channel_type, m.channel_type),
      COALESCE(r.metadata->>'conversation_name', ''),
      COALESCE(MIN(m.created_at), now()),
      COALESCE(MAX(m.created_at), now())
    FROM bot_history_messages m
    LEFT JOIN bot_channel_routes r ON r.id = m.route_id
    WHERE m.route_id IS NOT NULL
    GROUP BY m.bot_id, m.route_id, COALESCE(r.channel_type, m.channel_type), COALESCE(r.metadata->>'conversation_name', '');

    -- 4) Create default sessions for messages without route_id (web/cli)
    INSERT INTO bot_sessions (bot_id, route_id, channel_type, title, created_at, updated_at)
    SELECT DISTINCT
      m.bot_id,
      NULL::UUID,
      NULL::TEXT,
      '',
      COALESCE(MIN(m.created_at), now()),
      COALESCE(MAX(m.created_at), now())
    FROM bot_history_messages m
    WHERE m.route_id IS NULL
      AND NOT EXISTS (
        SELECT 1 FROM bot_sessions s
        WHERE s.bot_id = m.bot_id AND s.route_id IS NULL AND s.channel_type IS NULL
      )
    GROUP BY m.bot_id;

    -- 5) Assign session_id to routed messages
    UPDATE bot_history_messages m
    SET session_id = s.id
    FROM bot_sessions s
    WHERE m.route_id IS NOT NULL
      AND s.bot_id = m.bot_id
      AND s.route_id = m.route_id;

    -- 6) Assign session_id to non-routed messages
    UPDATE bot_history_messages m
    SET session_id = s.id
    FROM bot_sessions s
    WHERE m.route_id IS NULL
      AND m.session_id IS NULL
      AND s.bot_id = m.bot_id
      AND s.route_id IS NULL
      AND s.channel_type IS NULL;

    -- 7) Drop old columns and indexes
    DROP INDEX IF EXISTS idx_bot_history_messages_route;
    DROP INDEX IF EXISTS idx_bot_history_messages_source_lookup;
    DROP INDEX IF EXISTS idx_bot_history_messages_reply_lookup;
    DROP INDEX IF EXISTS idx_bot_history_messages_identity_route_created;

    ALTER TABLE bot_history_messages DROP COLUMN IF EXISTS route_id;
    ALTER TABLE bot_history_messages DROP COLUMN IF EXISTS channel_type;
  END IF;
END $$;

-- 8) Create new indexes on messages (idempotent)
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_session
  ON bot_history_messages(session_id, created_at);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_session_source
  ON bot_history_messages(session_id, source_message_id);
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_session_reply
  ON bot_history_messages(session_id, source_reply_to_message_id);

-- 9) Add active_session_id to routes
ALTER TABLE bot_channel_routes
  ADD COLUMN IF NOT EXISTS active_session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL;

-- 10) Set active_session_id for existing routes
UPDATE bot_channel_routes r
SET active_session_id = s.id
FROM bot_sessions s
WHERE s.route_id = r.id
  AND s.deleted_at IS NULL;
