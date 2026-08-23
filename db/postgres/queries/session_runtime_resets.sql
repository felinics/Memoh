-- name: LockBotForRuntimeReset :one
-- This is intentionally a separate statement from acquisition. Under READ
-- COMMITTED, a contender that waited here gets a fresh statement snapshot for
-- the child/gate check below after the parent owner commits.
SELECT id
FROM bots
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(bot_id)
FOR UPDATE;

-- name: ValidateLockedBotRuntimeReset :one
SELECT id
FROM bots
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(bot_id)
  AND runtime_reset_token = sqlc.arg(reset_token)
  AND runtime_reset_expires_at > clock_timestamp();

-- name: ValidateLockedBotSessionRuntimeReset :one
SELECT session.id
FROM bot_sessions session
WHERE session.team_id = public.memoh_current_team_id()
  AND session.bot_id = sqlc.arg(bot_id)
  AND session.id = sqlc.arg(session_id)
  AND session.runtime_reset_token = sqlc.arg(reset_token)
  AND session.runtime_reset_expires_at > clock_timestamp()
FOR UPDATE OF session;

-- name: RefreshLockedBotRuntimeReset :one
-- The caller holds the bot parent lock and validated this token before a
-- potentially long protected mutation. Token-only CAS may refresh an expiry
-- that elapsed while that same transaction prevented every successor.
UPDATE bots
SET runtime_reset_expires_at = clock_timestamp()
      + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(bot_id)
  AND runtime_reset_token = sqlc.arg(reset_token)
RETURNING runtime_reset_expires_at;

-- name: RefreshLockedBotSessionRuntimeReset :one
UPDATE bot_sessions
SET runtime_reset_expires_at = clock_timestamp()
      + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(session_id)
  AND bot_id = sqlc.arg(bot_id)
  AND runtime_reset_token = sqlc.arg(reset_token)
RETURNING runtime_reset_expires_at;

-- name: AcquireLockedBotSessionRuntimeReset :one
UPDATE bot_sessions session
SET runtime_reset_token = sqlc.arg(reset_token),
    runtime_reset_expires_at = clock_timestamp()
      + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'
WHERE session.team_id = public.memoh_current_team_id()
  AND session.id = sqlc.arg(session_id)
  AND session.bot_id = sqlc.arg(bot_id)
  AND session.deleted_at IS NULL
  AND NOT EXISTS (
    SELECT 1
    FROM bots bot
    WHERE bot.team_id = public.memoh_current_team_id()
      AND bot.id = sqlc.arg(bot_id)
      AND bot.runtime_reset_expires_at > clock_timestamp()
  )
  AND (
    session.runtime_reset_token = sqlc.arg(reset_token)
    OR session.runtime_reset_expires_at IS NULL
    OR session.runtime_reset_expires_at <= clock_timestamp()
  )
RETURNING session.runtime_reset_expires_at;

-- name: AcquireLockedBotRuntimeReset :one
UPDATE bots bot
SET runtime_reset_token = sqlc.arg(reset_token),
    runtime_reset_expires_at = clock_timestamp()
      + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'
WHERE bot.team_id = public.memoh_current_team_id()
  AND bot.id = sqlc.arg(bot_id)
  AND (
    bot.runtime_reset_token = sqlc.arg(reset_token)
    OR bot.runtime_reset_expires_at IS NULL
    OR bot.runtime_reset_expires_at <= clock_timestamp()
  )
  AND NOT EXISTS (
    SELECT 1
    FROM bot_sessions session
    WHERE session.team_id = public.memoh_current_team_id()
      AND session.bot_id = bot.id
      AND session.runtime_reset_expires_at > clock_timestamp()
  )
RETURNING runtime_reset_expires_at;

-- name: RenewBotSessionRuntimeReset :one
UPDATE bot_sessions target
SET runtime_reset_expires_at = clock_timestamp()
      + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'
WHERE target.team_id = public.memoh_current_team_id()
  AND target.id = sqlc.arg(session_id)
  AND target.bot_id = sqlc.arg(bot_id)
  AND target.runtime_reset_token = sqlc.arg(reset_token)
  AND target.runtime_reset_expires_at > clock_timestamp()
  AND NOT EXISTS (
    SELECT 1
    FROM bots bot
    WHERE bot.team_id = public.memoh_current_team_id()
      AND bot.id = sqlc.arg(bot_id)
      AND bot.runtime_reset_expires_at > clock_timestamp()
  )
RETURNING target.runtime_reset_expires_at;

