-- 0108_compaction_artifacts
-- Persist summary artifact coverage, anchors, and frontier lineage.

ALTER TABLE bot_history_message_compacts
  ADD COLUMN IF NOT EXISTS artifact_version INTEGER NOT NULL DEFAULT 1,
  ADD COLUMN IF NOT EXISTS coverage JSONB NOT NULL DEFAULT '[]'::jsonb,
  ADD COLUMN IF NOT EXISTS anchor_start_ms BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS anchor_end_ms BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS artifact_level INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS parent_ids UUID[] NOT NULL DEFAULT '{}'::uuid[],
  ADD COLUMN IF NOT EXISTS superseded_by UUID REFERENCES bot_history_message_compacts(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS superseded_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_compacts_active_session
  ON bot_history_message_compacts(session_id, anchor_start_ms, started_at)
  WHERE status = 'ok' AND superseded_at IS NULL;
