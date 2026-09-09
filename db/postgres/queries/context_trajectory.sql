-- name: AppendContextTrajectoryEvent :one
WITH owner AS (
    SELECT s.id FROM bot_sessions AS s
    WHERE s.team_id = public.memoh_current_team_id()
      AND s.bot_id = sqlc.arg(bot_id) AND s.id = sqlc.arg(session_id)
      AND s.runtime_fencing_token = sqlc.arg(runtime_fencing_token)
      AND s.deleted_at IS NULL
    FOR NO KEY UPDATE
), inserted AS (
    INSERT INTO context_trajectory_events AS stored (bot_id, session_id, run_id, capture_id, sequence, event)
    SELECT sqlc.arg(bot_id), owner.id, sqlc.arg(run_id), sqlc.arg(capture_id), sqlc.arg(sequence), sqlc.arg(event)::jsonb
    FROM owner
    ON CONFLICT (team_id, run_id, capture_id, sequence) DO UPDATE
    SET event = stored.event
    WHERE stored.bot_id = EXCLUDED.bot_id AND stored.session_id = EXCLUDED.session_id
      AND stored.event = EXCLUDED.event
    RETURNING sequence
), contents AS (
    INSERT INTO context_trajectory_contents (bot_id, session_id, content_hash, content)
    SELECT sqlc.arg(bot_id)::uuid, sqlc.arg(session_id)::uuid, unnest(sqlc.arg(content_hashes)::text[]), unnest(sqlc.arg(contents)::bytea[])
    FROM inserted
    ON CONFLICT (team_id, bot_id, session_id, content_hash) DO NOTHING
)
SELECT sequence FROM inserted;

-- name: ListContextTrajectoryEvents :many
SELECT id, run_id, sequence, created_at,
       ((event - 'blocks') || jsonb_build_object(
           'block_count', jsonb_array_length(event->'blocks')
       ))::jsonb AS summary
FROM context_trajectory_events
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id) AND session_id = sqlc.arg(session_id)
  AND (sqlc.arg(before_id)::bigint = 0 OR id < sqlc.arg(before_id)::bigint)
ORDER BY id DESC
LIMIT sqlc.arg(row_limit)::int;

-- name: GetContextTrajectoryEvent :one
SELECT event
FROM context_trajectory_events
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id) AND session_id = sqlc.arg(session_id)
  AND id = sqlc.arg(id);

-- name: GetContextTrajectoryEventContents :many
SELECT DISTINCT c.content_hash, c.content
FROM context_trajectory_events AS e
CROSS JOIN LATERAL jsonb_array_elements(e.event->'blocks') AS block
CROSS JOIN LATERAL jsonb_array_elements_text(block->'chunks') AS ref(hash)
JOIN context_trajectory_contents AS c
  ON c.team_id = e.team_id AND c.bot_id = e.bot_id AND c.session_id = e.session_id AND c.content_hash = ref.hash
WHERE e.team_id = public.memoh_current_team_id()
  AND e.bot_id = sqlc.arg(bot_id) AND e.session_id = sqlc.arg(session_id)
  AND e.id = sqlc.arg(id);
