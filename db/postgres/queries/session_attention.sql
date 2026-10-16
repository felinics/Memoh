-- name: MarkSessionAttention :one
-- Records that a session needs its creator's attention. The WHERE clause is
-- the whole eligibility rule (user-visible web chat sessions with a creator)
-- plus a forward-only guard, so replays of the same run never re-notify: no
-- row back means the session is ineligible or already marked at least as late.
-- updated_at is deliberately left alone.
UPDATE bot_sessions s
SET attention_at = sqlc.arg(at)::timestamptz,
    attention_kind = sqlc.arg(kind)::text
WHERE s.team_id = public.memoh_current_team_id()
  AND s.id = sqlc.arg(id)
  AND s.deleted_at IS NULL
  AND s.visibility = 'user'
  AND s.type IN ('chat', 'acp_agent')
  AND COALESCE(s.channel_type, '') IN ('', 'local')
  AND s.created_by_user_id IS NOT NULL
  AND (s.attention_at IS NULL OR s.attention_at < sqlc.arg(at)::timestamptz)
RETURNING s.id, s.bot_id, s.created_by_user_id, s.title, s.attention_kind, s.attention_at;

-- name: MarkSessionRead :one
-- Moves a user's read cursor forward; an older through never rewinds it.
INSERT INTO session_reads (user_id, session_id, read_at)
VALUES (sqlc.arg(user_id), sqlc.arg(session_id), sqlc.arg(through)::timestamptz)
ON CONFLICT (team_id, user_id, session_id)
DO UPDATE SET read_at = GREATEST(session_reads.read_at, EXCLUDED.read_at)
RETURNING read_at;

-- name: ListUnreadSessionsForUser :many
SELECT s.id AS session_id, s.bot_id, s.title, s.attention_kind, s.attention_at
FROM bot_sessions s
LEFT JOIN session_reads r
  ON r.team_id = s.team_id AND r.session_id = s.id AND r.user_id = sqlc.arg(user_id)
WHERE s.team_id = public.memoh_current_team_id()
  AND s.created_by_user_id = sqlc.arg(user_id)
  AND s.deleted_at IS NULL
  AND s.attention_at IS NOT NULL
  AND s.visibility = 'user'
  AND s.type IN ('chat', 'acp_agent')
  AND COALESCE(s.channel_type, '') IN ('', 'local')
  AND (r.read_at IS NULL OR s.attention_at > r.read_at)
ORDER BY s.attention_at DESC, s.id DESC
LIMIT sqlc.arg(limit_count)::int;

-- name: ListSessionAttentionForUser :many
-- Attention and unread state of the given sessions, for the user who created
-- them. Sessions created by someone else, or never marked, are omitted.
SELECT s.id AS session_id,
       s.attention_kind,
       (r.read_at IS NULL OR s.attention_at > r.read_at)::bool AS unread
FROM bot_sessions s
LEFT JOIN session_reads r
  ON r.team_id = s.team_id AND r.session_id = s.id AND r.user_id = sqlc.arg(user_id)
WHERE s.team_id = public.memoh_current_team_id()
  AND s.id = ANY(sqlc.arg(session_ids)::uuid[])
  AND s.created_by_user_id = sqlc.arg(user_id)
  AND s.attention_at IS NOT NULL;
