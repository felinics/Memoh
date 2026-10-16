-- 0155_bot_create_request_key
-- Idempotency-Key of the POST /bots that created the bot, unique per owner.

ALTER TABLE public.bots ADD COLUMN IF NOT EXISTS create_request_key TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_bots_create_request_key
    ON public.bots (team_id, owner_user_id, create_request_key)
    WHERE create_request_key IS NOT NULL;
