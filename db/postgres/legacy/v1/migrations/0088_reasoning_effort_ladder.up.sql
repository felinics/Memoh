-- 0088_reasoning_effort_ladder
-- Allow the full reasoning effort ladder stored by command/settings.

ALTER TABLE bots DROP CONSTRAINT IF EXISTS bots_reasoning_effort_check;
ALTER TABLE bots ADD CONSTRAINT bots_reasoning_effort_check
  CHECK (reasoning_effort IN ('none', 'low', 'medium', 'high', 'xhigh'));
