-- 0131_tool_approval_options
-- Add ACP permission choices and the selected choice to approval audit rows.
--
-- This file deliberately spans three transactions. PostgreSQL retains ALTER
-- TABLE locks until commit; staging the wider CHECK as NOT VALID, committing,
-- then validating it keeps ACCESS EXCLUSIVE out of the historical row scan.
-- NOT VALID still enforces the constraint for every new or updated row.
-- Each phase is idempotent so a dirty, partially applied migration can be
-- forced back to 0129 and retried.

BEGIN;

-- Constant defaults make this an additive metadata-only change on supported
-- PostgreSQL versions and keep old binaries' explicitly generated SQL valid.
ALTER TABLE IF EXISTS public.tool_approval_requests
  ADD COLUMN IF NOT EXISTS options JSONB NOT NULL DEFAULT '[]'::jsonb;
ALTER TABLE IF EXISTS public.tool_approval_requests
  ADD COLUMN IF NOT EXISTS selected_option_id TEXT NOT NULL DEFAULT '';

DO $$
BEGIN
  IF to_regclass('public.tool_approval_requests') IS NOT NULL AND NOT EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid = 'public.tool_approval_requests'::regclass
       AND conname = 'tool_approval_operation_check_0131'
  ) THEN
    ALTER TABLE public.tool_approval_requests
      ADD CONSTRAINT tool_approval_operation_check_0131
      CHECK (operation IN ('read', 'write', 'exec', 'permission')) NOT VALID;
  END IF;
END $$;

COMMIT;

-- VALIDATE uses SHARE UPDATE EXCLUSIVE, which permits ordinary reads/writes.
BEGIN;

DO $$
BEGIN
  IF to_regclass('public.tool_approval_requests') IS NOT NULL AND EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid = 'public.tool_approval_requests'::regclass
       AND conname = 'tool_approval_operation_check_0131'
       AND NOT convalidated
  ) THEN
    ALTER TABLE public.tool_approval_requests
      VALIDATE CONSTRAINT tool_approval_operation_check_0131;
  END IF;
END $$;

COMMIT;

-- The replacement is validated, so the canonical-name switch is catalog-only.
BEGIN;

DO $$
BEGIN
  IF to_regclass('public.tool_approval_requests') IS NULL THEN
    RETURN;
  END IF;

  IF EXISTS (
    SELECT 1 FROM pg_constraint
     WHERE conrelid = 'public.tool_approval_requests'::regclass
       AND conname = 'tool_approval_operation_check_0131'
       AND convalidated
  ) THEN
    ALTER TABLE public.tool_approval_requests
      DROP CONSTRAINT IF EXISTS tool_approval_operation_check;
    ALTER TABLE public.tool_approval_requests
      RENAME CONSTRAINT tool_approval_operation_check_0131 TO tool_approval_operation_check;
  END IF;

  -- Completing a roll-forward after a failed down must remove the rollback
  -- guards, which intentionally reject new 0131 data while down is pending.
  ALTER TABLE public.tool_approval_requests
    DROP CONSTRAINT IF EXISTS tool_approval_operation_check_down_0131;
  ALTER TABLE public.tool_approval_requests
    DROP CONSTRAINT IF EXISTS tool_approval_options_rollback_guard_0131;
END $$;

COMMIT;
