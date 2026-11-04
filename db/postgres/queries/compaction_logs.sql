-- name: CreateCompactionLog :one
-- A compaction event records each pass here before its summarizer call. The
-- log is pending until the pass commits its summary or fails.
INSERT INTO bot_history_message_compacts (bot_id, session_id, team_id)
SELECT session.bot_id, session.id, session.team_id
FROM bot_sessions session
WHERE session.team_id = public.memoh_current_team_id()
  AND session.id = sqlc.arg(session_id)
  AND session.bot_id = sqlc.arg(bot_id)
RETURNING *;

-- name: CompleteCompactionLog :one
-- Settles a pending pass. A pass that commits does it in the transaction that
-- makes its checkpoint the session's active one; no row comes back for a log
-- that is no longer pending.
UPDATE bot_history_message_compacts
SET status = sqlc.arg(status),
    summary = sqlc.arg(summary),
    message_count = sqlc.arg(message_count),
    error_message = sqlc.arg(error_message),
    usage = sqlc.arg(usage),
    model_id = sqlc.arg(model_id),
    completed_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(id)
  AND status = 'pending'
RETURNING *;

-- name: CopyCompactionSummary :one
-- A fork carries a summary of its source as its own: the text the copy gives
-- it, which names the fork's rows where the source's named the source's, the
-- same model, count and timing, and no usage, so the summarizer call that
-- wrote it is counted once.
INSERT INTO bot_history_message_compacts (
  bot_id, session_id, team_id, status, summary, message_count, model_id, started_at, completed_at
)
SELECT fork.bot_id, fork.id, fork.team_id, 'ok', sqlc.arg(summary)::text, source.message_count,
  source.model_id, source.started_at, source.completed_at
FROM bot_history_message_compacts source
JOIN bot_sessions fork
  ON fork.team_id = public.memoh_current_team_id()
 AND fork.id = sqlc.arg(session_id)
 AND fork.bot_id = source.bot_id
WHERE source.team_id = public.memoh_current_team_id()
  AND source.id = sqlc.arg(source_compact_id)
  AND source.status = 'ok'
RETURNING id;

-- name: ListCompactionLogsByBot :many
SELECT *
FROM bot_history_message_compacts
WHERE team_id = public.memoh_current_team_id() AND bot_id = $1
ORDER BY started_at DESC
LIMIT $2 OFFSET $3;

-- name: CountCompactionLogsByBot :one
SELECT count(*) FROM bot_history_message_compacts WHERE team_id = public.memoh_current_team_id() AND bot_id = $1;

-- name: ReleasePendingCompactionLogsBySession :exec
-- A new runtime owner settles the passes its predecessors left pending: their
-- writes are fenced out, so those passes can never commit.
UPDATE bot_history_message_compacts
SET status = 'error',
    error_message = 'claimant lost runtime ownership',
    completed_at = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND session_id = sqlc.arg(session_id)
  AND status = 'pending';
