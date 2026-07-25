package catalog

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	catalogpostgres "github.com/memohai/memoh/domains/model/internal/postgres/catalog"
)

// NewPostgresService creates a model catalog service backed by PostgreSQL via
// the owner-private adapter. cmd composition should call this constructor only.
func NewPostgresService(log *slog.Logger, pool *pgxpool.Pool, resolver ProviderResolver, opts ...Option) *Service {
	options := append([]Option{WithProviderResolver(resolver)}, opts...)
	return NewService(log, catalogpostgres.NewStore(pool), options...)
}

// TitleModelValidator is the minimal surface Accounts inject for title-model
// reference validation.
type TitleModelValidator interface {
	IsValidTitleModel(context.Context, string) (bool, error)
}

// NewPostgresTitleModelValidator returns a PostgreSQL-backed title model
// checker for Accounts injection. cmd must use this constructor rather than
// importing owner-private postgres adapters.
func NewPostgresTitleModelValidator(pool *pgxpool.Pool) TitleModelValidator {
	return catalogpostgres.NewTitleModelValidator(pool)
}
