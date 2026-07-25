-- 0074_tool_approval_policy_defaults (down)
-- Restore previous tool approval JSON default shape.

ALTER TABLE bots
  ALTER COLUMN tool_approval_config SET DEFAULT '{"enabled":false,"write":{"require_approval":true,"bypass_globs":[".cache/**","tmp/**","node_modules/.cache/**","dist/**"]},"edit":{"require_approval":true,"bypass_globs":[".cache/**","tmp/**","node_modules/.cache/**","dist/**"]},"exec":{"require_approval":true,"bypass_commands":["npm","pnpm","yarn","bun","go","git"]}}'::jsonb;
