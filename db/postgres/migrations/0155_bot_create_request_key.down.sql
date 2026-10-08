-- 0155_bot_create_request_key
-- Drop the bot creation idempotency key.

DROP INDEX IF EXISTS public.idx_bots_create_request_key;
ALTER TABLE public.bots DROP COLUMN IF EXISTS create_request_key;
