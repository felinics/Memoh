-- 0142_bot_workdirs_remote_binding_restrict
-- Restore cascading deletion of workdirs when a remote workspace binding is removed.

ALTER TABLE public.bot_workdirs
    DROP CONSTRAINT IF EXISTS bot_workdirs_remote_binding_fkey;

-- Match the up migration's RLS-safe installation. NOT VALID skips only the
-- historical scan; new writes and cascading deletes remain enforced.
ALTER TABLE public.bot_workdirs
    ADD CONSTRAINT bot_workdirs_remote_binding_fkey
    FOREIGN KEY (team_id, remote_binding_id)
    REFERENCES public.bot_remote_runtime_bindings(team_id, id) ON DELETE CASCADE
    NOT VALID;
