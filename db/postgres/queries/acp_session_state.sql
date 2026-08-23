-- name: GetACPSessionState :one
-- A staged snapshot becomes readable only after its run owns the session's
-- canonical publication head. Returning the head even when the LEFT JOIN
-- misses lets the adapter distinguish "no ACP history yet" from "history
-- committed but its snapshot is missing".
WITH target_session AS MATERIALIZED (
  SELECT session.team_id, session.id
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
    AND session.bot_id = sqlc.arg(bot_id)
    AND session.runtime_type = 'acp_agent'
    AND session.deleted_at IS NULL
  FOR SHARE
)
SELECT
  publication.run_id AS through_run_id,
  state.through_run_id AS staged_through_run_id,
  COALESCE(state.agent_id, '')::text AS agent_id,
  COALESCE(state.acp_session_id, '')::text AS acp_session_id,
  COALESCE(state.cwd, '')::text AS cwd,
  COALESCE(state.transcript_path, '')::text AS transcript_path,
  COALESCE(state.runtime_fencing_token, 0)::bigint AS runtime_fencing_token,
  COALESCE(state.file_count, 0)::int AS file_count,
  COALESCE(state.record_count, 0)::bigint AS record_count,
  COALESCE(state.file_shapes, '[]'::jsonb) AS file_shapes
FROM acp_session_publications publication
JOIN target_session session
  ON session.team_id = publication.team_id
 AND session.id = publication.session_id
LEFT JOIN acp_session_states state
  ON state.team_id = publication.team_id
 AND state.session_id = publication.session_id
 AND state.through_run_id = publication.run_id
WHERE publication.team_id = public.memoh_current_team_id()
  AND publication.session_id = sqlc.arg(session_id)
  AND publication.checkpoint_reset = false;

-- name: GetACPRuntimeConfigEpoch :one
-- A handle records this pair before its process starts. Bound resolution and
-- every prompt compare it again, so a reset on another server invalidates
-- idle and unbound processes that have no active session_run to route to.
SELECT
  bot.runtime_config_epoch AS bot_runtime_config_epoch,
  COALESCE(session.runtime_config_epoch, 0)::bigint AS session_runtime_config_epoch
FROM bots bot
LEFT JOIN bot_sessions session
  ON session.team_id = bot.team_id
 AND session.bot_id = bot.id
 AND session.id = sqlc.narg(session_id)
 AND session.deleted_at IS NULL
WHERE bot.team_id = public.memoh_current_team_id()
  AND bot.id = sqlc.arg(bot_id)
  AND (
    sqlc.narg(session_id)::uuid IS NULL
    OR session.id IS NOT NULL
  );

-- name: GetACPSessionPublicationHead :one
-- Return the canonical ACP publication head independently from snapshot
-- availability. Warm runtimes use this small watermark before every native
-- prompt so another server instance (or a history clear) cannot leave two
-- process-local conversations advancing from different canonical heads.
SELECT publication.run_id, publication.checkpoint_reset
FROM acp_session_publications publication
JOIN bot_sessions session
  ON session.team_id = publication.team_id
 AND session.id = publication.session_id
WHERE publication.team_id = public.memoh_current_team_id()
  AND publication.session_id = sqlc.arg(session_id)
  AND session.bot_id = sqlc.arg(bot_id)
  AND session.runtime_type = 'acp_agent'
  AND session.deleted_at IS NULL;

-- name: GetACPCanonicalStateShape :one
-- The committed head version's per-file shapes: the authoritative read bound
-- for Load and the append-only baseline for the next staging. Reset heads
-- have no state row, so this returns no rows for them.
SELECT state.through_run_id, state.file_shapes
FROM acp_session_publications publication
JOIN bot_sessions session
  ON session.team_id = publication.team_id
 AND session.id = publication.session_id
 AND session.bot_id = sqlc.arg(bot_id)
JOIN acp_session_states state
  ON state.team_id = publication.team_id
 AND state.session_id = publication.session_id
 AND state.through_run_id = publication.run_id
WHERE publication.team_id = public.memoh_current_team_id()
  AND publication.session_id = sqlc.arg(session_id)
  AND publication.checkpoint_reset = false;

