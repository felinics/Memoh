-- 0143_session_queue_append_indexes
-- Remove append position indexes for durable input queues.
DROP INDEX IF EXISTS public.session_follow_up_queue_append_order;
DROP INDEX IF EXISTS public.session_steer_queue_append_order;
