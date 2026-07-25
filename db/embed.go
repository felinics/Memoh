package db

import "embed"

// MigrationsFS embeds Epoch v2 active owner assets, the frozen v1 ledger, the
// v1→v2 bridge, and the independently versioned pgvector migrations.
//
//go:embed postgres/manifest.yaml
//go:embed postgres/iam/migrations/*.sql
//go:embed postgres/api/migrations/*.sql
//go:embed postgres/agent/migrations/*.sql
//go:embed postgres/channel/migrations/*.sql
//go:embed postgres/memory/migrations/*.sql
//go:embed postgres/runtime/migrations/*.sql
//go:embed postgres/model/migrations/*.sql
//go:embed postgres/media/migrations/*.sql
//go:embed postgres/legacy/v1/migrations/*.sql
//go:embed postgres/legacy/v1/migrations.sha256
//go:embed postgres/legacy/v1/v1_119_schema.sql
//go:embed postgres/legacy/v1/upgrade/to_v2/plan.yaml
//go:embed postgres/legacy/v1/upgrade/to_v2/cross_owner_fks.json
//go:embed postgres/legacy/v1/upgrade/to_v2/sql/*.sql
//go:embed pgvector/migrations/*.sql
var MigrationsFS embed.FS
