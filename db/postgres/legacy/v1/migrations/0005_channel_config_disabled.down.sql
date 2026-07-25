DO $$
BEGIN
  IF EXISTS (
    SELECT 1 FROM information_schema.columns
    WHERE table_name = 'bot_channel_configs' AND column_name = 'disabled'
  ) THEN
    ALTER TABLE bot_channel_configs ADD COLUMN IF NOT EXISTS status TEXT NOT NULL DEFAULT 'verified';
    UPDATE bot_channel_configs SET status = CASE WHEN disabled THEN 'disabled' ELSE 'verified' END;
    ALTER TABLE bot_channel_configs DROP COLUMN disabled;
    ALTER TABLE bot_channel_configs DROP CONSTRAINT IF EXISTS bot_channel_status_check;
    ALTER TABLE bot_channel_configs ADD CONSTRAINT bot_channel_status_check CHECK (status IN ('pending', 'verified', 'disabled'));
  END IF;
END $$;
