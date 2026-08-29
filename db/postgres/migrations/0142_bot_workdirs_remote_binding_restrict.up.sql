-- 0142_bot_workdirs_remote_binding_restrict
-- Keep remote workspace bindings while any live or archived workdir refers to them.

ALTER TABLE public.bot_workdirs
    DROP CONSTRAINT IF EXISTS bot_workdirs_remote_binding_fkey;

-- NOT VALID avoids a validation scan through the referenced table's FORCE RLS
-- policy. Migration connections intentionally have no memoh.team_id and must
-- continue to fail closed; PostgreSQL still enforces this FK for new writes.
ALTER TABLE public.bot_workdirs
    ADD CONSTRAINT bot_workdirs_remote_binding_fkey
    FOREIGN KEY (team_id, remote_binding_id)
    REFERENCES public.bot_remote_runtime_bindings(team_id, id) ON DELETE RESTRICT
    NOT VALID;