-- name: RenewBotRuntimeReset :one
UPDATE bots target
SET runtime_reset_expires_at = clock_timestamp()
      + sqlc.arg(lease_milliseconds)::bigint * interval '1 millisecond'
WHERE target.team_id = public.memoh_current_team_id()
  AND target.id = sqlc.arg(bot_id)
  AND target.runtime_reset_token = sqlc.arg(reset_token)
  AND target.runtime_reset_expires_at > clock_timestamp()
  AND NOT EXISTS (
    SELECT 1
    FROM bot_sessions session
    WHERE session.team_id = public.memoh_current_team_id()
      AND session.bot_id = sqlc.arg(bot_id)
      AND session.runtime_reset_expires_at > clock_timestamp()
  )
RETURNING target.runtime_reset_expires_at;

-- name: ReleaseBotSessionRuntimeReset :one
UPDATE bot_sessions
SET runtime_reset_token = NULL,
    runtime_reset_expires_at = NULL
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(session_id)
  AND bot_id = sqlc.arg(bot_id)
  AND runtime_reset_token = sqlc.arg(reset_token)
RETURNING id;

-- name: ReleaseBotRuntimeReset :one
UPDATE bots
SET runtime_reset_token = NULL,
    runtime_reset_expires_at = NULL
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(bot_id)
  AND runtime_reset_token = sqlc.arg(reset_token)
RETURNING id;

-- name: GetEffectiveSessionRuntimeReset :one
SELECT reset.scope, reset.reset_token, reset.runtime_reset_expires_at
FROM (
  SELECT
    'bot'::text AS scope,
    bot.runtime_reset_token AS reset_token,
    bot.runtime_reset_expires_at
  FROM bots bot
  WHERE bot.team_id = public.memoh_current_team_id()
    AND bot.id = sqlc.arg(bot_id)
    AND bot.runtime_reset_token IS NOT NULL
    AND bot.runtime_reset_expires_at > clock_timestamp()
  UNION ALL
  SELECT
    'session'::text AS scope,
    session.runtime_reset_token AS reset_token,
    session.runtime_reset_expires_at
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.bot_id = sqlc.arg(bot_id)
    AND session.id = sqlc.arg(session_id)
    AND session.runtime_reset_token IS NOT NULL
    AND session.runtime_reset_expires_at > clock_timestamp()
) reset
ORDER BY CASE reset.scope WHEN 'bot' THEN 0 ELSE 1 END
LIMIT 1;

-- name: GetBotRuntimeReset :one
SELECT runtime_reset_token, runtime_reset_expires_at
FROM bots
WHERE team_id = public.memoh_current_team_id()
  AND id = sqlc.arg(bot_id)
  AND runtime_reset_token IS NOT NULL
  AND runtime_reset_expires_at > clock_timestamp();

-- name: GetEffectiveBotScopeRuntimeReset :one
-- Mirrors effectiveResetLease in internal/agent/runtime/session/types.go: a
-- bot-scope reader is blocked by the bot lease or by ANY active session lease
-- of that bot, because a bot-wide operation must not proceed while one of the
-- bot's sessions is mid-reset.
SELECT reset.scope, reset.session_id, reset.reset_token, reset.runtime_reset_expires_at
FROM (
  SELECT
    'bot'::text AS scope,
    NULL::uuid AS session_id,
    bot.runtime_reset_token AS reset_token,
    bot.runtime_reset_expires_at
  FROM bots bot
  WHERE bot.team_id = public.memoh_current_team_id()
    AND bot.id = sqlc.arg(bot_id)
    AND bot.runtime_reset_token IS NOT NULL
    AND bot.runtime_reset_expires_at > clock_timestamp()
  UNION ALL
  SELECT
    'session'::text AS scope,
    session.id AS session_id,
    session.runtime_reset_token AS reset_token,
    session.runtime_reset_expires_at
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.bot_id = sqlc.arg(bot_id)
    AND session.runtime_reset_token IS NOT NULL
    AND session.runtime_reset_expires_at > clock_timestamp()
) reset
ORDER BY CASE reset.scope WHEN 'bot' THEN 0 ELSE 1 END, reset.session_id
LIMIT 1;

-- name: SessionLiveForRuntimeReset :one
-- Distinguishes "session reset lease is blocked" from "session no longer
-- exists": an acquire loop must retry the former and fail fast on the latter.
SELECT EXISTS (
  SELECT 1
  FROM bot_sessions session
  WHERE session.team_id = public.memoh_current_team_id()
    AND session.id = sqlc.arg(session_id)
    AND session.bot_id = sqlc.arg(bot_id)
    AND session.deleted_at IS NULL
)::boolean AS live;
