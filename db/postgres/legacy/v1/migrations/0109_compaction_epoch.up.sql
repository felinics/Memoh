-- 0109_compaction_epoch
-- Fence session compaction artifacts when their source history is rewritten.

ALTER TABLE bot_sessions
  ADD COLUMN IF NOT EXISTS compaction_epoch BIGINT NOT NULL DEFAULT 0;

ALTER TABLE bot_history_message_compacts
  ADD COLUMN IF NOT EXISTS compaction_epoch BIGINT NOT NULL DEFAULT 0;

ALTER TABLE bot_history_message_compacts
  DROP CONSTRAINT IF EXISTS bot_history_message_compacts_session_id_fkey;
ALTER TABLE bot_history_message_compacts
  ADD CONSTRAINT bot_history_message_compacts_session_id_fkey
  FOREIGN KEY (session_id) REFERENCES bot_sessions(id) ON DELETE CASCADE;

CREATE INDEX IF NOT EXISTS idx_compacts_owner_epoch
  ON bot_history_message_compacts(bot_id, session_id, compaction_epoch, started_at DESC);

UPDATE bot_sessions session
SET compaction_epoch = session.compaction_epoch + 1
WHERE EXISTS (
  SELECT 1
  FROM bot_history_message_compacts compact
  CROSS JOIN LATERAL (
    SELECT
      count(*) AS linked_count,
      count(*) FILTER (WHERE
        message.bot_id = compact.bot_id
        AND message.session_id = compact.session_id
        AND message.turn_visible = true
        AND message.turn_id IS NOT NULL
        AND message.turn_position IS NOT NULL
        AND message.turn_message_seq IS NOT NULL
        AND message.turn_superseded_at IS NULL
      ) AS valid_count
    FROM bot_history_messages message
    WHERE message.compact_id = compact.id
  ) sources
  WHERE compact.bot_id = session.bot_id
    AND compact.session_id = session.id
    AND compact.compaction_epoch = session.compaction_epoch
    AND compact.status = 'ok'
    AND NULLIF(BTRIM(compact.summary, E' \t\n\r\f\x0B'), '') IS NOT NULL
    AND (
      compact.message_count <= 0
      OR sources.linked_count IS DISTINCT FROM compact.message_count::bigint
      OR sources.valid_count IS DISTINCT FROM compact.message_count::bigint
    )
);
