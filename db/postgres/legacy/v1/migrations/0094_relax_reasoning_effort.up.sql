-- 0094_relax_reasoning_effort
-- Reasoning effort is now a free-form tier string (minimal/low/medium/high/xhigh/max/none),
-- driven by per-model capability discovery rather than a fixed enum. Drop the
-- old CHECK constraint that only permitted low/medium/high.

ALTER TABLE bots DROP CONSTRAINT IF EXISTS bots_reasoning_effort_check;
