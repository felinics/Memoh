package provider

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	providerpostgres "github.com/memohai/memoh/domains/model/internal/postgres/provider"
)

// DefaultProbeTimeout is the timeout used by provider Test and remote model discovery.
const DefaultProbeTimeout = 60 * time.Second

// NewPostgresService creates a provider service backed by PostgreSQL via the
// owner-private adapter. cmd composition should call this constructor only.
func NewPostgresService(
	log *slog.Logger,
	pool *pgxpool.Pool,
	callbackURL string,
	templates TemplateCatalog,
	templatesDir string,
	opts ...Option,
) *Service {
	store := providerpostgres.NewStore(pool)
	return NewServiceWithCatalog(log, store, store, templates, callbackURL, templatesDir, opts...)
}
