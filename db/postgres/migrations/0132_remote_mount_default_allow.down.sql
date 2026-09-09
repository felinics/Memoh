-- 0132_remote_mount_default_allow
-- Restore the previous Remote Runtime mount-time default.
ALTER TABLE bot_remote_runtime_bindings
  ALTER COLUMN tool_approval_config SET DEFAULT '{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"ask","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"ask","bypass_commands":[],"force_review_commands":[]}}'::jsonb;
