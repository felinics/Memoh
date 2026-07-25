-- 0038_session_type_and_logs
-- Add type column to bot_sessions, session_id to heartbeat logs, and create schedule_logs table.

-- 1) bot_sessions: add type column
ALTER TABLE bot_sessions
  ADD COLUMN IF NOT EXISTS type TEXT NOT NULL DEFAULT 'chat';

-- Add CHECK constraint separately (ADD COLUMN IF NOT EXISTS cannot carry inline CHECK portably).
DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1 FROM pg_constraint WHERE conname = 'bot_sessions_type_check'
  ) THEN
    ALTER TABLE bot_sessions
      ADD CONSTRAINT bot_sessions_type_check CHECK (type IN ('chat', 'heartbeat', 'schedule'));
  END IF;
END$$;

-- 2) bot_heartbeat_logs: add session_id
ALTER TABLE bot_heartbeat_logs
  ADD COLUMN IF NOT EXISTS session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL;

-- 3) schedule_logs: new table
CREATE TABLE IF NOT EXISTS schedule_logs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  schedule_id UUID NOT NULL REFERENCES schedule(id) ON DELETE CASCADE,
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID REFERENCES bot_sessions(id) ON DELETE SET NULL,
  status TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'error')),
  result_text TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  usage JSONB,
  model_id UUID REFERENCES models(id) ON DELETE SET NULL,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_schedule_logs_schedule ON schedule_logs(schedule_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_schedule_logs_bot ON schedule_logs(bot_id, started_at DESC);
