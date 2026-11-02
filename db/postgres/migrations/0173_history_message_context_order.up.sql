-- 0173_history_message_context_order
-- Index a session's context rows (visible, placed in a turn, not a legacy
-- passive_sync row) in turn order, for the byte-budgeted history reads
-- (history load, turn responses, compaction candidates). Its predicate is the
-- context-row filter those reads spell out, so their walks step through the
-- index one context row at a time.
--
-- The build is not CONCURRENTLY: a migration file runs as one transaction,
-- which CREATE INDEX CONCURRENTLY cannot run in. It holds a SHARE lock on
-- bot_history_messages, blocking writes but not reads, while it reads the
-- table once.
CREATE INDEX IF NOT EXISTS idx_bot_history_messages_context_order
  ON public.bot_history_messages (team_id, session_id, turn_position, turn_message_seq, created_at, id)
  WHERE turn_visible = true
    AND turn_id IS NOT NULL
    AND turn_position IS NOT NULL
    AND turn_message_seq IS NOT NULL
    AND (metadata->>'trigger_mode' IS NULL OR metadata->>'trigger_mode' <> 'passive_sync');
