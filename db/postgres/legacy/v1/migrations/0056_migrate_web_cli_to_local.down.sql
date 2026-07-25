-- 0056_migrate_web_cli_to_local (rollback)
-- Revert 'local' back to 'web'. Cannot restore 'cli' distinction since it was merged.

UPDATE channel_identities SET channel_type = 'web' WHERE channel_type = 'local';
UPDATE user_channel_bindings SET channel_type = 'web' WHERE channel_type = 'local';
UPDATE bot_channel_configs SET channel_type = 'web' WHERE channel_type = 'local';
UPDATE channel_identity_bind_codes SET channel_type = 'web' WHERE channel_type = 'local';
UPDATE bot_channel_routes SET channel_type = 'web' WHERE channel_type = 'local';
UPDATE bot_sessions SET channel_type = 'web' WHERE channel_type = 'local';
