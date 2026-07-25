-- 0040_compaction
-- Add context compaction support: compaction logs table, compact_id on messages, bot settings columns.

-- bot_history_message_compacts: stores compaction records and summaries.
CREATE TABLE IF NOT EXISTS bot_history_message_compacts (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'ok', 'error')),
  summary TEXT NOT NULL DEFAULT '',
  message_count INTEGER NOT NULL DEFAULT 0,
  error_message TEXT NOT NULL DEFAULT '',
  usage JSONB,
  model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_compacts_bot_session ON bot_history_message_compacts(bot_id, session_id, started_at DESC);

-- Add compact_id to messages to track which compaction a message belongs to.
ALTER TABLE bot_history_messages ADD COLUMN IF NOT EXISTS compact_id UUID REFERENCES bot_history_message_compacts(id) ON DELETE SET NULL;
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_compact ON bot_history_messages(compact_id);

-- Bot-level compaction settings.
ALTER TABLE bots ADD COLUMN IF NOT EXISTS compaction_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE bots ADD COLUMN IF NOT EXISTS compaction_threshold INTEGER NOT NULL DEFAULT 100000;
ALTER TABLE bots ADD COLUMN IF NOT EXISTS compaction_model_id UUID REFERENCES models(id) ON DELETE SET NULL;
