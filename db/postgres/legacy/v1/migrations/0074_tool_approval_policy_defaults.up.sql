-- 0074_tool_approval_policy_defaults
-- Update tool approval policy JSON defaults and add force-review rule arrays.

ALTER TABLE bots
  ALTER COLUMN tool_approval_config SET DEFAULT '{"enabled":false,"write":{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]},"edit":{"require_approval":true,"bypass_globs":["/data/**","/tmp/**"],"force_review_globs":[]},"exec":{"require_approval":false,"bypass_commands":[],"force_review_commands":[]}}'::jsonb;

UPDATE bots
SET tool_approval_config = jsonb_build_object(
  'enabled', COALESCE(tool_approval_config->'enabled', 'false'::jsonb),
  'write', jsonb_build_object(
    'require_approval', COALESCE(tool_approval_config #> '{write,require_approval}', 'true'::jsonb),
    'bypass_globs', '["/data/**","/tmp/**"]'::jsonb,
    'force_review_globs', COALESCE(tool_approval_config #> '{write,force_review_globs}', '[]'::jsonb)
  ),
  'edit', jsonb_build_object(
    'require_approval', COALESCE(tool_approval_config #> '{edit,require_approval}', 'true'::jsonb),
    'bypass_globs', '["/data/**","/tmp/**"]'::jsonb,
    'force_review_globs', COALESCE(tool_approval_config #> '{edit,force_review_globs}', '[]'::jsonb)
  ),
  'exec', jsonb_build_object(
    'require_approval', false,
    'bypass_commands', COALESCE(tool_approval_config #> '{exec,bypass_commands}', '[]'::jsonb) - 'npm' - 'pnpm' - 'yarn' - 'bun' - 'go' - 'git',
    'force_review_commands', COALESCE(tool_approval_config #> '{exec,force_review_commands}', '[]'::jsonb)
  )
)
WHERE tool_approval_config IS NOT NULL;
