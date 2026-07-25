package template

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
	"github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type Store struct {
	transactions transactionBeginner
	bindQueries  func(pgx.Tx) legacyQueries
}

type transactionBeginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

type legacyQueries interface {
	AcquireProviderTemplateSyncLock(context.Context) error
	ListAllProviderTemplates(context.Context) ([]sqlc.ModelProviderTemplate, error)
	UpsertProviderTemplate(context.Context, sqlc.UpsertProviderTemplateParams) (sqlc.ModelProviderTemplate, error)
	SetProviderTemplateActive(context.Context, sqlc.SetProviderTemplateActiveParams) error
	ListAllProviderTemplateModels(context.Context, pgtype.UUID) ([]sqlc.ModelProviderTemplateModel, error)
	UpsertProviderTemplateModel(context.Context, sqlc.UpsertProviderTemplateModelParams) (sqlc.ModelProviderTemplateModel, error)
	SetProviderTemplateModelActive(context.Context, sqlc.SetProviderTemplateModelActiveParams) error
}

type transaction struct {
	queries legacyQueries
}

var (
	_ templateport.SyncStore   = (*Store)(nil)
	_ templateport.Transaction = (*transaction)(nil)
)

func NewSyncStore(pool *pgxpool.Pool) *Store {
	if pool == nil {
		return newSyncStore(nil, nil)
	}
	queries := sqlc.New(pool)
	return newSyncStore(pool, func(tx pgx.Tx) legacyQueries { return queries.WithTx(tx) })
}

func newSyncStore(transactions transactionBeginner, bindQueries func(pgx.Tx) legacyQueries) *Store {
	return &Store{transactions: transactions, bindQueries: bindQueries}
}

func (s *Store) RunSyncTransaction(ctx context.Context, fn func(templateport.Transaction) error) error {
	if fn == nil {
		return errors.New("provider template sync transaction callback is required")
	}
	if s == nil || s.transactions == nil || s.bindQueries == nil {
		return templateport.ErrTransactionsRequired
	}
	tx, err := s.transactions.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(BindTransaction(s.bindQueries(tx))); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// BindTransaction binds provider-template synchronization to
// transaction-scoped PostgreSQL queries.
func BindTransaction(queries legacyQueries) templateport.Transaction {
	return &transaction{queries: queries}
}

func (tx *transaction) AcquireSyncLock(ctx context.Context) error {
	return tx.queries.AcquireProviderTemplateSyncLock(ctx)
}

func (tx *transaction) ListTemplates(ctx context.Context) ([]templateport.TemplateRecord, error) {
	rows, err := tx.queries.ListAllProviderTemplates(ctx)
	if err != nil {
		return nil, err
	}
	templates := make([]templateport.TemplateRecord, 0, len(rows))
	for _, row := range rows {
		templates = append(templates, templateport.TemplateRecord{
			ID:          row.ID.String(),
			Domain:      row.Domain,
			Key:         row.Key,
			ContentHash: row.ContentHash,
			Active:      row.Active,
		})
	}
	return templates, nil
}

func (tx *transaction) UpsertTemplate(ctx context.Context, input templateport.UpsertTemplateCommand) (templateport.TemplateRecord, error) {
	icon := pgtype.Text{}
	if strings.TrimSpace(input.Icon) != "" {
		icon = pgtype.Text{String: input.Icon, Valid: true}
	}
	row, err := tx.queries.UpsertProviderTemplate(ctx, sqlc.UpsertProviderTemplateParams{
		Key:           input.Key,
		Domain:        input.Domain,
		Name:          input.Name,
		Description:   input.Description,
		Icon:          icon,
		Driver:        input.Driver,
		ConfigSchema:  input.ConfigSchema,
		DefaultConfig: input.DefaultConfig,
		Metadata:      input.Metadata,
		Source:        input.Source,
		ContentHash:   input.ContentHash,
		SortOrder:     int32(input.SortOrder), //nolint:gosec // Catalog sizes are bounded by checked-in configuration.
	})
	if err != nil {
		return templateport.TemplateRecord{}, err
	}
	return templateport.TemplateRecord{
		ID:          row.ID.String(),
		Domain:      row.Domain,
		Key:         row.Key,
		ContentHash: row.ContentHash,
		Active:      row.Active,
	}, nil
}

func (tx *transaction) DeactivateTemplate(ctx context.Context, id string) error {
	postgresID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return tx.queries.SetProviderTemplateActive(ctx, sqlc.SetProviderTemplateActiveParams{
		ID: postgresID, Active: false,
	})
}

func (tx *transaction) ListModels(ctx context.Context, templateID string) ([]templateport.ModelRecord, error) {
	postgresID, err := db.ParseUUID(templateID)
	if err != nil {
		return nil, err
	}
	rows, err := tx.queries.ListAllProviderTemplateModels(ctx, postgresID)
	if err != nil {
		return nil, err
	}
	models := make([]templateport.ModelRecord, 0, len(rows))
	for _, row := range rows {
		models = append(models, templateport.ModelRecord{
			ID: row.ID.String(), ModelID: row.ModelID, Type: row.Type,
		})
	}
	return models, nil
}

func (tx *transaction) UpsertModel(ctx context.Context, input templateport.UpsertModelCommand) error {
	templateID, err := db.ParseUUID(input.TemplateID)
	if err != nil {
		return err
	}
	_, err = tx.queries.UpsertProviderTemplateModel(ctx, sqlc.UpsertProviderTemplateModelParams{
		ProviderTemplateID: templateID,
		ModelID:            input.ModelID,
		Name:               input.Name,
		Type:               input.Type,
		Config:             input.Config,
		Metadata:           input.Metadata,
		SortOrder:          int32(input.SortOrder), //nolint:gosec // Catalog sizes are bounded by checked-in configuration.
	})
	return err
}

func (tx *transaction) DeactivateModel(ctx context.Context, id string) error {
	postgresID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return tx.queries.SetProviderTemplateModelActive(ctx, sqlc.SetProviderTemplateModelActiveParams{
		ID: postgresID, Active: false,
	})
}
