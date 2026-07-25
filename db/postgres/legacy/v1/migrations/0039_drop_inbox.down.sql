-- 0039_drop_inbox (rollback)
-- Recreate bot_inbox table and max_inbox_items column.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS max_inbox_items INTEGER NOT NULL DEFAULT 50;

CREATE TABLE IF NOT EXISTS bot_inbox (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  source TEXT NOT NULL DEFAULT '',
  header JSONB NOT NULL DEFAULT '{}'::jsonb,
  content TEXT NOT NULL DEFAULT '',
  action TEXT NOT NULL DEFAULT 'notify',
  is_read BOOLEAN NOT NULL DEFAULT FALSE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  read_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_bot_inbox_bot_unread ON bot_inbox(bot_id, created_at DESC) WHERE is_read = FALSE;
CREATE INDEX IF NOT EXISTS idx_bot_inbox_bot_created ON bot_inbox(bot_id, created_at DESC);
