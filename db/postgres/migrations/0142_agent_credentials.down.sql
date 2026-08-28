-- 0142_agent_credentials
-- Remove Agent credential storage and bindings.

ALTER TABLE public.schedule
    DROP CONSTRAINT IF EXISTS schedule_agent_credential_id_fkey;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_acp_fields_check;
ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_existing_session_check;
ALTER TABLE public.schedule
    DROP COLUMN IF EXISTS agent_credential_id;
ALTER TABLE public.schedule ADD CONSTRAINT schedule_existing_session_check
  CHECK (run_target <> 'existing_session' OR (runtime_type IS NULL AND bot_agent_id IS NULL AND acp_agent_id IS NULL AND workdir_id IS NULL));
ALTER TABLE public.schedule ADD CONSTRAINT schedule_acp_fields_check
  CHECK (
    run_target <> 'new_session'
    OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
    OR (COALESCE(runtime_type, 'model') = 'model' AND bot_agent_id IS NULL AND acp_agent_id IS NULL AND acp_model_id IS NULL)
  );

DROP TABLE IF EXISTS public.bot_agent_credentials;
DROP TABLE IF EXISTS public.agent_credentials;
