-- Replace status (TEXT) with disabled (BOOLEAN). Idempotent: no-op when already migrated.
DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'bot_channel_configs' AND column_name = 'status'
  ) THEN
    ALTER TABLE bot_channel_configs ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT false;
    UPDATE bot_channel_configs SET disabled = (status = 'disabled');
    ALTER TABLE bot_channel_configs DROP CONSTRAINT IF EXISTS bot_channel_status_check;
    ALTER TABLE bot_channel_configs DROP COLUMN status;
  END IF;
END $$;
