-- 0126_drop_reasoning_enabled
-- Collapse the reasoning on/off flag into reasoning_effort. The effort column
-- becomes the single source of truth: 'disable' means no reasoning, any other
-- value is a concrete tier, and the column default ('medium') is what a bot that
-- was never configured resolves to.
--
-- Existing rows map reasoning_enabled = false to 'disable', so upgrading never
-- silently turns reasoning on (and its cost) for a running deployment. Because
-- the column default was false, this covers nearly every existing bot; raising
-- those to a real tier is deliberately left to a separate, opt-in migration.

UPDATE bots SET reasoning_effort = 'disable' WHERE reasoning_enabled = false;

ALTER TABLE bots DROP COLUMN IF EXISTS reasoning_enabled;
