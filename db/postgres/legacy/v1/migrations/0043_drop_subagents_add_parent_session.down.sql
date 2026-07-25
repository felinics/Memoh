-- 0043_drop_subagents_add_parent_session (rollback)
-- Re-create the subagents table and remove parent_session_id from bot_sessions.

DROP INDEX IF EXISTS idx_bot_sessions_parent;

ALTER TABLE bot_sessions
  DROP COLUMN IF EXISTS parent_session_id;

ALTER TABLE bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_type_check;

ALTER TABLE bot_sessions
  ADD CONSTRAINT bot_sessions_type_check CHECK (type IN ('chat', 'heartbeat', 'schedule'));

CREATE TABLE IF NOT EXISTS subagents (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  name TEXT NOT NULL,
  description TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  deleted BOOLEAN NOT NULL DEFAULT false,
  deleted_at TIMESTAMPTZ,
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  messages JSONB NOT NULL DEFAULT '[]'::jsonb,
  metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
  skills JSONB NOT NULL DEFAULT '[]'::jsonb,
  usage JSONB NOT NULL DEFAULT '{}'::jsonb,
  CONSTRAINT subagents_name_unique UNIQUE (bot_id, name)
);

CREATE INDEX IF NOT EXISTS idx_subagents_bot_id ON subagents(bot_id);
CREATE INDEX IF NOT EXISTS idx_subagents_deleted ON subagents(deleted);
