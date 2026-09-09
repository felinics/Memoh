-- 0139_session_runtime_reset_fence
-- Remove crash-recoverable bot/session reset leases.

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_runtime_reset_pair_check,
    DROP COLUMN IF EXISTS runtime_config_epoch,
    DROP COLUMN IF EXISTS runtime_reset_expires_at,
    DROP COLUMN IF EXISTS runtime_reset_token;

ALTER TABLE public.bots
    DROP CONSTRAINT IF EXISTS bots_runtime_reset_pair_check,
    DROP COLUMN IF EXISTS runtime_config_epoch,
    DROP COLUMN IF EXISTS runtime_reset_expires_at,
    DROP COLUMN IF EXISTS runtime_reset_token;
