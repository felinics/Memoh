-- 0100_email_provider_user_scope
-- Restore global email provider names.

CREATE TEMP TABLE email_provider_global_map (
  old_provider_id UUID PRIMARY KEY,
  survivor_provider_id UUID NOT NULL
) ON COMMIT DROP;

INSERT INTO email_provider_global_map (old_provider_id, survivor_provider_id)
SELECT
  ep.id,
  (
    SELECT ep2.id
    FROM email_providers AS ep2
    WHERE ep2.name = ep.name
    ORDER BY
      (
        SELECT COUNT(*)
        FROM email_oauth_tokens AS tok
        WHERE tok.email_provider_id = ep2.id
      ) DESC,
      ep2.created_at ASC,
      ep2.id ASC
    LIMIT 1
  )
FROM email_providers AS ep;

UPDATE bot_email_bindings AS beb
SET email_provider_id = map.survivor_provider_id
FROM email_provider_global_map AS map
WHERE map.old_provider_id = beb.email_provider_id
  AND map.old_provider_id <> map.survivor_provider_id;

DELETE FROM email_oauth_tokens
WHERE email_provider_id IN (
  SELECT old_provider_id
  FROM email_provider_global_map
  WHERE old_provider_id <> survivor_provider_id
);

DELETE FROM email_providers
WHERE id IN (
  SELECT old_provider_id
  FROM email_provider_global_map
  WHERE old_provider_id <> survivor_provider_id
);

DROP INDEX IF EXISTS idx_email_providers_user_id;

ALTER TABLE email_providers
  DROP CONSTRAINT IF EXISTS email_providers_user_name_unique;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'email_providers_name_unique'
  ) THEN
    ALTER TABLE email_providers
      ADD CONSTRAINT email_providers_name_unique UNIQUE (name);
  END IF;
END
$$;

ALTER TABLE email_providers
  DROP COLUMN IF EXISTS user_id;

DROP TABLE email_provider_global_map;
