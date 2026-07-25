-- 0014_reasoning (rollback)
-- Remove reasoning support flag from models and reasoning settings from bots.

ALTER TABLE bots DROP CONSTRAINT IF EXISTS bots_reasoning_effort_check;
ALTER TABLE bots DROP COLUMN IF EXISTS reasoning_effort;
ALTER TABLE bots DROP COLUMN IF EXISTS reasoning_enabled;

ALTER TABLE models DROP COLUMN IF EXISTS supports_reasoning;
