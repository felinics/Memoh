-- 0119_subagent_fork_history
-- Store forked subagent context as scoped invisible history messages.

TRUNCATE TABLE public.subagent_configs;

ALTER TABLE public.subagent_configs
    DROP CONSTRAINT IF EXISTS subagent_configs_fork_snapshot_check;

ALTER TABLE public.subagent_configs
    DROP COLUMN IF EXISTS parent_messages;

CREATE INDEX IF NOT EXISTS idx_bot_history_messages_subagent_fork_context
    ON public.bot_history_messages (session_id, turn_position, turn_message_seq)
    WHERE turn_visible = false
      AND session_mode = 'subagent'
      AND metadata->>'context_scope' = 'subagent_fork';
