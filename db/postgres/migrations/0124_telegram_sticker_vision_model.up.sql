-- 0124_telegram_sticker_vision_model
-- Add an optional Telegram Sticker vision model that inherits the Bot's
-- auxiliary-vision model when unset.

ALTER TABLE public.bots
    ADD COLUMN IF NOT EXISTS telegram_sticker_vision_model_id UUID
        REFERENCES public.models(id) ON DELETE SET NULL;
