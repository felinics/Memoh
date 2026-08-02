-- 0123_auxiliary_vision_settings
-- Add per-bot auxiliary vision overrides while preserving the process-level
-- TOML configuration as the inherited default.

ALTER TABLE public.bots
    ADD COLUMN IF NOT EXISTS auxiliary_vision_mode TEXT NOT NULL DEFAULT 'inherit',
    ADD COLUMN IF NOT EXISTS auxiliary_vision_model_id UUID REFERENCES public.models(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS auxiliary_vision_prompt TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS auxiliary_vision_max_retries INTEGER,
    ADD COLUMN IF NOT EXISTS auxiliary_vision_timeout_seconds INTEGER,
    DROP CONSTRAINT IF EXISTS bots_auxiliary_vision_mode_check,
    ADD CONSTRAINT bots_auxiliary_vision_mode_check CHECK (
        auxiliary_vision_mode IN ('inherit', 'enabled', 'disabled')
    ),
    DROP CONSTRAINT IF EXISTS bots_auxiliary_vision_max_retries_check,
    ADD CONSTRAINT bots_auxiliary_vision_max_retries_check CHECK (
        auxiliary_vision_max_retries IS NULL
        OR auxiliary_vision_max_retries BETWEEN 0 AND 10
    ),
    DROP CONSTRAINT IF EXISTS bots_auxiliary_vision_timeout_seconds_check,
    ADD CONSTRAINT bots_auxiliary_vision_timeout_seconds_check CHECK (
        auxiliary_vision_timeout_seconds IS NULL
        OR auxiliary_vision_timeout_seconds BETWEEN 1 AND 86400
    );