-- name: UpsertACPSessionPublication :execrows
-- Written in the same transaction as the round's canonical messages. The run
-- join proves the run belongs to this tenant+session and still owns the
-- session's current fencing generation, so a stale runtime cannot move the
-- head.
WITH target_session AS MATERIALIZED (
  SELECT session.team_id, session.id, session.runtime_fencing_token
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
    AND session.bot_id = sqlc.arg(bot_id)
    AND session.runtime_type = 'acp_agent'
    AND session.deleted_at IS NULL
),
target_run AS MATERIALIZED (
  SELECT target.team_id, target.id AS session_id, run.run_id
  FROM target_session target
  JOIN session_runs run
    ON run.team_id = target.team_id
   AND run.session_id = target.id
   AND run.bot_id = sqlc.arg(bot_id)
   AND run.run_id = sqlc.arg(run_id)
   AND run.fencing_token = target.runtime_fencing_token
)
INSERT INTO acp_session_publications (team_id, session_id, run_id, checkpoint_reset)
SELECT target.team_id, target.session_id, target.run_id, sqlc.arg(checkpoint_reset)
FROM target_run target
ON CONFLICT (team_id, session_id) DO UPDATE SET
  run_id = EXCLUDED.run_id,
  checkpoint_reset = EXCLUDED.checkpoint_reset,
  updated_at = now();

-- name: DeleteACPSessionPublicationsBySession :execrows
DELETE FROM acp_session_publications publication
WHERE publication.team_id = public.memoh_current_team_id()
  AND publication.session_id = sqlc.arg(session_id);

-- name: ListACPSessionStateLinePage :many
-- Keyset pagination keeps the Go result bounded independently of snapshot
-- size. The cumulative byte guard normally limits a page to max_page_bytes;
-- ordinal 1 guarantees progress when one valid JSONL record is larger than
-- that target (the adapter applies the stricter per-record hard limit).
-- file_bounds is the requested version's shape as a {path: records} object:
-- it is the version-membership filter, hiding lines beyond the version's per-
-- file record counts (a crashed candidate's dangling tail) and files the
-- version never contained.
WITH file_bounds AS MATERIALIZED (
  SELECT bound.key AS file_path, bound.value::bigint AS max_line
  FROM jsonb_each_text(sqlc.arg(file_bounds)::jsonb) AS bound(key, value)
),
candidate_lines AS MATERIALIZED (
  SELECT line.file_path, line.line_number, line.content_bytes
  FROM acp_session_state_lines line
  JOIN file_bounds bound
    ON bound.file_path = line.file_path
   AND line.line_number <= bound.max_line
  JOIN bot_sessions session
    ON session.team_id = line.team_id
   AND session.id = line.session_id
   AND session.bot_id = sqlc.arg(bot_id)
   AND session.runtime_type = 'acp_agent'
   AND session.deleted_at IS NULL
  WHERE line.team_id = public.memoh_current_team_id()
    AND line.session_id = sqlc.arg(session_id)
    AND (
      line.file_path > sqlc.arg(after_file_path)::text
      OR (
        line.file_path = sqlc.arg(after_file_path)::text
        AND line.line_number > sqlc.arg(after_line_number)::bigint
      )
    )
  ORDER BY line.file_path, line.line_number
  LIMIT sqlc.arg(max_results)::int
),
sized_lines AS MATERIALIZED (
  SELECT
    candidate_lines.file_path,
    candidate_lines.line_number,
    row_number() OVER (
      ORDER BY candidate_lines.file_path, candidate_lines.line_number
    ) AS ordinal,
    sum(
      octet_length(candidate_lines.file_path)
      + candidate_lines.content_bytes
      + 32
    ) OVER (
      ORDER BY candidate_lines.file_path, candidate_lines.line_number
    ) AS cumulative_bytes
  FROM candidate_lines
),
selected_lines AS MATERIALIZED (
  SELECT file_path, line_number
  FROM sized_lines
  WHERE ordinal = 1
     OR cumulative_bytes <= sqlc.arg(max_page_bytes)::bigint
)
SELECT line.file_path, line.line_number, line.content
FROM selected_lines selected
JOIN acp_session_state_lines line
  ON line.team_id = public.memoh_current_team_id()
 AND line.session_id = sqlc.arg(session_id)
 AND line.file_path = selected.file_path
 AND line.line_number = selected.line_number
ORDER BY line.file_path, line.line_number;

-- name: UpsertACPSessionState :one
-- This is deliberately a staging write. Canonical history is committed later
-- by the application; GetACPSessionState is the promotion gate. The run join
-- proves that an arbitrary run id cannot be attached to another session and
-- that this process still owns the run's current fencing generation.
WITH target_session AS MATERIALIZED (
  SELECT session.team_id, session.id, session.runtime_fencing_token
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
    AND session.bot_id = sqlc.arg(bot_id)
    AND session.runtime_type = 'acp_agent'
    AND session.deleted_at IS NULL
),
target_run AS MATERIALIZED (
  SELECT target.team_id, target.id AS session_id, target.runtime_fencing_token, run.run_id
  FROM target_session target
  JOIN session_runs run
    ON run.team_id = target.team_id
   AND run.session_id = target.id
   AND run.bot_id = sqlc.arg(bot_id)
   AND run.run_id = sqlc.arg(through_run_id)
   AND run.fencing_token = target.runtime_fencing_token
   AND run.state IN ('running', 'waiting_decision')
)
INSERT INTO acp_session_states (
  team_id,
  session_id,
  through_run_id,
  agent_id,
  acp_session_id,
  cwd,
  transcript_path,
  runtime_fencing_token,
  file_count,
  record_count,
  file_shapes
)
SELECT
  target.team_id,
  target.session_id,
  target.run_id,
  sqlc.arg(agent_id),
  sqlc.arg(acp_session_id),
  sqlc.arg(cwd),
  sqlc.arg(transcript_path),
  target.runtime_fencing_token,
  sqlc.arg(file_count),
  sqlc.arg(record_count),
  sqlc.arg(file_shapes)
FROM target_run target
ON CONFLICT (team_id, session_id, through_run_id) DO UPDATE SET
  agent_id = EXCLUDED.agent_id,
  acp_session_id = EXCLUDED.acp_session_id,
  cwd = EXCLUDED.cwd,
  transcript_path = EXCLUDED.transcript_path,
  runtime_fencing_token = EXCLUDED.runtime_fencing_token,
  file_count = EXCLUDED.file_count,
  record_count = EXCLUDED.record_count,
  file_shapes = EXCLUDED.file_shapes,
  updated_at = now()
WHERE acp_session_states.runtime_fencing_token <= EXCLUDED.runtime_fencing_token
RETURNING acp_session_states.*;

-- name: TrimACPSessionStateLines :execrows
-- Delete one file's lines beyond keep_records. keep_records = 0 clears the
-- file for a full rewrite; a positive value clears a crashed candidate's
-- dangling tail before the next append.
DELETE FROM acp_session_state_lines
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id)
  AND file_path = sqlc.arg(file_path)
  AND line_number > sqlc.arg(keep_records)::bigint;

