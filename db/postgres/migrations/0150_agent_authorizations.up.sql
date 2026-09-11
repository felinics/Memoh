-- 0150_agent_authorizations
-- Temporary encrypted authorization sessions for Bot creation.

CREATE TABLE IF NOT EXISTS public.agent_authorizations (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id UUID NOT NULL DEFAULT public.memoh_current_team_id() REFERENCES public.teams(id) ON DELETE RESTRICT,
    owner_user_id UUID NOT NULL,
    runtime TEXT NOT NULL CHECK (runtime IN ('codex', 'claude-code')),
    auth_kind TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('pending', 'ready', 'claimed')),
    encrypted_payload BYTEA NOT NULL,
    encryption_nonce BYTEA NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL,
    poll_after TIMESTAMPTZ NOT NULL DEFAULT now(),
    claimed_agent_id UUID,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT agent_authorizations_owner_fkey FOREIGN KEY (team_id, owner_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE CASCADE,
    CONSTRAINT agent_authorizations_claim_fkey FOREIGN KEY (team_id, claimed_agent_id)
        REFERENCES public.bot_agents(team_id, id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_agent_authorizations_expiry ON public.agent_authorizations (team_id, expires_at);
ALTER TABLE public.agent_authorizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.agent_authorizations FORCE ROW LEVEL SECURITY;
DROP POLICY IF EXISTS agent_authorizations_team ON public.agent_authorizations;
CREATE POLICY agent_authorizations_team ON public.agent_authorizations
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
