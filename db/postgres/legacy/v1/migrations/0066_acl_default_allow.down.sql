-- 0066_acl_default_allow
-- Restore the bot ACL default effect to deny for newly created bots.

DO $$
BEGIN
  IF EXISTS (
    SELECT 1
    FROM information_schema.columns
    WHERE table_name = 'bots' AND column_name = 'acl_default_effect'
  ) THEN
    ALTER TABLE bots
      ALTER COLUMN acl_default_effect SET DEFAULT 'deny';
  END IF;
END $$;
