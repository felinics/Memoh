-- 0117_global_title_model
-- Move automatic session title model selection from each bot to the owner's team profile.

-- The migration may run as a non-superuser against FORCE RLS tables. Suspend
-- RLS transactionally so the backfill and foreign-key validation cover every
-- team rather than only the request-scoped team.
ALTER TABLE public.bots NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.bots DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.models NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.models DISABLE ROW LEVEL SECURITY;
ALTER TABLE public.team_members NO FORCE ROW LEVEL SECURITY;
ALTER TABLE public.team_members DISABLE ROW LEVEL SECURITY;

ALTER TABLE public.team_members
    ADD COLUMN IF NOT EXISTS title_model_id UUID;

-- Preserve an existing choice only when all configured bots owned by the same
-- member agree. Conflicting per-bot choices cannot be promoted safely to one
-- global preference, so those profiles remain unset for explicit selection.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1
        FROM information_schema.columns
        WHERE table_schema = 'public'
          AND table_name = 'bots'
          AND column_name = 'title_model_id'
    ) THEN
        EXECUTE $migration$
            WITH owner_title_models AS (
                SELECT
                    team_id,
                    owner_user_id,
                    min(title_model_id::text)::uuid AS title_model_id
                FROM public.bots
                WHERE title_model_id IS NOT NULL
                GROUP BY team_id, owner_user_id
                HAVING count(DISTINCT title_model_id) = 1
            )
            UPDATE public.team_members membership
            SET title_model_id = selected.title_model_id,
                updated_at = now()
            FROM owner_title_models selected
            WHERE membership.team_id = selected.team_id
              AND membership.user_id = selected.owner_user_id
              AND membership.title_model_id IS NULL
        $migration$;
    END IF;
END
$$;

ALTER TABLE public.team_members
    DROP CONSTRAINT IF EXISTS team_members_title_model_id_fkey,
    ADD CONSTRAINT team_members_title_model_id_fkey
        FOREIGN KEY (team_id, title_model_id)
        REFERENCES public.models(team_id, id)
        ON DELETE SET NULL (title_model_id);

ALTER TABLE public.bots
    DROP COLUMN IF EXISTS title_model_id;

-- Append the membership preference so CREATE OR REPLACE remains compatible
-- with the existing view's column order.
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
    tm.title_model_id
FROM public.team_members tm
JOIN public.users u ON u.id = tm.user_id
WHERE tm.team_id = public.memoh_current_team_id();

ALTER TABLE public.bots ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.bots FORCE ROW LEVEL SECURITY;
ALTER TABLE public.models ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.models FORCE ROW LEVEL SECURITY;
ALTER TABLE public.team_members ENABLE ROW LEVEL SECURITY;
ALTER TABLE public.team_members FORCE ROW LEVEL SECURITY;