-- name: DeleteACPSessionStateLineFilesNotIn :execrows
-- Remove files the incoming version no longer contains, including files a
-- crashed candidate introduced that neither the canonical nor the incoming
-- version knows about. kept_paths is a JSONB array of file path strings.
DELETE FROM acp_session_state_lines line
WHERE line.team_id = public.memoh_current_team_id()
  AND line.session_id = sqlc.arg(session_id)
  AND NOT EXISTS (
    SELECT 1
    FROM jsonb_array_elements_text(sqlc.arg(kept_paths)::jsonb) AS kept(path)
    WHERE kept.path = line.file_path
  );

-- name: DeleteACPSessionStateLinesBySession :execrows
DELETE FROM acp_session_state_lines
WHERE team_id = public.memoh_current_team_id()
  AND session_id = sqlc.arg(session_id);

-- name: InsertACPSessionStateLines :one
-- content travels as a JSON string inside the envelope and lands verbatim in
-- the TEXT column, so the stored bytes are exactly the capture's compacted
-- bytes and every digest survives the database round trip.
WITH target_state AS MATERIALIZED (
  SELECT state.team_id, state.session_id, state.through_run_id
  FROM acp_session_states state
  WHERE state.team_id = public.memoh_current_team_id()
    AND state.session_id = sqlc.arg(session_id)
    AND state.through_run_id = sqlc.arg(through_run_id)
),
input_lines AS MATERIALIZED (
  SELECT
    item->>'file_path' AS file_path,
    (item->>'line_number')::bigint AS line_number,
    item->>'content' AS content,
    octet_length(item->>'content') AS content_bytes
  FROM jsonb_array_elements(sqlc.arg(state_lines)::jsonb) AS input(item)
),
inserted_lines AS (
  INSERT INTO acp_session_state_lines (
    team_id,
    session_id,
    file_path,
    line_number,
    content,
    content_bytes
  )
  SELECT
    state.team_id,
    state.session_id,
    line.file_path,
    line.line_number,
    line.content,
    line.content_bytes
  FROM target_state state
  CROSS JOIN input_lines line
  RETURNING content_bytes
)
SELECT
  count(*)::bigint AS rows_written,
  COALESCE(sum(content_bytes::bigint + 1), 0)::bigint AS jsonl_bytes
