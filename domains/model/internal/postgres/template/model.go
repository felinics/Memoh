package template

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type modelQueries interface {
	ListModelsByProviderID(context.Context, pgtype.UUID) ([]dbsqlc.ModelModel, error)
	UpsertRegistryModel(context.Context, dbsqlc.UpsertRegistryModelParams) (dbsqlc.ModelModel, error)
}

// ModelCatalog adapts generated statements for YAML model sync.
type ModelCatalog struct {
	queries modelQueries
}

var _ templateport.ModelCatalog = (*ModelCatalog)(nil)

func NewModelCatalog(pool *pgxpool.Pool) *ModelCatalog {
	return &ModelCatalog{queries: dbsqlc.New(pool)}
}

func NewModelCatalogWithQueries(queries modelQueries) *ModelCatalog {
	return &ModelCatalog{queries: queries}
}

func (c *ModelCatalog) ListModelIDs(ctx context.Context, providerID string) ([]string, error) {
	id, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	rows, err := c.queries.ListModelsByProviderID(ctx, id)
	if err != nil {
		return nil, err
	}
	items := make([]string, 0, len(rows))
	for _, row := range rows {
		items = append(items, row.ModelID)
	}
	return items, nil
}

func (c *ModelCatalog) UpsertModel(ctx context.Context, seed templateport.ModelSeed) error {
	providerID, err := db.ParseUUID(seed.ProviderID)
	if err != nil {
		return err
	}
	_, err = c.queries.UpsertRegistryModel(ctx, dbsqlc.UpsertRegistryModelParams{
		ProviderID: providerID,
		ModelID:    seed.ModelID,
		Name:       text(seed.Name),
		Type:       seed.Type,
		Config:     cloneBytes(seed.Config),
	})
	return err
}
