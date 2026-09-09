-- 0131_tool_approval_options
-- Remove ACP approval choices only when no audit row would lose information.
--
-- This destructive rollback takes one exclusive table lock so the data guard,
-- operation constraint replacement, and column removal are atomic. Production
-- application rollback should normally retain the additive 0131 schema.

BEGIN;

DO $$
DECLARE
  has_options BOOLEAN;
  has_selected_option_id BOOLEAN;
BEGIN
  IF to_regclass('public.tool_approval_requests') IS NULL THEN
    RETURN;
  END IF;

  -- Prevent a writer from inserting 0131-only data after the guard scan.
  LOCK TABLE public.tool_approval_requests IN ACCESS EXCLUSIVE MODE;

  SELECT EXISTS (
    SELECT 1 FROM pg_attribute
     WHERE attrelid = 'public.tool_approval_requests'::regclass
       AND attname = 'options' AND NOT attisdropped
  ) INTO has_options;
  SELECT EXISTS (
    SELECT 1 FROM pg_attribute
     WHERE attrelid = 'public.tool_approval_requests'::regclass
       AND attname = 'selected_option_id' AND NOT attisdropped
  ) INTO has_selected_option_id;

  IF has_options <> has_selected_option_id THEN
    RAISE EXCEPTION '0131 approval option columns are only partially present';
  END IF;

  -- Constraint validation scans every row even when FORCE RLS is enabled.
  ALTER TABLE public.tool_approval_requests
    DROP CONSTRAINT IF EXISTS tool_approval_operation_check_down_0131;
  ALTER TABLE public.tool_approval_requests
    ADD CONSTRAINT tool_approval_operation_check_down_0131
    CHECK (operation IN ('read', 'write', 'exec')) NOT VALID;

  IF has_options THEN
    ALTER TABLE public.tool_approval_requests
      DROP CONSTRAINT IF EXISTS tool_approval_options_rollback_guard_0131;
    ALTER TABLE public.tool_approval_requests
      ADD CONSTRAINT tool_approval_options_rollback_guard_0131
      CHECK (options = '[]'::jsonb AND selected_option_id = '') NOT VALID;
  END IF;

  BEGIN
    ALTER TABLE public.tool_approval_requests
      VALIDATE CONSTRAINT tool_approval_operation_check_down_0131;
    IF has_options THEN
      ALTER TABLE public.tool_approval_requests
        VALIDATE CONSTRAINT tool_approval_options_rollback_guard_0131;
    END IF;
  EXCEPTION
    WHEN check_violation THEN
      RAISE EXCEPTION 'cannot roll back 0131: tool approval rows contain permission choices or selections';
  END;

  ALTER TABLE public.tool_approval_requests
    DROP CONSTRAINT IF EXISTS tool_approval_operation_check;
  ALTER TABLE public.tool_approval_requests
    DROP CONSTRAINT IF EXISTS tool_approval_operation_check_0131;
  ALTER TABLE public.tool_approval_requests
    RENAME CONSTRAINT tool_approval_operation_check_down_0131 TO tool_approval_operation_check;

  ALTER TABLE public.tool_approval_requests
    DROP CONSTRAINT IF EXISTS tool_approval_options_rollback_guard_0131;
  ALTER TABLE public.tool_approval_requests
    DROP COLUMN IF EXISTS selected_option_id;
  ALTER TABLE public.tool_approval_requests
    DROP COLUMN IF EXISTS options;
END $$;

COMMIT;
