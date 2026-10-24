-- 0171_compaction_rewind
-- Drop the compaction rewind function.

DROP FUNCTION IF EXISTS public.memoh_rewind_compaction(uuid, timestamptz);
