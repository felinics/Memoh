-- 0142_agent_credentials
-- Remove Agent credential storage and the per-Agent credential binding.

DROP INDEX IF EXISTS public.idx_bot_agents_agent_credential;
ALTER TABLE public.bot_agents
    DROP CONSTRAINT IF EXISTS bot_agents_agent_credential_id_fkey;
ALTER TABLE public.bot_agents
    DROP COLUMN IF EXISTS agent_credential_id;

DROP TABLE IF EXISTS public.agent_credentials;
