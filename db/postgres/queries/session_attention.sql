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
-- Moves a user's read cursor forward on a session they created; an older
-- through never rewinds it. No row back means the session is not theirs.
INSERT INTO session_reads (user_id, session_id, read_at)
SELECT sqlc.arg(user_id), s.id, sqlc.arg(through)::timestamptz
FROM bot_sessions s
WHERE s.team_id = public.memoh_current_team_id()
  AND s.id = sqlc.arg(session_id)
  AND s.created_by_user_id = sqlc.arg(user_id)
ON CONFLICT (team_id, user_id, session_id)
DO UPDATE SET read_at = GREATEST(session_reads.read_at, EXCLUDED.read_at)
RETURNING read_at;

-- name: MarkSessionsUnread :many
-- The other side of MarkSessionRead: moves the user's unread mark forward on
-- the given sessions they created that can carry unread state at all, and
-- returns them as the unread summary does. Others are skipped silently. A
-- session keeps its own attention kind and time; one that never needed
-- attention shows as "marked" at the moment it was marked.
WITH eligible AS (
  SELECT s.id, s.bot_id, s.title, s.attention_kind, s.attention_at
  FROM bot_sessions s
  WHERE s.team_id = public.memoh_current_team_id()
    AND s.id = ANY(sqlc.arg(session_ids)::uuid[])
    AND s.created_by_user_id = sqlc.arg(user_id)
    AND s.deleted_at IS NULL
    AND s.visibility = 'user'
    AND s.type IN ('chat', 'acp_agent')
    AND COALESCE(s.channel_type, '') IN ('', 'local')
), marked AS (
  INSERT INTO session_reads (user_id, session_id, marked_unread_at)
  SELECT sqlc.arg(user_id), e.id, sqlc.arg(at)::timestamptz FROM eligible e
  ON CONFLICT (team_id, user_id, session_id)
  DO UPDATE SET marked_unread_at = GREATEST(session_reads.marked_unread_at, EXCLUDED.marked_unread_at)
  RETURNING session_id, read_at, marked_unread_at
)
SELECT e.id AS session_id,
       e.bot_id,
       e.title,
       COALESCE(e.attention_kind, 'marked')::text AS attention_kind,
       COALESCE(e.attention_at, m.marked_unread_at)::timestamptz AS attention_at,
       GREATEST(e.attention_at, m.marked_unread_at)::timestamptz AS read_through,
       (m.marked_unread_at > COALESCE(m.read_at, '-infinity'))::bool AS marked
FROM eligible e
JOIN marked m ON m.session_id = e.id;

-- name: ListUnreadSessionsForUser :many
-- attention_at is the time shown and sorted by (the session's own attention,
-- or the mark when it never had any); read_through is what reading it must
-- cover so both the attention and a manual mark clear.
SELECT s.id AS session_id,
       s.bot_id,
       s.title,
       COALESCE(s.attention_kind, 'marked')::text AS attention_kind,
       COALESCE(s.attention_at, r.marked_unread_at)::timestamptz AS attention_at,
       GREATEST(s.attention_at, r.marked_unread_at)::timestamptz AS read_through,
       COALESCE(r.marked_unread_at > COALESCE(r.read_at, '-infinity'), false)::bool AS marked
FROM bot_sessions s
LEFT JOIN session_reads r
  ON r.team_id = s.team_id AND r.session_id = s.id AND r.user_id = sqlc.arg(user_id)
WHERE s.team_id = public.memoh_current_team_id()
  AND s.created_by_user_id = sqlc.arg(user_id)
  AND s.deleted_at IS NULL
  AND s.visibility = 'user'
  AND s.type IN ('chat', 'acp_agent')
  AND COALESCE(s.channel_type, '') IN ('', 'local')
  AND (s.attention_at > COALESCE(r.read_at, '-infinity')
       OR r.marked_unread_at > COALESCE(r.read_at, '-infinity'))
ORDER BY COALESCE(s.attention_at, r.marked_unread_at) DESC, s.id DESC
LIMIT sqlc.arg(limit_count)::int;

-- name: ListSessionAttentionForUser :many
-- Attention and unread state of the given sessions, for the user who created
-- them. Sessions created by someone else, or never attended nor marked, are
-- omitted.
SELECT s.id AS session_id,
       COALESCE(s.attention_kind, 'marked')::text AS attention_kind,
       COALESCE(s.attention_at, r.marked_unread_at)::timestamptz AS attention_at,
       COALESCE(s.attention_at > COALESCE(r.read_at, '-infinity')
                OR r.marked_unread_at > COALESCE(r.read_at, '-infinity'), false)::bool AS unread
FROM bot_sessions s
LEFT JOIN session_reads r
  ON r.team_id = s.team_id AND r.session_id = s.id AND r.user_id = sqlc.arg(user_id)
WHERE s.team_id = public.memoh_current_team_id()
  AND s.id = ANY(sqlc.arg(session_ids)::uuid[])
  AND s.created_by_user_id = sqlc.arg(user_id)
  AND (s.attention_at IS NOT NULL OR r.marked_unread_at IS NOT NULL);
