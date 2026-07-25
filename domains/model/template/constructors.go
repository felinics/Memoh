package template

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	templatepostgres "github.com/memohai/memoh/domains/model/internal/postgres/template"
)

// NewPostgresService creates a template catalog service backed by PostgreSQL.
func NewPostgresService(log *slog.Logger, pool *pgxpool.Pool) *Service {
	return NewService(log, templatepostgres.NewCatalogStore(pool))
}

// SyncCatalog synchronizes checked-in template definitions into PostgreSQL.
func SyncCatalog(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, definitions []Definition) error {
	if pool == nil {
		return ErrTransactionsRequired
	}
	return Sync(ctx, log, templatepostgres.NewSyncStore(pool), definitions)
}

// SyncProvidersCatalog upserts YAML definitions into provider/model catalogs.
func SyncProvidersCatalog(ctx context.Context, log *slog.Logger, pool *pgxpool.Pool, definitions []Definition) error {
	if pool == nil {
		return ErrTransactionsRequired
	}
	return SyncProviders(ctx, log, templatepostgres.NewProviderCatalog(pool), templatepostgres.NewModelCatalog(pool), definitions)
}
