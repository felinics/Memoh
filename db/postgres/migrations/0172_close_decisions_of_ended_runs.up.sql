-- 0172_close_decisions_of_ended_runs
-- Close the ask_user requests and tool approvals that are still pending although
-- the run that asked them has ended. A run's owner closes its requests in its
-- finish path, but runs ended any other way kept them: a graceful shutdown on
-- the memory backend ended a parked run as lost, an inline waiter at shutdown
-- likewise, and a reaped run's tool approvals were never closed. Such a request
-- stays answerable in every client, and an answer is acknowledged but never
-- applied because nothing is left to continue the run. The server now closes a
-- run's requests whenever the run ends; this closes the ones already stranded.
--
-- Only requests whose own run is terminal are touched, so a live or parked
-- question is unaffected, and nothing is revived: those runs cannot continue.
-- The values match what the server writes when it closes a request of an ended
-- run.

-- Every table this migration reads or writes is under FORCE row level security
-- keyed on memoh.team_id, which a migration connection never sets. Lift the
-- policy for the data steps and restore it after, as 0165 does.
ALTER TABLE session_runs NO FORCE ROW LEVEL SECURITY;
ALTER TABLE session_runs DISABLE ROW LEVEL SECURITY;
ALTER TABLE user_input_requests NO FORCE ROW LEVEL SECURITY;
ALTER TABLE user_input_requests DISABLE ROW LEVEL SECURITY;
ALTER TABLE tool_approval_requests NO FORCE ROW LEVEL SECURITY;
ALTER TABLE tool_approval_requests DISABLE ROW LEVEL SECURITY;

UPDATE user_input_requests AS request
   SET status = 'canceled',
       result_json = jsonb_build_object(
         'status', 'canceled',
         'reason', 'run_ended',
         'instruction', 'The user canceled this input request. Do not ask the same question again; continue with a reasonable choice from the available context or briefly explain the next step.'
       ),
       responded_at = now(),
       canceled_at = now(),
       updated_at = now()
  FROM session_runs AS run
 WHERE run.team_id = request.team_id
   AND run.run_id = request.run_id
   AND run.state IN ('completed', 'aborted', 'failed', 'lost')
   AND request.status = 'pending';

UPDATE tool_approval_requests AS request
   SET status = 'cancelled',
       decision_reason = 'tool approval cancelled: the turn that requested it ended',
       decided_at = now()
  FROM session_runs AS run
 WHERE run.team_id = request.team_id
   AND run.run_id = request.run_id
   AND run.state IN ('completed', 'aborted', 'failed', 'lost')
   AND request.status = 'pending';

-- Restore every policy in the reverse order of lifting.
ALTER TABLE tool_approval_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE tool_approval_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE user_input_requests ENABLE ROW LEVEL SECURITY;
ALTER TABLE user_input_requests FORCE ROW LEVEL SECURITY;
ALTER TABLE session_runs ENABLE ROW LEVEL SECURITY;
ALTER TABLE session_runs FORCE ROW LEVEL SECURITY;
