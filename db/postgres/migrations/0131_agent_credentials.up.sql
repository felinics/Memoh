-- 0131_agent_credentials
-- Store encrypted, reusable Agent credentials separately from Bot/Session
-- configuration and bind them explicitly to compatible Bot Agent profiles.

CREATE TABLE IF NOT EXISTS public.agent_credentials (
    id                   UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id              UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                     REFERENCES public.teams(id) ON DELETE RESTRICT,
    owner_user_id        UUID        NOT NULL,
    provider             TEXT        NOT NULL,
    auth_kind            TEXT        NOT NULL,
    label                TEXT        NOT NULL,
    encrypted_payload    BYTEA       NOT NULL,
    encryption_nonce     BYTEA       NOT NULL,
    key_version          INTEGER     NOT NULL DEFAULT 1 CHECK (key_version > 0),
    account_metadata     JSONB       NOT NULL DEFAULT '{}'::jsonb,
    expires_at           TIMESTAMPTZ,
    credential_version   BIGINT      NOT NULL DEFAULT 1 CHECK (credential_version > 0),
    revoked_at           TIMESTAMPTZ,
    created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_credentials_team_key UNIQUE (team_id, id),
    CONSTRAINT agent_credentials_owner_fkey
        FOREIGN KEY (team_id, owner_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE RESTRICT,
    CONSTRAINT agent_credentials_provider_check CHECK (provider <> ''),
    CONSTRAINT agent_credentials_auth_kind_check CHECK (auth_kind <> ''),
    CONSTRAINT agent_credentials_label_check CHECK (label <> ''),
    CONSTRAINT agent_credentials_ciphertext_check CHECK (octet_length(encrypted_payload) > 0),
    CONSTRAINT agent_credentials_nonce_check CHECK (octet_length(encryption_nonce) = 12)
);

CREATE INDEX IF NOT EXISTS idx_agent_credentials_owner
    ON public.agent_credentials (team_id, owner_user_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_agent_credentials_kind
    ON public.agent_credentials (team_id, provider, auth_kind)
    WHERE revoked_at IS NULL;

CREATE TABLE IF NOT EXISTS public.bot_agent_credentials (
    team_id       UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                              REFERENCES public.teams(id) ON DELETE RESTRICT,
    bot_id        UUID        NOT NULL,
    agent_id      TEXT        NOT NULL,
    credential_id UUID        NOT NULL,
    is_default    BOOLEAN     NOT NULL DEFAULT false,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT bot_agent_credentials_pkey PRIMARY KEY (team_id, bot_id, agent_id, credential_id),
    CONSTRAINT bot_agent_credentials_agent_id_check CHECK (agent_id <> ''),
    CONSTRAINT bot_agent_credentials_bot_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_agent_credentials_credential_fkey
        FOREIGN KEY (team_id, credential_id)
        REFERENCES public.agent_credentials(team_id, id) ON DELETE CASCADE
);

CREATE UNIQUE INDEX IF NOT EXISTS bot_agent_credentials_one_default
    ON public.bot_agent_credentials (team_id, bot_id, agent_id)
    WHERE is_default;
CREATE INDEX IF NOT EXISTS idx_bot_agent_credentials_credential
    ON public.bot_agent_credentials (team_id, credential_id);

ALTER TABLE public.agent_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agent_credentials FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_agent_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_agent_credentials FORCE ROW LEVEL SECURITY;

CREATE POLICY agent_credentials_team_select ON public.agent_credentials
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY agent_credentials_team_insert ON public.agent_credentials
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY agent_credentials_team_update ON public.agent_credentials
    FOR UPDATE USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY agent_credentials_team_delete ON public.agent_credentials
    FOR DELETE USING (team_id = public.memoh_current_team_id());

CREATE POLICY bot_agent_credentials_team_select ON public.bot_agent_credentials
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_agent_credentials_team_insert ON public.bot_agent_credentials
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_agent_credentials_team_update ON public.bot_agent_credentials
    FOR UPDATE USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_agent_credentials_team_delete ON public.bot_agent_credentials
    FOR DELETE USING (team_id = public.memoh_current_team_id());

ALTER TABLE public.schedule
    ADD COLUMN IF NOT EXISTS agent_credential_id UUID;

ALTER TABLE public.schedule
    ADD CONSTRAINT schedule_agent_credential_id_fkey
    FOREIGN KEY (team_id, agent_credential_id)
    REFERENCES public.agent_credentials(team_id, id) ON DELETE SET NULL (agent_credential_id)
    NOT VALID;

ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_existing_session_check;
ALTER TABLE public.schedule ADD CONSTRAINT schedule_existing_session_check
  CHECK (
    run_target <> 'existing_session'
    OR (runtime_type IS NULL AND acp_agent_id IS NULL AND agent_credential_id IS NULL AND workdir_id IS NULL)
  );

ALTER TABLE public.schedule DROP CONSTRAINT IF EXISTS schedule_acp_fields_check;
ALTER TABLE public.schedule ADD CONSTRAINT schedule_acp_fields_check
  CHECK (
    run_target <> 'new_session'
    OR (runtime_type = 'acp_agent' AND acp_agent_id IS NOT NULL AND model_id IS NULL)
    OR (COALESCE(runtime_type, 'model') = 'model' AND acp_agent_id IS NULL AND agent_credential_id IS NULL AND acp_model_id IS NULL)
  );
