-- 0142_agent_credentials
-- Store encrypted Agent credentials separately from Bot/Session configuration.
-- Each Bot Agent instance points at exactly one credential via
-- bot_agents.agent_credential_id; there is no binding table and no default
-- selection. NULL means the Agent is not connected yet.

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

ALTER TABLE public.agent_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agent_credentials FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS agent_credentials_team_select ON public.agent_credentials;
DROP POLICY IF EXISTS agent_credentials_team_insert ON public.agent_credentials;
DROP POLICY IF EXISTS agent_credentials_team_update ON public.agent_credentials;
DROP POLICY IF EXISTS agent_credentials_team_delete ON public.agent_credentials;

CREATE POLICY agent_credentials_team_select ON public.agent_credentials
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY agent_credentials_team_insert ON public.agent_credentials
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY agent_credentials_team_update ON public.agent_credentials
    FOR UPDATE USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY agent_credentials_team_delete ON public.agent_credentials
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- One credential per Bot Agent instance. ON DELETE SET NULL detaches the
-- Agent instead of deleting it when a credential row is removed.
ALTER TABLE public.bot_agents
    ADD COLUMN IF NOT EXISTS agent_credential_id UUID;

ALTER TABLE public.bot_agents
    DROP CONSTRAINT IF EXISTS bot_agents_agent_credential_id_fkey;
ALTER TABLE public.bot_agents
    ADD CONSTRAINT bot_agents_agent_credential_id_fkey
    FOREIGN KEY (team_id, agent_credential_id)
    REFERENCES public.agent_credentials(team_id, id)
    ON DELETE SET NULL (agent_credential_id)
    NOT VALID;

CREATE INDEX IF NOT EXISTS idx_bot_agents_agent_credential
    ON public.bot_agents (team_id, agent_credential_id)
    WHERE agent_credential_id IS NOT NULL;
