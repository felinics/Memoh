// Package provider implements Model Provider-owned PostgreSQL persistence.
package provider

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	providerport "github.com/memohai/memoh/domains/model/internal/port/provider"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type legacyQueries interface {
	CreateProvider(context.Context, dbsqlc.CreateProviderParams) (dbsqlc.ModelProvider, error)
	CreateProviderFromTemplate(context.Context, dbsqlc.CreateProviderFromTemplateParams) (dbsqlc.ModelProvider, error)
	DeleteProvider(context.Context, pgtype.UUID) error
	GetProviderByID(context.Context, pgtype.UUID) (dbsqlc.ModelProvider, error)
	GetProviderByName(context.Context, string) (dbsqlc.ModelProvider, error)
	ListProviders(context.Context) ([]dbsqlc.ModelProvider, error)
	UpdateProvider(context.Context, dbsqlc.UpdateProviderParams) (dbsqlc.ModelProvider, error)

	DeleteProviderOAuthToken(context.Context, pgtype.UUID) error
	GetProviderOAuthTokenByProvider(context.Context, pgtype.UUID) (dbsqlc.ModelProviderOauthToken, error)
	GetProviderOAuthTokenByState(context.Context, string) (dbsqlc.ModelProviderOauthToken, error)
	UpdateProviderOAuthState(context.Context, dbsqlc.UpdateProviderOAuthStateParams) error
	UpsertProviderOAuthToken(context.Context, dbsqlc.UpsertProviderOAuthTokenParams) (dbsqlc.ModelProviderOauthToken, error)
}

// Store confines the current generated query package to the transitional
// PostgreSQL adapter until owner-local Provider SQLC is generated.
type Store struct {
	queries legacyQueries
}

var (
	_ providerport.ProviderStore = (*Store)(nil)
	_ providerport.OAuthStore    = (*Store)(nil)
)

// NewStore creates a postgres-backed provider store from a connection pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{queries: dbsqlc.New(pool)}
}

// NewStoreWithQueries creates a store with an injected query surface (tests).
func NewStoreWithQueries(queries legacyQueries) *Store {
	return &Store{queries: queries}
}

func (s *Store) CreateProvider(ctx context.Context, command providerport.CreateProviderCommand) (providerport.ProviderRecord, error) {
	row, err := s.queries.CreateProvider(ctx, dbsqlc.CreateProviderParams{
		Name:       command.Name,
		ClientType: command.ClientType,
		Icon:       text(command.Icon),
		Enable:     command.Enable,
		Config:     cloneBytes(command.Config),
		Metadata:   cloneBytes(command.Metadata),
	})
	if err != nil {
		return providerport.ProviderRecord{}, mapProviderError(err)
	}
	return providerRecord(row), nil
}

func (s *Store) CreateProviderFromTemplate(ctx context.Context, command providerport.CreateProviderFromTemplateCommand) (providerport.ProviderRecord, error) {
	templateID, err := db.ParseUUID(command.ProviderTemplateID)
	if err != nil {
		return providerport.ProviderRecord{}, err
	}
	row, err := s.queries.CreateProviderFromTemplate(ctx, dbsqlc.CreateProviderFromTemplateParams{
		ProviderTemplateID: templateID,
		Name:               command.Name,
		ClientType:         command.ClientType,
		Icon:               text(command.Icon),
		Enable:             command.Enable,
		Config:             cloneBytes(command.Config),
		Metadata:           cloneBytes(command.Metadata),
	})
	if err != nil {
		return providerport.ProviderRecord{}, mapProviderError(err)
	}
	return providerRecord(row), nil
}

func (s *Store) GetProvider(ctx context.Context, id string) (providerport.ProviderRecord, error) {
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return providerport.ProviderRecord{}, err
	}
	row, err := s.queries.GetProviderByID(ctx, providerID)
	if err != nil {
		return providerport.ProviderRecord{}, mapProviderError(err)
	}
	return providerRecord(row), nil
}

func (s *Store) GetProviderByName(ctx context.Context, name string) (providerport.ProviderRecord, error) {
	row, err := s.queries.GetProviderByName(ctx, name)
	if err != nil {
		return providerport.ProviderRecord{}, mapProviderError(err)
	}
	return providerRecord(row), nil
}

func (s *Store) ListProviders(ctx context.Context) ([]providerport.ProviderRecord, error) {
	rows, err := s.queries.ListProviders(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]providerport.ProviderRecord, 0, len(rows))
	for _, row := range rows {
		items = append(items, providerRecord(row))
	}
	return items, nil
}

func (s *Store) UpdateProvider(ctx context.Context, command providerport.UpdateProviderCommand) (providerport.ProviderRecord, error) {
	providerID, err := db.ParseUUID(command.ID)
	if err != nil {
		return providerport.ProviderRecord{}, err
	}
	row, err := s.queries.UpdateProvider(ctx, dbsqlc.UpdateProviderParams{
		ID:         providerID,
		Name:       command.Name,
		ClientType: command.ClientType,
		Icon:       text(command.Icon),
		Enable:     command.Enable,
		Config:     cloneBytes(command.Config),
		Metadata:   cloneBytes(command.Metadata),
	})
	if err != nil {
		return providerport.ProviderRecord{}, mapProviderError(err)
	}
	return providerRecord(row), nil
}

