-- 0132_bot_peer_grants
-- Bot-to-bot access grants. bot_user_grants answers "may this person use this
-- bot"; this table answers "may this bot reach this bot", which is a different
-- question with a different subject and a different permission vocabulary.
--
-- Why a second table instead of a third subject_type on bot_user_grants:
--   * bot_user_grants.user_id is a real reference into team_members; a bot
--     subject has to reference bots(team_id, id). One column cannot do both,
--     and a polymorphic subject id would give up the foreign key entirely.
--   * The vocabularies are disjoint. Peer grants must never be able to carry
--     'manage' or any workspace scope: handing bot B exec rights inside bot A's
--     workspace is lateral movement with no human in the loop. The CHECK below
--     makes that unrepresentable rather than merely rejected in Go.
--   * Every existing user-permission query would have to start filtering out
--     bot rows, and each missed filter is a privilege bug.
--
-- The row is a directed edge subject -> bot_id, owned by the callee (bot_id):
-- a grant is the callee consenting to spend its own attention. Both directions
-- are queried -- "who may reach me" and "whom may I reach" -- so the reverse
-- lookup gets its own index.

CREATE TABLE IF NOT EXISTS public.bot_peer_grants (
    id                 UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    team_id            UUID        NOT NULL DEFAULT public.memoh_current_team_id()
                                   REFERENCES public.teams(id) ON DELETE RESTRICT,
    -- Callee: the bot being reached. This grant is part of its configuration.
    bot_id             UUID        NOT NULL,
    subject_type       TEXT        NOT NULL,
    -- Caller. NULL exactly when subject_type = 'any_bot'.
    subject_bot_id     UUID,
    permissions        JSONB       NOT NULL DEFAULT '[]'::jsonb,
    created_by_user_id UUID,
    created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
    CONSTRAINT bot_peer_grants_team_key UNIQUE (team_id, id),
    CONSTRAINT bot_peer_grants_bot_id_fkey
        FOREIGN KEY (team_id, bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    CONSTRAINT bot_peer_grants_subject_bot_id_fkey
        FOREIGN KEY (team_id, subject_bot_id)
        REFERENCES public.bots(team_id, id) ON DELETE CASCADE,
    -- Creator reference follows the platform convention: composite key into
    -- team_members with a column-list SET NULL, so clearing the reference can
    -- never clear the NOT NULL team_id.
    CONSTRAINT bot_peer_grants_created_by_user_id_fkey
        FOREIGN KEY (team_id, created_by_user_id)
        REFERENCES public.team_members(team_id, user_id) ON DELETE SET NULL (created_by_user_id),
    CONSTRAINT bot_peer_grants_subject_type_check
        CHECK (subject_type IN ('bot', 'any_bot')),
    -- A bot subject must name a bot other than the callee; a self-edge would
    -- let a bot authorize contacting itself, which is only ever a loop.
    CONSTRAINT bot_peer_grants_subject_value_check CHECK (
        (subject_type = 'bot' AND subject_bot_id IS NOT NULL AND subject_bot_id <> bot_id)
        OR (subject_type = 'any_bot' AND subject_bot_id IS NULL)
    ),
    -- The peer vocabulary, enforced in the database. jsonb <@ is array
    -- containment, so this admits any subset of the three scopes and nothing
    -- else -- 'manage' and the workspace scopes cannot be stored here at all.
    CONSTRAINT bot_peer_grants_permissions_check CHECK (
        jsonb_typeof(permissions) = 'array'
        AND jsonb_array_length(permissions) > 0
        AND permissions <@ '["discover", "contact", "delegate"]'::jsonb
    )
);

-- One row per directed edge. NULLS NOT DISTINCT folds the single 'any_bot' row
-- into the same uniqueness scope instead of letting it be inserted repeatedly.
ALTER TABLE public.bot_peer_grants
    DROP CONSTRAINT IF EXISTS bot_peer_grants_unique_subject;
ALTER TABLE public.bot_peer_grants
    ADD CONSTRAINT bot_peer_grants_unique_subject
    UNIQUE NULLS NOT DISTINCT (team_id, bot_id, subject_bot_id);

-- "Whom may I reach": the caller-side lookup, driving teammate discovery.
CREATE INDEX IF NOT EXISTS idx_bot_peer_grants_subject
    ON public.bot_peer_grants (team_id, subject_bot_id);

ALTER TABLE public.bot_peer_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_peer_grants FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS bot_peer_grants_team_select ON public.bot_peer_grants;
DROP POLICY IF EXISTS bot_peer_grants_team_insert ON public.bot_peer_grants;
DROP POLICY IF EXISTS bot_peer_grants_team_update ON public.bot_peer_grants;
DROP POLICY IF EXISTS bot_peer_grants_team_delete ON public.bot_peer_grants;

CREATE POLICY bot_peer_grants_team_select ON public.bot_peer_grants
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY bot_peer_grants_team_insert ON public.bot_peer_grants
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_peer_grants_team_update ON public.bot_peer_grants
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY bot_peer_grants_team_delete ON public.bot_peer_grants
    FOR DELETE USING (team_id = public.memoh_current_team_id());
