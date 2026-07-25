-- 0119_subagent_fork_history
-- Restore the legacy parent-message snapshot column for managed subagents.

DROP INDEX IF EXISTS public.idx_bot_history_messages_subagent_fork_context;

TRUNCATE TABLE public.subagent_configs;

ALTER TABLE public.subagent_configs
    ADD COLUMN IF NOT EXISTS parent_messages JSONB;

ALTER TABLE public.subagent_configs
    DROP CONSTRAINT IF EXISTS subagent_configs_fork_snapshot_check;

ALTER TABLE public.subagent_configs
    ADD CONSTRAINT subagent_configs_fork_snapshot_check CHECK (
        (forked AND parent_messages IS NOT NULL AND jsonb_typeof(parent_messages) = 'array')
        OR (NOT forked AND parent_messages IS NULL)
    );
