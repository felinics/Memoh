-- 0139_session_runtime_reset_fence
-- Add crash-recoverable bot/session reset leases that fence new run admission.

ALTER TABLE public.bots
    ADD COLUMN IF NOT EXISTS runtime_reset_token UUID,
    ADD COLUMN IF NOT EXISTS runtime_reset_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS runtime_config_epoch BIGINT NOT NULL DEFAULT 0;

ALTER TABLE public.bot_sessions
    ADD COLUMN IF NOT EXISTS runtime_reset_token UUID,
    ADD COLUMN IF NOT EXISTS runtime_reset_expires_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS runtime_config_epoch BIGINT NOT NULL DEFAULT 0;

ALTER TABLE public.bots
    DROP CONSTRAINT IF EXISTS bots_runtime_reset_pair_check,
    ADD CONSTRAINT bots_runtime_reset_pair_check CHECK (
        (runtime_reset_token IS NULL) = (runtime_reset_expires_at IS NULL)
    );

ALTER TABLE public.bot_sessions
    DROP CONSTRAINT IF EXISTS bot_sessions_runtime_reset_pair_check,
    ADD CONSTRAINT bot_sessions_runtime_reset_pair_check CHECK (
        (runtime_reset_token IS NULL) = (runtime_reset_expires_at IS NULL)
    );
