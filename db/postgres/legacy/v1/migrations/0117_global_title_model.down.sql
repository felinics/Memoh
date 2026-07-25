-- 0117_global_title_model (rollback)
-- Restore per-bot automatic session title model selection.

ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.models NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.models DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.team_members NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.team_members DISABLE ROW LEVEL SECURITY;

ALTER TABLE public.bots
    ADD COLUMN IF NOT EXISTS title_model_id UUID;

ALTER TABLE public.bots
    DROP CONSTRAINT IF EXISTS bots_title_model_id_fkey,
    ADD CONSTRAINT bots_title_model_id_fkey
        FOREIGN KEY (team_id, title_model_id)
        REFERENCES public.models(team_id, id)
        ON DELETE SET NULL (title_model_id);

DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'team_members'
          AND column_name = 'title_model_id'
    ) THEN
        EXECUTE $migration$
            UPDATE public.bots bot
            SET title_model_id = membership.title_model_id,
                updated_at = now()
            FROM public.team_members membership
            WHERE bot.team_id = membership.team_id
              AND bot.owner_user_id = membership.user_id
              AND membership.title_model_id IS NOT NULL
        $migration$;
    END IF;
END
$$;

-- Removing a projected view column requires recreation rather than
-- CREATE OR REPLACE because PostgreSQL does not allow dropping view columns.
DROP VIEW IF EXISTS public.team_accounts;
CREATE VIEW public.team_accounts
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
    tm.updated_at AS membership_updated_at
FROM public.team_members tm
JOIN public.users u ON u.id = tm.user_id
WHERE tm.team_id = public.memoh_current_team_id();

ALTER TABLE public.team_members
    DROP CONSTRAINT IF EXISTS team_members_title_model_id_fkey,
    DROP COLUMN IF EXISTS title_model_id;

ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;
ALTER TABLE public.models ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.models FORCE ROW LEVEL SECURITY;
ALTER TABLE public.team_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.team_members FORCE ROW LEVEL SECURITY;
