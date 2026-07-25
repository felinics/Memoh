-- 0016_heartbeat
-- Add heartbeat configuration to bots and heartbeat execution log table.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS heartbeat_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE bots ADD COLUMN IF NOT EXISTS heartbeat_interval INTEGER NOT NULL DEFAULT 30;
ALTER TABLE bots ADD COLUMN IF NOT EXISTS heartbeat_prompt TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS bot_heartbeat_logs (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  status TEXT NOT NULL DEFAULT 'ok' CHECK (status IN ('ok', 'alert', 'error')),
  result_text TEXT NOT NULL DEFAULT '',
  error_message TEXT NOT NULL DEFAULT '',
  usage JSONB,
  started_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  completed_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_heartbeat_logs_bot_started ON bot_heartbeat_logs(bot_id, started_at DESC);
