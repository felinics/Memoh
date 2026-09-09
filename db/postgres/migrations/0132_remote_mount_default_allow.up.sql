-- 0132_remote_mount_default_allow
-- Default new Remote Runtime mounts to allow-everything.
-- Remote mounts default to allow-everything: mounting a computer is already
-- the explicit trust decision, so per-call asks are just friction. The Go
-- fallback (DefaultRemoteToolApprovalConfig) only fires when the stored config
-- is EMPTY, but new mounts never store empty — the insert omits this column,
-- so this DEFAULT is the value every new binding actually gets. It must carry
-- the allow-everything default or the Go-side change never takes effect.
ALTER TABLE bot_remote_runtime_bindings
  ALTER COLUMN tool_approval_config SET DEFAULT '{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"allow","bypass_commands":[],"force_review_commands":[]}}'::jsonb;
