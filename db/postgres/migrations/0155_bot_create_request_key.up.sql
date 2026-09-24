-- 0155_bot_create_request_key
-- Idempotent bot creation. A client resends POST /bots with the same
-- Idempotency-Key when the first response never arrived; the unique index
-- makes the key name one bot per owner, so the resend is answered with the bot
-- the first attempt created instead of a second one. Bots created without a
-- key keep NULL and are not indexed.

ALTER TABLE public.bots ADD COLUMN IF NOT EXISTS create_request_key TEXT;

CREATE UNIQUE INDEX IF NOT EXISTS idx_bots_create_request_key
    ON public.bots (team_id, owner_user_id, create_request_key)
    WHERE create_request_key IS NOT NULL;
