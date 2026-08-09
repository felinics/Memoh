-- 0132_bot_peer_grants
-- Drop the bot-to-bot access grant table. Policies, indexes and constraints
-- live on the table itself, so dropping it removes them all.

DROP TABLE IF EXISTS public.bot_peer_grants;
