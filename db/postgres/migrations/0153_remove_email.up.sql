-- 0153_remove_email
-- Remove built-in bot email configuration, credentials, bindings, and sent mail.
-- This permanently deletes the data; rollback can restore only empty tables.

DROP TABLE IF EXISTS public.email_outbox;
DROP TABLE IF EXISTS public.bot_email_bindings;
DROP TABLE IF EXISTS public.email_oauth_tokens;
DROP TABLE IF EXISTS public.email_providers;
