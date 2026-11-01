-- 0167_drop_compaction_settings (down)
-- Restore the per-bot compaction settings with their defaults. Values stored
-- before the drop are not recoverable.
--
-- FK form follows 0162: post-team composite (team_id, col) with the column
-- list on SET NULL, NOT VALID because post-team migrations run under FORCE RLS
-- without memoh.team_id. The column is born NULL, so nothing is skipped.

ALTER TABLE public.bots
  ADD COLUMN IF NOT EXISTS compaction_enabled BOOLEAN NOT NULL DEFAULT true,
  ADD COLUMN IF NOT EXISTS compaction_threshold INTEGER NOT NULL DEFAULT 0,
  ADD COLUMN IF NOT EXISTS compaction_target_percent INTEGER,
  ADD COLUMN IF NOT EXISTS compaction_model_id UUID;

ALTER TABLE public.bots
  DROP CONSTRAINT IF EXISTS bots_compaction_model_id_fkey,
  ADD CONSTRAINT bots_compaction_model_id_fkey
    FOREIGN KEY (team_id, compaction_model_id)
    REFERENCES public.models(team_id, id)
    ON DELETE SET NULL (compaction_model_id)
    NOT VALID;
