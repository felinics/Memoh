-- 0173_history_message_context_order
-- Drop the context-order index of bot_history_messages.

DROP INDEX IF EXISTS public.idx_bot_history_messages_context_order;
