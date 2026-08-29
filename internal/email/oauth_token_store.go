package email

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// OAuthToken holds a stored OAuth2 token for an email provider.
type OAuthToken struct {
	ProviderID   string    `json:"provider_id"`
	EmailAddress string    `json:"email_address"`
	AccessToken  string    `json:"access_token"`  //nolint:gosec // encrypted at rest, needed for token refresh.
	RefreshToken string    `json:"refresh_token"` //nolint:gosec // encrypted at rest, needed for token refresh.
	ExpiresAt    time.Time `json:"expires_at"`
	Scope        string    `json:"scope"`
}

// OAuthTokenStore persists and retrieves OAuth tokens for email providers.
type OAuthTokenStore interface {
	Get(ctx context.Context, providerID string) (*OAuthToken, error)
	Save(ctx context.Context, t OAuthToken) error
	SetPendingState(ctx context.Context, providerID, state string) error
	GetByState(ctx context.Context, state string) (*OAuthToken, error)
	Delete(ctx context.Context, providerID string) error
}

// DBOAuthTokenStore is the DB-backed implementation of OAuthTokenStore.
type DBOAuthTokenStore struct {
	queries dbstore.Queries
}

func NewDBOAuthTokenStore(queries dbstore.Queries) *DBOAuthTokenStore {
	return &DBOAuthTokenStore{queries: queries}
}

func (s *DBOAuthTokenStore) Get(ctx context.Context, providerID string) (*OAuthToken, error) {
	pgID, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	row, err := s.queries.GetEmailOAuthTokenByProvider(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("get oauth token: %w", err)
	}
	return s.toOAuthToken(row), nil
}

func (s *DBOAuthTokenStore) Save(ctx context.Context, t OAuthToken) error {
	pgID, err := db.ParseUUID(t.ProviderID)
	if err != nil {
		return err
	}
	var expiresAt pgtype.Timestamptz
	if !t.ExpiresAt.IsZero() {
		expiresAt = pgtype.Timestamptz{Time: t.ExpiresAt, Valid: true}
	}
	_, err = s.queries.UpsertEmailOAuthToken(ctx, sqlc.UpsertEmailOAuthTokenParams{
		EmailProviderID: pgID,
		EmailAddress:    t.EmailAddress,
		AccessToken:     t.AccessToken,
		RefreshToken:    t.RefreshToken,
		ExpiresAt:       expiresAt,
		Scope:           t.Scope,
		State:           "",
	})
	return err
}

func (s *DBOAuthTokenStore) SetPendingState(ctx context.Context, providerID, state string) error {
	pgID, err := db.ParseUUID(providerID)
	if err != nil {
		return err
	}
	return s.queries.UpdateEmailOAuthState(ctx, sqlc.UpdateEmailOAuthStateParams{
		EmailProviderID: pgID,
		State:           state,
	})
}

func (s *DBOAuthTokenStore) GetByState(ctx context.Context, state string) (*OAuthToken, error) {
	row, err := s.queries.GetEmailOAuthTokenByState(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("get oauth token by state: %w", err)
	}
	return s.toOAuthToken(row), nil
}

func (s *DBOAuthTokenStore) Delete(ctx context.Context, providerID string) error {
	pgID, err := db.ParseUUID(providerID)
	if err != nil {
		return err
	}
	return s.queries.DeleteEmailOAuthToken(ctx, pgID)
}

func (*DBOAuthTokenStore) toOAuthToken(row sqlc.EmailOauthToken) *OAuthToken {
	t := &OAuthToken{
		ProviderID:   row.EmailProviderID.String(),
		EmailAddress: row.EmailAddress,
		AccessToken:  row.AccessToken,
		RefreshToken: row.RefreshToken,
		Scope:        row.Scope,
	}
	if row.ExpiresAt.Valid {
		t.ExpiresAt = row.ExpiresAt.Time
	}
	return t
}
