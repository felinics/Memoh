# Server-owned model catalog

The server owns models.dev fetching, validation, conversion and caching. Web and
Desktop render the same normalized response and request an import by provider ID;
they do not download the upstream catalog or derive runtime model configurations.

## Boundaries

- Public metadata and credentials are separate. Only a server-configured URL is
  fetched. API requests cannot supply a URL, and no provider credentials are sent
  to the public catalog. Cached data is public and shared across tenants.
- `GET /providers/catalog/models?source=...&client_type=...` previews normalized
  chat models without creating or changing providers or model rows.
- `POST /providers/{id}/import-catalog-models` resolves the source and
  protocol from the stored provider. `skip_model_ids` preserves the existing flow
  where a user creates a model manually before importing the remaining catalog.
- An explicit catalog import never silently switches to an authenticated provider
  endpoint. A valid empty catalog remains empty. Users can choose endpoint import
  separately. New models remain disabled and the existing refresh merge rules
  remain unchanged by this relocation.
- Endpoint discovery keeps its existing request contract. Public catalog imports
  accept only model IDs to preserve, never client-generated model configs. The
  provisional `models` payload from the unmerged catalog PR was removed.
  A distinct catalog URL returns 404 on an older server rather than silently
  falling through to endpoint discovery.
- Source identity comes from explicit `metadata.source`, or the `registry` /
  `preset` metadata written by shipped YAML templates. Gallery IDs are not source
  IDs and do not introduce another compatibility path.

## Cache and failure behavior

Optional server configuration:

```toml
[model_catalog]
url = "https://models.dev/api.json"
# Default: <workspace.data_root>/.cache/models-dev.json
cache_path = "/opt/memoh/data/.cache/models-dev.json"
```

The URL is an operator-controlled metadata mirror, not a client-supplied URL. It
must use HTTP(S) without URL credentials. Mount the cache directory if it should
survive a pod replacement; an unwritable cache falls back to process memory.

The process shares one immutable raw catalog snapshot. Refreshes are coalesced,
bounded to 10 seconds and 32 MiB, and use ETag revalidation. Freshness is 30
minutes; failed refreshes back off for one minute. A failed fetch or malformed catalog envelope
cannot replace a valid snapshot. A caller canceling its request does not cancel
the bounded fetch shared by other callers.

The server stores the last valid raw snapshot atomically under the configured
cache path. It revalidates that snapshot on restart and converts it with the
current server's code. Cache write failures do not fail an otherwise valid
preview/import. When refresh fails, the last valid snapshot is served with
`stale: true`, its validation time and a content revision. Without a valid snapshot
the API returns `provider.catalog_unavailable`. Refresh itself never writes model
rows. Each server replica owns its cache; this is not a distributed lock or a
cross-replica consistency guarantee.

Unknown upstream properties are tolerated. Only the requested provider is decoded
and validated; a broken neighbor does not prevent healthy sources from being used.
Malformed models or unsupported control values fail that provider before any
writes. Other sources remain usable. Provider/model identity,
limits and normalized configurations are checked without constructing a database model.
This change preserves the existing conversion vocabulary and protocol adapters;
it does not introduce new wire protocols or broaden their supported controls.

## Cloud after PR 668

Cloud PR 668 is already deployed. Its relay `/v1/models` emits both the existing
v1 view and v2 capability fields. That authenticated, platform-owned catalog is
not a models.dev response and must never pass through the public source parser.

- Managed providers and `metadata.source: builtin` cannot use public catalog
  imports. The endpoint-discovery entry point also retains this protection.
- Cloud's builtin sync continues to own availability, per-model `client_type`,
  limits, dimensions and authoritative capability replacement. Tenant enable
  preferences remain separate. Public catalog metadata does not set Cloud
  prices, quota or allowed models.
- No database migration or change to PR 668's relay/API/schema is required for
  this service. Keep 668 deployed; do not run its down migration for an OSS rollback.
- The later OSS-to-Cloud sync still needs the builtin v2 consumer described by
  MEM-61, preserving Cloud's managed guards and effective client type. Upgrade
  Memoh and the worker's `tenant-bootstrap` together. This change does not solve
  PR 1574's separate read-time model-config migration/rollback incompatibility.
- During mixed-version releases, ship the server before the new Web/Desktop
  client. Existing endpoint-discovery clients keep their request contract. A newer client against an old
  server cannot use the new catalog preview/import contract; do not silently fall
  back to client-side conversion.

## Verification

Tests cover the former frontend conversion fixtures, malformed input, empty
catalogs, cache freshness, ETags, concurrent loads, cancellation, corrupt cache
recovery, stale fallback, managed-provider rejection and import source identity.
Runtime verification must exercise the real provider preview and import UI with
browser access to models.dev blocked, then check server-side cache/runtime effects.
