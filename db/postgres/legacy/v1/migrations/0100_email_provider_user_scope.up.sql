-- 0100_email_provider_user_scope
-- Scope email providers to users and move Gmail OAuth client secrets to server config.

ALTER TABLE email_providers
  ADD COLUMN IF NOT EXISTS user_id UUID REFERENCES users(id) ON DELETE CASCADE;

UPDATE email_providers
SET config = config - 'client_id' - 'client_secret'
WHERE provider = 'gmail';

ALTER TABLE email_providers
  DROP CONSTRAINT IF EXISTS email_providers_name_unique;

CREATE TEMP TABLE email_provider_owner_map (
  old_provider_id UUID NOT NULL,
  owner_user_id UUID NOT NULL,
  new_provider_id UUID NOT NULL,
  is_primary BOOLEAN NOT NULL,
  PRIMARY KEY (old_provider_id, owner_user_id)
) ON COMMIT DROP;

WITH bound_owners AS (
  SELECT DISTINCT
    beb.email_provider_id AS old_provider_id,
    b.owner_user_id
  FROM bot_email_bindings AS beb
  JOIN bots AS b ON b.id = beb.bot_id
),
fallback_owner AS (
  SELECT id AS owner_user_id
  FROM users AS u
  WHERE username IS NOT NULL
  -- 0001 is the canonical latest schema, where role has moved to
  -- team_members. to_jsonb keeps this historical migration replayable while
  -- preserving the legacy preference for an admin when the column exists.
  ORDER BY (to_jsonb(u) ->> 'role' = 'admin') DESC, created_at ASC
  LIMIT 1
),
provider_owners AS (
  SELECT old_provider_id, owner_user_id
  FROM bound_owners
  UNION
  SELECT ep.id, fo.owner_user_id
  FROM email_providers AS ep
  CROSS JOIN fallback_owner AS fo
  WHERE NOT EXISTS (
    SELECT 1
    FROM bound_owners AS bo
    WHERE bo.old_provider_id = ep.id
  )
),
ranked_owners AS (
  SELECT
    old_provider_id,
    owner_user_id,
    ROW_NUMBER() OVER (PARTITION BY old_provider_id ORDER BY owner_user_id::text) AS owner_rank
  FROM provider_owners
)
INSERT INTO email_provider_owner_map (old_provider_id, owner_user_id, new_provider_id, is_primary)
SELECT
  old_provider_id,
  owner_user_id,
  CASE WHEN owner_rank = 1 THEN old_provider_id ELSE gen_random_uuid() END,
  owner_rank = 1
FROM ranked_owners;

UPDATE email_providers AS ep
SET user_id = map.owner_user_id
FROM email_provider_owner_map AS map
WHERE ep.id = map.old_provider_id
  AND map.is_primary;

INSERT INTO email_providers (id, user_id, name, provider, config, created_at, updated_at)
SELECT
  map.new_provider_id,
  map.owner_user_id,
  ep.name,
  ep.provider,
  ep.config,
  ep.created_at,
  ep.updated_at
FROM email_provider_owner_map AS map
JOIN email_providers AS ep ON ep.id = map.old_provider_id
WHERE NOT map.is_primary;

INSERT INTO email_oauth_tokens (
  email_provider_id,
  email_address,
  access_token,
  refresh_token,
  expires_at,
  scope,
  state,
  created_at,
  updated_at
)
SELECT
  map.new_provider_id,
  tok.email_address,
  tok.access_token,
  tok.refresh_token,
  tok.expires_at,
  tok.scope,
  tok.state,
  tok.created_at,
  tok.updated_at
FROM email_provider_owner_map AS map
JOIN email_oauth_tokens AS tok ON tok.email_provider_id = map.old_provider_id
WHERE NOT map.is_primary
ON CONFLICT DO NOTHING;

UPDATE bot_email_bindings AS beb
SET email_provider_id = map.new_provider_id
FROM bots AS b, email_provider_owner_map AS map
WHERE b.id = beb.bot_id
  AND map.old_provider_id = beb.email_provider_id
  AND map.owner_user_id = b.owner_user_id;

DO $$
BEGIN
  IF NOT EXISTS (
    SELECT 1
    FROM pg_constraint
    WHERE conname = 'email_providers_user_name_unique'
  ) THEN
    ALTER TABLE email_providers
      ADD CONSTRAINT email_providers_user_name_unique UNIQUE (user_id, name);
  END IF;
END
$$;

CREATE INDEX IF NOT EXISTS idx_email_providers_user_id ON email_providers(user_id);

ALTER TABLE email_providers
  ALTER COLUMN user_id SET NOT NULL;

INSERT INTO email_providers (user_id, name, provider, config)
SELECT u.id, 'Gmail', 'gmail', '{}'::jsonb
FROM users AS u
WHERE u.username IS NOT NULL
ON CONFLICT ON CONSTRAINT email_providers_user_name_unique DO NOTHING;

DROP TABLE email_provider_owner_map;