func (s *Store) DeleteProvider(ctx context.Context, id string) error {
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return mapProviderError(s.queries.DeleteProvider(ctx, providerID))
}

func (s *Store) GetOAuthToken(ctx context.Context, providerID string) (providerport.OAuthTokenRecord, error) {
	id, err := db.ParseUUID(providerID)
	if err != nil {
		return providerport.OAuthTokenRecord{}, err
	}
	row, err := s.queries.GetProviderOAuthTokenByProvider(ctx, id)
	if err != nil {
		return providerport.OAuthTokenRecord{}, mapOAuthError(err)
	}
	return oauthTokenRecord(row), nil
}

func (s *Store) GetOAuthTokenByState(ctx context.Context, state string) (providerport.OAuthTokenRecord, error) {
	row, err := s.queries.GetProviderOAuthTokenByState(ctx, state)
	if err != nil {
		return providerport.OAuthTokenRecord{}, mapOAuthError(err)
	}
	return oauthTokenRecord(row), nil
}

func (s *Store) UpdateOAuthState(ctx context.Context, update providerport.OAuthStateUpdate) error {
	providerID, err := db.ParseUUID(update.ProviderID)
	if err != nil {
		return err
	}
	return s.queries.UpdateProviderOAuthState(ctx, dbsqlc.UpdateProviderOAuthStateParams{
		ProviderID:       providerID,
		State:            update.State,
		PkceCodeVerifier: update.PKCECodeVerifier,
		Metadata:         metadataJSON(update.Metadata),
	})
}

func (s *Store) SaveOAuthToken(ctx context.Context, token providerport.OAuthTokenRecord) error {
	providerID, err := db.ParseUUID(token.ProviderID)
	if err != nil {
		return err
	}
	var expiresAt pgtype.Timestamptz
	if !token.ExpiresAt.IsZero() {
		expiresAt = pgtype.Timestamptz{Time: token.ExpiresAt, Valid: true}
	}
	_, err = s.queries.UpsertProviderOAuthToken(ctx, dbsqlc.UpsertProviderOAuthTokenParams{
		ProviderID:       providerID,
		AccessToken:      token.AccessToken,
		RefreshToken:     token.RefreshToken,
		ExpiresAt:        expiresAt,
		Scope:            token.Scope,
		TokenType:        token.TokenType,
		State:            token.State,
		PkceCodeVerifier: token.PKCECodeVerifier,
		Metadata:         metadataJSON(token.Metadata),
	})
	return err
}

func (s *Store) DeleteOAuthToken(ctx context.Context, providerID string) error {
	id, err := db.ParseUUID(providerID)
	if err != nil {
		return err
	}
	return s.queries.DeleteProviderOAuthToken(ctx, id)
}

func providerRecord(row dbsqlc.ModelProvider) providerport.ProviderRecord {
	return providerport.ProviderRecord{
		ID:                 db.UUIDString(row.ID),
		ProviderTemplateID: db.UUIDString(row.ProviderTemplateID),
		Name:               row.Name,
		ClientType:         row.ClientType,
		Icon:               db.TextToString(row.Icon),
		Enable:             row.Enable,
		Config:             cloneBytes(row.Config),
		Metadata:           cloneBytes(row.Metadata),
		CreatedAt:          row.CreatedAt.Time,
		UpdatedAt:          row.UpdatedAt.Time,
	}
}

func oauthTokenRecord(row dbsqlc.ModelProviderOauthToken) providerport.OAuthTokenRecord {
	record := providerport.OAuthTokenRecord{
		ProviderID:       db.UUIDString(row.ProviderID),
		AccessToken:      row.AccessToken,
		RefreshToken:     row.RefreshToken,
		Scope:            row.Scope,
		TokenType:        row.TokenType,
		State:            row.State,
		PKCECodeVerifier: row.PkceCodeVerifier,
		Metadata:         decodeMetadata(row.Metadata),
	}
	if row.ExpiresAt.Valid {
		record.ExpiresAt = row.ExpiresAt.Time
	}
	return record
}

func mapProviderError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.Join(providerport.ErrProviderNotFound, err)
	}
	if db.IsUniqueViolation(err) {
		return errors.Join(providerport.ErrProviderNameTaken, err)
	}
	return err
}

func mapOAuthError(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return errors.Join(providerport.ErrOAuthTokenNotFound, err)
	}
	return err
}

func text(value string) pgtype.Text {
	return pgtype.Text{String: value, Valid: value != ""}
}

func cloneBytes(value []byte) []byte {
	return append([]byte(nil), value...)
}
