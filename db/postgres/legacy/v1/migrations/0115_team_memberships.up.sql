-- 0115_team_memberships
-- Split global user principals from team membership and preserve team-safe references.

-- The migration owner may be a non-superuser. Disable the old FORCE RLS
-- boundary before inspecting every team; this file runs transactionally, so a
-- failed preflight restores the original policies and RLS state.
DROP POLICY IF EXISTS users_team_delete ON public.users;
DROP POLICY IF EXISTS users_team_update ON public.users;
DROP POLICY IF EXISTS users_team_insert ON public.users;
DROP POLICY IF EXISTS users_team_select ON public.users;
ALTER TABLE public.users NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.users DISABLE ROW LEVEL SECURITY;

-- A username or email could briefly have been created once per team after
-- 0112. Such rows cannot be merged automatically without choosing credentials
-- and ownership, so fail before changing constraints.
DO $duplicate_global_identities$
BEGIN
    IF EXISTS (
        SELECT 1 FROM public.users
         WHERE username IS NOT NULL
         GROUP BY username HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot globalize users: duplicate usernames exist across teams';
    END IF;
    IF EXISTS (
        SELECT 1 FROM public.users
         WHERE email IS NOT NULL
         GROUP BY email HAVING count(*) > 1
    ) THEN
        RAISE EXCEPTION 'cannot globalize users: duplicate emails exist across teams';
    END IF;
END
$duplicate_global_identities$;

CREATE TABLE IF NOT EXISTS public.team_members (
    team_id    UUID        NOT NULL DEFAULT public.memoh_current_team_id(),
    user_id    UUID        NOT NULL,
    role       user_role   NOT NULL DEFAULT 'member',
    is_active  BOOLEAN     NOT NULL DEFAULT true,
    data_root  TEXT,
    metadata   JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (team_id, user_id),
    CONSTRAINT team_members_team_id_fkey
        FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT,
    CONSTRAINT team_members_user_id_fkey
        FOREIGN KEY (user_id) REFERENCES public.users(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS team_members_user_id_idx ON public.team_members (user_id);

-- The canonical 0001 already creates this table with FORCE RLS. Temporarily
-- disable it so the replay path can rebuild foreign keys without requiring a
-- request-scoped team GUC. The legacy path reaches the same state with a new,
-- not-yet-protected table.
ALTER TABLE public.team_members NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.team_members DISABLE ROW LEVEL SECURITY;

INSERT INTO public.team_members (
    team_id, user_id, role, is_active, data_root, created_at, updated_at
)
SELECT
    (to_jsonb(u) ->> 'team_id')::uuid,
    u.id,
    (to_jsonb(u) ->> 'role')::user_role,
    u.is_active,
    to_jsonb(u) ->> 'data_root',
    u.created_at,
    u.updated_at
FROM public.users AS u
WHERE to_jsonb(u) ? 'team_id'
ON CONFLICT (team_id, user_id) DO NOTHING;

-- PostgreSQL validates replacement FKs by scanning their child tables. A
-- non-superuser migration owner is subject to FORCE RLS during that internal
-- scan, so temporarily suspend RLS while constraints are rebuilt.
ALTER TABLE public.bot_acl_rules NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_acl_rules DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_channel_admins NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_channel_admins DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_user_grants NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_user_grants DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.channel_link_codes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.channel_link_codes DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.email_providers NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.email_providers DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_identity_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_identity_bindings DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_provider_oauth_tokens NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_provider_oauth_tokens DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes DISABLE ROW LEVEL SECURITY;

-- Team-owned references now target membership rather than requiring the user
-- principal itself to belong to exactly one team.
ALTER TABLE public.bot_acl_rules
    DROP CONSTRAINT IF EXISTS bot_acl_rules_created_by_user_id_fkey;
ALTER TABLE public.bot_channel_admins
    DROP CONSTRAINT IF EXISTS bot_channel_admins_created_by_user_id_fkey;
ALTER TABLE public.bot_history_messages
    DROP CONSTRAINT IF EXISTS bot_history_messages_sender_account_user_id_fkey;
ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_created_by_user_id_fkey;
ALTER TABLE public.bot_user_grants
    DROP CONSTRAINT IF EXISTS bot_user_grants_created_by_user_id_fkey,
    DROP CONSTRAINT IF EXISTS bot_user_grants_user_id_fkey;
ALTER TABLE public.bots
    DROP CONSTRAINT IF EXISTS bots_owner_user_id_fkey;
ALTER TABLE public.channel_link_codes
    DROP CONSTRAINT IF EXISTS channel_link_codes_user_id_fkey;
ALTER TABLE public.email_providers
    DROP CONSTRAINT IF EXISTS email_providers_user_id_fkey;
ALTER TABLE public.user_channel_bindings
    DROP CONSTRAINT IF EXISTS user_channel_bindings_user_id_fkey;
ALTER TABLE public.user_channel_identity_bindings
    DROP CONSTRAINT IF EXISTS user_channel_identity_bindings_user_id_fkey;
ALTER TABLE public.user_provider_oauth_tokens
    DROP CONSTRAINT IF EXISTS user_provider_oauth_tokens_user_id_fkey;
ALTER TABLE public.user_runtimes
    DROP CONSTRAINT IF EXISTS user_runtimes_user_id_fkey;

ALTER TABLE public.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE SET NULL (created_by_user_id);
ALTER TABLE public.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE SET NULL (created_by_user_id);
ALTER TABLE public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_sender_account_user_id_fkey
    FOREIGN KEY (team_id, sender_account_user_id)
    REFERENCES public.team_members(team_id, user_id);
ALTER TABLE public.bot_sessions
    ADD CONSTRAINT bot_sessions_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE SET NULL (created_by_user_id);
ALTER TABLE public.bot_user_grants
    ADD CONSTRAINT bot_user_grants_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE SET NULL (created_by_user_id),
    ADD CONSTRAINT bot_user_grants_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.bots
    ADD CONSTRAINT bots_owner_user_id_fkey
    FOREIGN KEY (team_id, owner_user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.channel_link_codes
    ADD CONSTRAINT channel_link_codes_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.email_providers
    ADD CONSTRAINT email_providers_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.user_channel_bindings
    ADD CONSTRAINT user_channel_bindings_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;
ALTER TABLE public.user_runtimes
    ADD CONSTRAINT user_runtimes_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.team_members(team_id, user_id)
    ON DELETE CASCADE;

ALTER TABLE public.bot_acl_rules ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_acl_rules FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_channel_admins ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_channel_admins FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_history_messages FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_sessions FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bot_user_grants ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bot_user_grants FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;
ALTER TABLE public.channel_link_codes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.channel_link_codes FORCE ROW LEVEL SECURITY;
ALTER TABLE public.email_providers ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.email_providers FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_identity_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_channel_identity_bindings FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_provider_oauth_tokens ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_provider_oauth_tokens FORCE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.user_runtimes FORCE ROW LEVEL SECURITY;

ALTER TABLE public.users
    DROP CONSTRAINT IF EXISTS users_team_id_fkey,
    DROP CONSTRAINT IF EXISTS memoh_team_key_018c4edf45ca,
    DROP CONSTRAINT IF EXISTS users_email_unique,
    DROP CONSTRAINT IF EXISTS users_username_unique;

ALTER TABLE public.users
    DROP COLUMN IF EXISTS team_id,
    DROP COLUMN IF EXISTS role,
    DROP COLUMN IF EXISTS data_root;

ALTER TABLE public.users
    ADD CONSTRAINT users_email_unique UNIQUE (email),
    ADD CONSTRAINT users_username_unique UNIQUE (username);

ALTER TABLE public.team_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.team_members FORCE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS team_members_team_select ON public.team_members;
DROP POLICY IF EXISTS team_members_team_insert ON public.team_members;
DROP POLICY IF EXISTS team_members_team_update ON public.team_members;
DROP POLICY IF EXISTS team_members_team_delete ON public.team_members;
CREATE POLICY team_members_team_select ON public.team_members
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY team_members_team_insert ON public.team_members
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY team_members_team_update ON public.team_members
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY team_members_team_delete ON public.team_members
    FOR DELETE USING (team_id = public.memoh_current_team_id());

-- Preserve the account-shaped query contract while sourcing authorization
-- fields from the current team's membership.
CREATE OR REPLACE VIEW public.team_accounts
WITH (security_invoker = true)
AS
SELECT
    u.id,
    u.username,
    u.email,
    u.password_hash,
    tm.role,
    u.display_name,
    u.avatar_url,
    u.timezone,
    tm.data_root,
    u.last_login_at,
    (u.is_active AND tm.is_active) AS is_active,
    u.metadata,
    u.created_at,
    u.updated_at,
    tm.team_id,
    u.is_active AS principal_is_active,
    tm.is_active AS membership_is_active,
    tm.created_at AS joined_at,
    tm.updated_at AS membership_updated_at,
    -- 0001 is the canonical latest schema and already has this 0117 column
    -- during fresh replay. to_jsonb keeps 0115 compatible with older upgrade
    -- databases where team_members does not have the column yet.
    (to_jsonb(tm) ->> 'title_model_id')::uuid AS title_model_id
FROM public.team_members tm
JOIN public.users u ON u.id = tm.user_id
WHERE tm.team_id = public.memoh_current_team_id();

-- Serialize membership authority changes and keep one active admin per team.
CREATE OR REPLACE FUNCTION public.memoh_guard_last_active_team_admin()
RETURNS trigger
LANGUAGE plpgsql
SECURITY INVOKER
SET search_path = pg_catalog, pg_temp
AS $$
BEGIN
    IF OLD.role <> 'admin'
       OR NOT OLD.is_active
       OR NOT EXISTS (
           SELECT 1
             FROM public.users principal
            WHERE principal.id = OLD.user_id
              AND principal.is_active
       ) THEN
        IF TG_OP = 'DELETE' THEN
            RETURN OLD;
        END IF;
        RETURN NEW;
    END IF;

    IF TG_OP = 'UPDATE' AND NEW.role = 'admin' AND NEW.is_active THEN
        RETURN NEW;
    END IF;

    -- Concurrent demotions/removals for the same team must observe each other.
    PERFORM 1
      FROM public.teams
     WHERE id = OLD.team_id
     FOR UPDATE;

    IF NOT EXISTS (
        SELECT 1
          FROM public.team_members candidate
          JOIN public.users principal
            ON principal.id = candidate.user_id
           AND principal.is_active
         WHERE candidate.team_id = OLD.team_id
           AND candidate.user_id <> OLD.user_id
           AND candidate.role = 'admin'
           AND candidate.is_active
    ) THEN
        RAISE EXCEPTION 'team must retain at least one active admin'
            USING ERRCODE = '23514',
                  CONSTRAINT = 'team_members_last_active_admin';
    END IF;

    IF TG_OP = 'DELETE' THEN
        RETURN OLD;
    END IF;
    RETURN NEW;
END
$$;

DROP TRIGGER IF EXISTS team_members_last_active_admin_guard ON public.team_members;
CREATE TRIGGER team_members_last_active_admin_guard
BEFORE UPDATE OF role, is_active OR DELETE ON public.team_members
FOR EACH ROW
EXECUTE FUNCTION public.memoh_guard_last_active_team_admin();

-- 0001 binds the short-lived fresh-install migration connection to the
-- singleton team while historical migrations replay under FORCE RLS. Clear
-- that bootstrap context at the chain tip so the connection is fail-closed
-- even before golang-migrate releases it.
SELECT set_config('memoh.team_id', '', false);
