-- 0019_add_email (rollback)
-- Drop email tables in reverse order of creation.

DROP TABLE IF EXISTS email_outbox;
DROP TABLE IF EXISTS bot_email_bindings;
DROP TABLE IF EXISTS email_providers;
