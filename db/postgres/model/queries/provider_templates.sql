-- name: AcquireProviderTemplateSyncLock :exec
SELECT pg_advisory_xact_lock(78273144982731);

-- name: ListProviderTemplates :many
SELECT
  t.*,
  EXISTS (
    SELECT 1 FROM model.providers p
     WHERE p.team_id = iam.memoh_current_team_id()
       AND (
         p.provider_template_id = t.id
         OR p.metadata->'template'->>'id' = t.id::text
         OR (
           t.source <> ''
           AND (
             p.metadata->'preset'->>'source' = t.source
             OR p.metadata->'registry'->>'source' = t.source
           )
         )
       )
  ) AS configured
FROM model.provider_templates t
WHERE t.active = true
  AND (sqlc.arg(domain)::text = '' OR t.domain = sqlc.arg(domain))
ORDER BY t.sort_order ASC, t.name ASC, t.key ASC;

-- name: ListAllProviderTemplates :many
SELECT *
FROM model.provider_templates
ORDER BY domain ASC, sort_order ASC, key ASC;

-- name: GetProviderTemplateByID :one
SELECT *
FROM model.provider_templates
WHERE id = sqlc.arg(id) AND active = true;

-- name: GetProviderTemplateByDomainAndKey :one
SELECT *
FROM model.provider_templates
WHERE domain = sqlc.arg(domain) AND key = sqlc.arg(key);

-- name: UpsertProviderTemplate :one
INSERT INTO model.provider_templates (
  key,
  domain,
  name,
  description,
  icon,
  driver,
  config_schema,
  default_config,
  metadata,
  source,
  content_hash,
  sort_order,
  active
)
VALUES (
  sqlc.arg(key),
  sqlc.arg(domain),
  sqlc.arg(name),
  sqlc.arg(description),
  sqlc.arg(icon),
  sqlc.arg(driver),
  sqlc.arg(config_schema),
  sqlc.arg(default_config),
  sqlc.arg(metadata),
  sqlc.arg(source),
  sqlc.arg(content_hash),
  sqlc.arg(sort_order),
  true
)
ON CONFLICT (domain, key) DO UPDATE SET
  name = EXCLUDED.name,
  description = EXCLUDED.description,
  icon = EXCLUDED.icon,
  driver = EXCLUDED.driver,
  config_schema = EXCLUDED.config_schema,
  default_config = EXCLUDED.default_config,
  metadata = EXCLUDED.metadata,
  source = EXCLUDED.source,
  content_hash = EXCLUDED.content_hash,
  sort_order = EXCLUDED.sort_order,
  active = true,
  updated_at = now()
RETURNING *;

-- name: SetProviderTemplateActive :exec
UPDATE model.provider_templates
SET active = sqlc.arg(active), updated_at = now()
WHERE id = sqlc.arg(id) AND active IS DISTINCT FROM sqlc.arg(active);

-- name: ListProviderTemplateModels :many
SELECT *
FROM model.provider_template_models
WHERE provider_template_id = sqlc.arg(provider_template_id)
  AND active = true
ORDER BY sort_order ASC, model_id ASC;

-- name: ListAllProviderTemplateModels :many
SELECT *
FROM model.provider_template_models
WHERE provider_template_id = sqlc.arg(provider_template_id)
ORDER BY sort_order ASC, model_id ASC;

-- name: UpsertProviderTemplateModel :one
INSERT INTO model.provider_template_models (
  provider_template_id,
  model_id,
  name,
  type,
  config,
  metadata,
  sort_order,
  active
)
VALUES (
  sqlc.arg(provider_template_id),
  sqlc.arg(model_id),
  sqlc.arg(name),
  sqlc.arg(type),
  sqlc.arg(config),
  sqlc.arg(metadata),
  sqlc.arg(sort_order),
  true
)
ON CONFLICT (provider_template_id, type, model_id) DO UPDATE SET
  name = EXCLUDED.name,
  config = EXCLUDED.config,
  metadata = EXCLUDED.metadata,
  sort_order = EXCLUDED.sort_order,
  active = true,
  updated_at = now()
RETURNING *;

-- name: SetProviderTemplateModelActive :exec
UPDATE model.provider_template_models
SET active = sqlc.arg(active), updated_at = now()
WHERE id = sqlc.arg(id) AND active IS DISTINCT FROM sqlc.arg(active);
