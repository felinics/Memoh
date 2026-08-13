-- 0132_remote_mount_default_allow
-- Default new and untouched Remote Runtime mounts to allow-everything.
-- Remote mounts default to allow-everything: mounting a computer is already
-- the explicit trust decision, so per-call asks are just friction. The Go
-- fallback (DefaultRemoteToolApprovalConfig) only fires when the stored config
-- is EMPTY, but new mounts never store empty — the insert omits this column,
-- so this DEFAULT is the value every new binding actually gets. It must carry
-- the allow-everything default or the Go-side change never takes effect.
ALTER TABLE bot_remote_runtime_bindings
  ALTER COLUMN tool_approval_config SET DEFAULT '{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"allow","bypass_commands":[],"force_review_commands":[]}}'::jsonb;

-- bot_remote_runtime_bindings is under FORCE row level security keyed on
-- memoh.team_id, which a migration connection never sets, so a bare UPDATE
-- fails with "memoh.team_id is not set". Lift the policy for the data step
-- and restore it after, the same way 0128 does. (The SET DEFAULT above is
-- DDL and not subject to RLS, so it needs no such lift.)
-- Flip rows still holding the pre-change default exactly (jsonb = is
-- normalized, so key order doesn't matter). Caveat: a row explicitly saved as
-- exactly the old default is indistinguishable from a default-created row and
-- would also flip; any row with an actually-edited config stays untouched.
ALTER TABLE bot_remote_runtime_bindings NO FORCE ROW LEVEL SECURITY;
ALTER TABLE bot_remote_runtime_bindings DISABLE ROW LEVEL SECURITY;

UPDATE bot_remote_runtime_bindings
SET tool_approval_config = '{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"allow","bypass_commands":[],"force_review_commands":[]}}'::jsonb
WHERE tool_approval_config = '{"enabled":true,"read":{"mode":"allow","bypass_globs":[],"force_review_globs":[]},"write":{"mode":"ask","bypass_globs":[],"force_review_globs":[]},"exec":{"mode":"ask","bypass_commands":[],"force_review_commands":[]}}'::jsonb;

ALTER TABLE bot_remote_runtime_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE bot_remote_runtime_bindings FORCE ROW LEVEL SECURITY;
