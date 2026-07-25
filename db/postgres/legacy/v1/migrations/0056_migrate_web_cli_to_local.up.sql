-- 0056_migrate_web_cli_to_local
-- Rename channel_type 'web' and 'cli' to 'local' across all tables.

UPDATE channel_identities SET channel_type = 'local' WHERE channel_type IN ('web', 'cli');
UPDATE user_channel_bindings SET channel_type = 'local' WHERE channel_type IN ('web', 'cli');
UPDATE bot_channel_configs SET channel_type = 'local' WHERE channel_type IN ('web', 'cli');
UPDATE channel_identity_bind_codes SET channel_type = 'local' WHERE channel_type IN ('web', 'cli');
UPDATE bot_channel_routes SET channel_type = 'local' WHERE channel_type IN ('web', 'cli');
UPDATE bot_sessions SET channel_type = 'local' WHERE channel_type IN ('web', 'cli');
