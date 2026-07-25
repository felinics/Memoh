-- 0043_drop_subagents_add_parent_session
-- Drop the subagents table (no longer needed) and add parent_session_id to bot_sessions for subagent session tracking.

DROP TABLE IF EXISTS subagents;

ALTER TABLE bot_sessions
  DROP CONSTRAINT IF EXISTS bot_sessions_type_check;

ALTER TABLE bot_sessions
  ADD CONSTRAINT bot_sessions_type_check CHECK (type IN ('chat', 'heartbeat', 'schedule', 'subagent'));

ALTER TABLE bot_sessions
  ADD COLUMN IF NOT EXISTS parent_session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_bot_sessions_parent ON bot_sessions(parent_session_id) WHERE parent_session_id IS NOT NULL;
