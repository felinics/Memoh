-- 0142_session_input_queues (down)
ALTER TABLE public.session_runs DROP CONSTRAINT IF EXISTS session_runs_source_follow_up_item_fkey;
DROP INDEX IF EXISTS public.session_runs_source_follow_up_unique;
ALTER TABLE public.session_runs DROP COLUMN IF EXISTS source_follow_up_item_id;
DROP TABLE IF EXISTS public.session_queue_step_commits CASCADE;
DROP INDEX IF EXISTS public.session_follow_up_queue_team_item_unique;
DROP INDEX IF EXISTS public.session_steer_queue_team_item_unique;
DROP TABLE IF EXISTS public.session_follow_up_queue CASCADE;
DROP TABLE IF EXISTS public.session_steer_queue CASCADE;