FROM inserted_lines;

-- name: PruneACPSessionStateVersions :execrows
-- Bound normal storage to the incoming staged candidate plus the current
-- canonical head. A failed candidate can survive only until the next stage;
-- a crash before this statement rolls the entire replacement back.
DELETE FROM acp_session_states state
WHERE state.team_id = public.memoh_current_team_id()
  AND state.session_id = sqlc.arg(session_id)
  AND state.through_run_id <> sqlc.arg(through_run_id)
  AND NOT EXISTS (
    SELECT 1
    FROM acp_session_publications publication
    WHERE publication.team_id = state.team_id
      AND publication.session_id = state.session_id
      AND publication.checkpoint_reset = false
      AND publication.run_id = state.through_run_id
  );

-- name: DeleteACPSessionStatesBySession :execrows
DELETE FROM acp_session_states state
WHERE state.team_id = public.memoh_current_team_id()
  AND state.session_id = sqlc.arg(session_id);

-- name: GetACPRoundOutcome :one
-- Read the terminal ACP outcome for commit-unknown reconciliation after the
-- caller has first waited on the session write lock in a separate statement.
SELECT COALESCE(message.metadata->>'acp_turn_outcome', '')::text AS outcome
FROM bot_history_messages message
JOIN session_runs run
  ON run.team_id = message.team_id
 AND run.run_id = message.run_id
 AND run.bot_id = message.bot_id
 AND run.session_id = message.session_id
 AND run.turn_id = message.turn_id
 AND run.turn_position = message.turn_position
WHERE message.team_id = public.memoh_current_team_id()
  AND message.bot_id = sqlc.arg(bot_id)
  AND message.session_id = sqlc.arg(session_id)
  AND message.run_id = sqlc.arg(run_id)
  AND message.role = 'assistant'
  AND message.runtime_type = 'acp_agent'
  AND message.turn_visible = true
  AND message.metadata->>'acp_turn_outcome' IN ('succeeded', 'failed', 'aborted')
ORDER BY message.turn_message_seq DESC, message.created_at DESC, message.id DESC
LIMIT 1;

-- name: GetACPLeadingUserMessageID :one
-- Reconcile the single eager user row after an uncertain COMMIT. The caller
-- first waits on the session row in a separate statement, so no row here is a
-- definitive rollback rather than an observation racing the old backend.
SELECT message.id
FROM bot_history_messages message
JOIN session_runs run
  ON run.team_id = message.team_id
 AND run.run_id = message.run_id
 AND run.bot_id = message.bot_id
 AND run.session_id = message.session_id
 AND run.turn_id = message.turn_id
 AND run.turn_position = message.turn_position
WHERE message.team_id = public.memoh_current_team_id()
  AND message.bot_id = sqlc.arg(bot_id)
  AND message.session_id = sqlc.arg(session_id)
  AND message.run_id = sqlc.arg(run_id)
  AND message.turn_id = sqlc.arg(turn_id)
  AND message.role = 'user'
  AND message.runtime_type = 'acp_agent'
  AND message.turn_message_seq = 1
ORDER BY message.created_at DESC, message.id DESC
LIMIT 1;

-- name: DeleteACPDecisionProjectionsByRun :execrows
-- Decision cards are temporary stream projections. Delete by their stable
-- run marker as well as by returned IDs so an insert whose COMMIT ack was lost
-- cannot survive the final canonical round as a duplicate assistant message.
DELETE FROM bot_history_messages message
WHERE message.team_id = public.memoh_current_team_id()
  AND message.bot_id = sqlc.arg(bot_id)
  AND message.session_id = sqlc.arg(session_id)
  AND message.run_id = sqlc.arg(run_id)
  AND message.role = 'assistant'
  AND message.runtime_type = 'acp_agent'
  AND message.metadata @> '{"acp_decision_projection": true}'::jsonb;
