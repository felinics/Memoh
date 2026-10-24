-- 0170_history_uncompacted_index
-- Remove the uncompacted history index.

DROP INDEX CONCURRENTLY IF EXISTS public.idx_bot_history_messages_session_uncompacted;
