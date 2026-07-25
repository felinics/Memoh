package template

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type providerQueries interface {
	ListProviders(context.Context) ([]dbsqlc.ModelProvider, error)
	ListSpeechProviders(context.Context) ([]dbsqlc.ModelProvider, error)
	ListTranscriptionProviders(context.Context) ([]dbsqlc.ModelProvider, error)
	UpsertRegistryProvider(context.Context, dbsqlc.UpsertRegistryProviderParams) (dbsqlc.ModelProvider, error)
	UpdateProvider(context.Context, dbsqlc.UpdateProviderParams) (dbsqlc.ModelProvider, error)
}

// ProviderCatalog adapts generated statements for YAML provider sync.
type ProviderCatalog struct {
	queries providerQueries
}

var _ templateport.ProviderCatalog = (*ProviderCatalog)(nil)

func NewProviderCatalog(pool *pgxpool.Pool) *ProviderCatalog {
	return &ProviderCatalog{queries: dbsqlc.New(pool)}
}

func NewProviderCatalogWithQueries(queries providerQueries) *ProviderCatalog {
	return &ProviderCatalog{queries: queries}
}

func (c *ProviderCatalog) ListProviders(ctx context.Context) ([]templateport.ProviderRecord, error) {
	var out []templateport.ProviderRecord
	seen := make(map[string]bool)
	for _, list := range []func(context.Context) ([]dbsqlc.ModelProvider, error){
		c.queries.ListProviders,
		c.queries.ListSpeechProviders,
		c.queries.ListTranscriptionProviders,
	} {
		rows, err := list(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			item := providerRecord(row)
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			out = append(out, item)
		}
	}
	return out, nil
}

func (c *ProviderCatalog) UpsertProvider(ctx context.Context, seed templateport.ProviderSeed) (templateport.ProviderRecord, error) {
	row, err := c.queries.UpsertRegistryProvider(ctx, dbsqlc.UpsertRegistryProviderParams{
		Name:       seed.Name,
		ClientType: seed.ClientType,
		Icon:       text(seed.Icon),
		Config:     cloneBytes(seed.Config),
	})
	if err != nil {
		return templateport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func (c *ProviderCatalog) UpdateProvider(ctx context.Context, update templateport.ProviderUpdate) (templateport.ProviderRecord, error) {
	id, err := db.ParseUUID(update.ID)
	if err != nil {
		return templateport.ProviderRecord{}, err
	}
	row, err := c.queries.UpdateProvider(ctx, dbsqlc.UpdateProviderParams{
		ID:         id,
		Name:       update.Name,
		ClientType: update.ClientType,
		Icon:       text(update.Icon),
		Enable:     update.Enable,
		Config:     cloneBytes(update.Config),
		Metadata:   cloneBytes(update.Metadata),
	})
	if err != nil {
		return templateport.ProviderRecord{}, err
	}
	return providerRecord(row), nil
}

func providerRecord(row dbsqlc.ModelProvider) templateport.ProviderRecord {
	return templateport.ProviderRecord{
		ID:         uuidString(row.ID),
		Name:       row.Name,
		ClientType: row.ClientType,
		Icon:       textString(row.Icon),
		Enable:     row.Enable,
		Config:     cloneBytes(row.Config),
		Metadata:   cloneBytes(row.Metadata),
	}
}

func uuidString(value pgtype.UUID) string {
	if !value.Valid {
		return ""
	}
	return value.String()
}

func text(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: value, Valid: true}
}

func textString(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
