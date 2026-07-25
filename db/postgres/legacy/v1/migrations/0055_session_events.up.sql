-- 0055_session_events
-- Create bot_session_events table for the DCP pipeline event store.
-- Stores CanonicalEvent JSON for cold-start replay of the pipeline.

CREATE TABLE IF NOT EXISTS bot_session_events (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  bot_id UUID NOT NULL REFERENCES bots(id) ON DELETE CASCADE,
  session_id UUID NOT NULL REFERENCES bot_sessions(id) ON DELETE CASCADE,
  event_kind TEXT NOT NULL CHECK (event_kind IN ('message', 'edit', 'delete', 'service')),
  event_data JSONB NOT NULL,
  external_message_id TEXT,
  sender_channel_identity_id UUID,
  received_at_ms BIGINT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_session_events_session_received
  ON bot_session_events (session_id, received_at_ms);

CREATE UNIQUE INDEX IF NOT EXISTS idx_session_events_dedup
  ON bot_session_events (session_id, event_kind, external_message_id)
  WHERE external_message_id IS NOT NULL AND external_message_id != '';
