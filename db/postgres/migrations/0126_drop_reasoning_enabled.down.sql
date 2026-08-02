-- 0126_drop_reasoning_enabled (down)
-- Restore the boolean flag from the effort value, then fold 'disable' back into
-- the pre-migration representation (flag off + a concrete tier), since the older
-- schema cannot store 'disable'.

ALTER TABLE bots ADD COLUMN IF NOT EXISTS reasoning_enabled BOOLEAN NOT NULL DEFAULT false;

UPDATE bots SET reasoning_enabled = (reasoning_effort <> 'disable');

UPDATE bots SET reasoning_effort = 'medium' WHERE reasoning_effort = 'disable';
