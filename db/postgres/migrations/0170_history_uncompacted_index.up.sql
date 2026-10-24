-- 0170_history_uncompacted_index
-- Index the visible rows no compaction has claimed, by session and age. A
-- history load starts at the oldest row its summary does not replace, the
-- session's compaction anchor, and finds it here without walking the rows
-- the summary already stands for. This migration must remain a single
-- statement: golang-migrate then executes CREATE INDEX CONCURRENTLY outside an
-- implicit transaction.

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_bot_history_messages_session_uncompacted
    ON public.bot_history_messages (team_id, session_id, created_at)
    WHERE compact_id IS NULL
      AND turn_visible = true
      AND turn_id IS NOT NULL
      AND turn_position IS NOT NULL
      AND turn_message_seq IS NOT NULL;
