-- 0124_telegram_sticker_vision_model
-- Remove the per-bot Telegram Sticker vision-model override.

ALTER TABLE public.bots
    DROP COLUMN IF EXISTS telegram_sticker_vision_model_id;
