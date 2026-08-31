-- 0143_session_queue_append_indexes
-- Index append position lookups for durable steer and follow-up queues.
CREATE INDEX IF NOT EXISTS session_steer_queue_append_order
    ON public.session_steer_queue(team_id, session_id, position DESC);
CREATE INDEX IF NOT EXISTS session_follow_up_queue_append_order
    ON public.session_follow_up_queue(team_id, session_id, position DESC);
