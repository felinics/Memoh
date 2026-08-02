-- 0123_auxiliary_vision_settings
-- Remove per-bot auxiliary vision overrides.

ALTER TABLE public.bots
    DROP CONSTRAINT IF EXISTS bots_auxiliary_vision_timeout_seconds_check,
    DROP CONSTRAINT IF EXISTS bots_auxiliary_vision_max_retries_check,
    DROP CONSTRAINT IF EXISTS bots_auxiliary_vision_mode_check,
    DROP COLUMN IF EXISTS auxiliary_vision_timeout_seconds,
    DROP COLUMN IF EXISTS auxiliary_vision_max_retries,
    DROP COLUMN IF EXISTS auxiliary_vision_prompt,
    DROP COLUMN IF EXISTS auxiliary_vision_model_id,
    DROP COLUMN IF EXISTS auxiliary_vision_mode;
