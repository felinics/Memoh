-- Declarative workspace state. Intent columns are written by the API layer;
-- observation columns and the lease are written by the reconciler only.

-- name: UpsertBotWorkspaceIntent :one
-- Record a new intent for the bot's workspace. A new row starts at
-- generation 1; an existing row bumps the generation, keeps the previous image
-- when none is given, resets the retry budget and makes the row due now.
INSERT INTO bot_workspaces (bot_id, desired_state, image, preserve_data)
VALUES (sqlc.arg(bot_id), sqlc.arg(desired_state), sqlc.arg(image), sqlc.arg(preserve_data))
ON CONFLICT (bot_id) DO UPDATE SET
  desired_state      = EXCLUDED.desired_state,
  desired_generation = bot_workspaces.desired_generation + 1,
  image              = CASE WHEN EXCLUDED.image <> '' THEN EXCLUDED.image ELSE bot_workspaces.image END,
  preserve_data      = EXCLUDED.preserve_data,
  attempts           = 0,
  next_attempt_at    = now(),
  version            = bot_workspaces.version + 1,
  updated_at         = now()
RETURNING *;

-- name: GetBotWorkspace :one
SELECT * FROM bot_workspaces
WHERE team_id = public.memoh_current_team_id() AND bot_id = sqlc.arg(bot_id);

-- name: ClaimBotWorkspaces :many
-- Claim due rows for one reconcile pass. A row is due when it has not yet
-- responded to its latest intent, is stuck in a transitional state whose lease
-- expired, or is a failed row whose backoff elapsed. SKIP LOCKED keeps
-- concurrent Server instances from claiming the same row; an expired lease
-- lets another instance take over a crashed pass.
UPDATE bot_workspaces
SET
  lease_owner = sqlc.arg(lease_owner),
  lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::double precision),
  version     = version + 1,
  updated_at  = now()
WHERE bot_id IN (
  SELECT bot_id FROM bot_workspaces
  WHERE team_id = public.memoh_current_team_id()
    AND next_attempt_at <= now()
    AND (lease_until IS NULL OR lease_until < now())
    AND (
      observed_generation < desired_generation
      OR observed_state IN ('provisioning', 'removing')
      OR (desired_state = 'present' AND observed_state IN ('absent', 'failed'))
      OR (desired_state = 'absent' AND observed_state <> 'absent')
    )
  ORDER BY next_attempt_at
  LIMIT sqlc.arg(lim)::int
  FOR UPDATE SKIP LOCKED
)
RETURNING *;

-- name: ClaimBotWorkspace :one
-- Claim one specific row (for an on-demand observation after a user action)
-- when nobody else holds its lease.
UPDATE bot_workspaces
SET
  lease_owner = sqlc.arg(lease_owner),
  lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::double precision),
  version     = version + 1,
  updated_at  = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND (lease_until IS NULL OR lease_until < now())
RETURNING *;

-- name: RenewBotWorkspaceLease :execrows
UPDATE bot_workspaces
SET
  lease_until = now() + make_interval(secs => sqlc.arg(lease_seconds)::double precision),
  updated_at  = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: UpdateBotWorkspaceObserved :one
-- Write an observation. The lease must still be held by the caller; the
-- version pins the row the caller read so a concurrent intent bump is not
-- silently overwritten (the caller re-reads and re-decides instead).
UPDATE bot_workspaces
SET
  observed_state      = sqlc.arg(observed_state),
  observed_generation = sqlc.arg(observed_generation),
  ever_ready          = ever_ready OR sqlc.arg(mark_ready)::boolean,
  last_error          = sqlc.arg(last_error),
  last_error_phase    = sqlc.arg(last_error_phase),
  attempts            = sqlc.arg(attempts),
  next_attempt_at     = sqlc.arg(next_attempt_at),
  lease_owner         = CASE WHEN sqlc.arg(release_lease)::boolean THEN '' ELSE lease_owner END,
  lease_until         = CASE WHEN sqlc.arg(release_lease)::boolean THEN NULL ELSE lease_until END,
  version             = version + 1,
  updated_at          = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND lease_owner = sqlc.arg(lease_owner)
  AND version = sqlc.arg(expected_version)
RETURNING *;

-- name: ReleaseBotWorkspaceLease :execrows
UPDATE bot_workspaces
SET
  lease_owner = '',
  lease_until = NULL,
  updated_at  = now()
WHERE team_id = public.memoh_current_team_id()
  AND bot_id = sqlc.arg(bot_id)
  AND lease_owner = sqlc.arg(lease_owner);

-- name: ListBotWorkspacesByObservedState :many
SELECT * FROM bot_workspaces
WHERE team_id = public.memoh_current_team_id()
  AND observed_state = sqlc.arg(observed_state)
ORDER BY updated_at DESC
LIMIT sqlc.arg(lim)::int;

-- name: CountBotWorkspacesByObservedState :many
SELECT observed_state, COUNT(*)::bigint AS total
FROM bot_workspaces
WHERE team_id = public.memoh_current_team_id()
GROUP BY observed_state;
