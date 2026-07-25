-- 0108_compaction_artifacts
-- Remove summary artifact coverage, anchors, and frontier lineage.

DROP INDEX IF EXISTS idx_compacts_active_session;

ALTER TABLE bot_history_message_compacts
  DROP COLUMN IF EXISTS superseded_at,
  DROP COLUMN IF EXISTS superseded_by,
  DROP COLUMN IF EXISTS parent_ids,
  DROP COLUMN IF EXISTS artifact_level,
  DROP COLUMN IF EXISTS anchor_end_ms,
  DROP COLUMN IF EXISTS anchor_start_ms,
  DROP COLUMN IF EXISTS coverage,
  DROP COLUMN IF EXISTS artifact_version;
