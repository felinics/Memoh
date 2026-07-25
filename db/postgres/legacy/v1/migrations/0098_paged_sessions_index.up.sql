-- 0098_paged_sessions_index
-- Add a composite (bot_id, updated_at DESC, id DESC) partial index on
-- non-deleted rows to back the new paged /sessions endpoint. The previous
-- indexes covered (bot_id) and (bot_id, deleted_at) but did not help when
-- ordering by updated_at DESC, id DESC for keyset pagination.

CREATE INDEX IF NOT EXISTS idx_bot_sessions_bot_active_updated
  ON bot_sessions(bot_id, updated_at DESC, id DESC)
  WHERE deleted_at IS NULL;
