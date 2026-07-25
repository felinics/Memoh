-- 0115_team_memberships
-- Restore the pre-membership model when every user still belongs to exactly one team.

DROP TRIGGER IF EXISTS team_members_last_active_admin_guard ON public.team_members;
DROP FUNCTION IF EXISTS public.memoh_guard_last_active_team_admin();

-- Inspect the complete membership set even when the migration owner is a
-- non-superuser subject to FORCE RLS. Transaction rollback restores this state
-- when the fail-closed guard rejects a multi-membership database.
DROP POLICY IF EXISTS team_members_team_delete ON public.team_members;
DROP POLICY IF EXISTS team_members_team_update ON public.team_members;
DROP POLICY IF EXISTS team_members_team_insert ON public.team_members;
DROP POLICY IF EXISTS team_members_team_select ON public.team_members;
ALTER TABLE public.team_members NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.team_members DISABLE ROW LEVEL SECURITY;

DO $membership_rollback_guard$
BEGIN
    IF EXISTS (
        SELECT user_id
          FROM public.team_members
         GROUP BY user_id
        HAVING count(*) <> 1
    ) OR EXISTS (
        SELECT 1
          FROM public.users u
         WHERE NOT EXISTS (
             SELECT 1 FROM public.team_members tm WHERE tm.user_id = u.id
         )
    ) THEN
        RAISE EXCEPTION 'cannot roll back team memberships: every user must have exactly one membership';
    END IF;
END
$membership_rollback_guard$;

DROP VIEW IF EXISTS public.team_accounts;

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

ALTER TABLE public.bot_acl_rules
    DROP CONSTRAINT bot_acl_rules_created_by_user_id_fkey;
ALTER TABLE public.bot_channel_admins
    DROP CONSTRAINT bot_channel_admins_created_by_user_id_fkey;
ALTER TABLE public.bot_history_messages
    DROP CONSTRAINT bot_history_messages_sender_account_user_id_fkey;
ALTER TABLE public.bot_sessions
    DROP CONSTRAINT bot_sessions_created_by_user_id_fkey;
ALTER TABLE public.bot_user_grants
    DROP CONSTRAINT bot_user_grants_created_by_user_id_fkey,
    DROP CONSTRAINT bot_user_grants_user_id_fkey;
ALTER TABLE public.bots
    DROP CONSTRAINT bots_owner_user_id_fkey;
ALTER TABLE public.channel_link_codes
    DROP CONSTRAINT channel_link_codes_user_id_fkey;
ALTER TABLE public.email_providers
    DROP CONSTRAINT email_providers_user_id_fkey;
ALTER TABLE public.user_channel_bindings
    DROP CONSTRAINT user_channel_bindings_user_id_fkey;
ALTER TABLE public.user_channel_identity_bindings
    DROP CONSTRAINT user_channel_identity_bindings_user_id_fkey;
ALTER TABLE public.user_provider_oauth_tokens
    DROP CONSTRAINT user_provider_oauth_tokens_user_id_fkey;
ALTER TABLE public.user_runtimes
    DROP CONSTRAINT user_runtimes_user_id_fkey;

ALTER TABLE public.users
    ADD COLUMN team_id UUID,
    ADD COLUMN role user_role,
    ADD COLUMN data_root TEXT;

UPDATE public.users u
SET team_id = tm.team_id,
    role = tm.role,
    data_root = tm.data_root,
    is_active = u.is_active AND tm.is_active,
    updated_at = GREATEST(u.updated_at, tm.updated_at)
FROM public.team_members tm
WHERE tm.user_id = u.id;

ALTER TABLE public.users
    ALTER COLUMN team_id SET DEFAULT public.memoh_current_team_id(),
    ALTER COLUMN team_id SET NOT NULL,
    ALTER COLUMN role SET DEFAULT 'member',
    ALTER COLUMN role SET NOT NULL;

ALTER TABLE public.users
    DROP CONSTRAINT users_email_unique,
    DROP CONSTRAINT users_username_unique,
    ADD CONSTRAINT users_email_unique UNIQUE (team_id, email),
    ADD CONSTRAINT users_username_unique UNIQUE (team_id, username),
    ADD CONSTRAINT memoh_team_key_018c4edf45ca UNIQUE (team_id, id),
    ADD CONSTRAINT users_team_id_fkey
        FOREIGN KEY (team_id) REFERENCES public.teams(id) ON DELETE RESTRICT;

ALTER TABLE public.bot_acl_rules
    ADD CONSTRAINT bot_acl_rules_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE SET NULL (created_by_user_id);
ALTER TABLE public.bot_channel_admins
    ADD CONSTRAINT bot_channel_admins_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE SET NULL (created_by_user_id);
ALTER TABLE public.bot_history_messages
    ADD CONSTRAINT bot_history_messages_sender_account_user_id_fkey
    FOREIGN KEY (team_id, sender_account_user_id)
    REFERENCES public.users(team_id, id);
ALTER TABLE public.bot_sessions
    ADD CONSTRAINT bot_sessions_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE SET NULL (created_by_user_id);
ALTER TABLE public.bot_user_grants
    ADD CONSTRAINT bot_user_grants_created_by_user_id_fkey
    FOREIGN KEY (team_id, created_by_user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE SET NULL (created_by_user_id),
    ADD CONSTRAINT bot_user_grants_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE CASCADE;
ALTER TABLE public.bots
    ADD CONSTRAINT bots_owner_user_id_fkey
    FOREIGN KEY (team_id, owner_user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE CASCADE;
ALTER TABLE public.channel_link_codes
    ADD CONSTRAINT channel_link_codes_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE CASCADE;
ALTER TABLE public.email_providers
    ADD CONSTRAINT email_providers_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE CASCADE;
ALTER TABLE public.user_channel_bindings
    ADD CONSTRAINT user_channel_bindings_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE CASCADE;
ALTER TABLE public.user_channel_identity_bindings
    ADD CONSTRAINT user_channel_identity_bindings_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE CASCADE;
ALTER TABLE public.user_provider_oauth_tokens
    ADD CONSTRAINT user_provider_oauth_tokens_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.users(team_id, id)
    ON DELETE CASCADE;
ALTER TABLE public.user_runtimes
    ADD CONSTRAINT user_runtimes_user_id_fkey
    FOREIGN KEY (team_id, user_id)
    REFERENCES public.users(team_id, id)
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

CREATE POLICY users_team_select ON public.users
    FOR SELECT USING (team_id = public.memoh_current_team_id());
CREATE POLICY users_team_insert ON public.users
    FOR INSERT WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY users_team_update ON public.users
    FOR UPDATE
    USING (team_id = public.memoh_current_team_id())
    WITH CHECK (team_id = public.memoh_current_team_id());
CREATE POLICY users_team_delete ON public.users
    FOR DELETE USING (team_id = public.memoh_current_team_id());

ALTER TABLE public.users ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.users FORCE ROW LEVEL SECURITY;

DROP TABLE public.team_members;
