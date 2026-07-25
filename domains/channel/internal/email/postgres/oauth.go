package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type oauthQueries interface {
	GetEmailOAuthTokenByProvider(context.Context, pgtype.UUID) (channelsqlc.ChannelEmailOauthToken, error)
	UpsertEmailOAuthToken(context.Context, channelsqlc.UpsertEmailOAuthTokenParams) (channelsqlc.ChannelEmailOauthToken, error)
	UpdateEmailOAuthState(context.Context, channelsqlc.UpdateEmailOAuthStateParams) error
	GetEmailOAuthTokenByState(context.Context, string) (channelsqlc.ChannelEmailOauthToken, error)
	DeleteEmailOAuthToken(context.Context, pgtype.UUID) error
}

// OAuthTokenStore adapts generated PostgreSQL statements to Email's OAuth port.
type OAuthTokenStore struct {
	queries oauthQueries
}

var _ emailport.OAuthTokenStore = (*OAuthTokenStore)(nil)

func NewOAuthTokenStore(pool *pgxpool.Pool) *OAuthTokenStore {
	return NewOAuthTokenStoreWithQueries(channelsqlc.New(pool))
}

func NewOAuthTokenStoreWithQueries(queries oauthQueries) *OAuthTokenStore {
	return &OAuthTokenStore{queries: queries}
}

func (s *OAuthTokenStore) Get(ctx context.Context, providerID string) (*emailport.OAuthToken, error) {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	row, err := s.queries.GetEmailOAuthTokenByProvider(ctx, parsed)
	if err != nil {
		return nil, fmt.Errorf("get oauth token: %w", classifyError(err))
	}
	return oauthToken(row), nil
}

func (s *OAuthTokenStore) Save(ctx context.Context, token emailport.OAuthToken) error {
	providerID, err := db.ParseUUID(token.ProviderID)
	if err != nil {
		return err
	}
	var expiresAt pgtype.Timestamptz
	if !token.ExpiresAt.IsZero() {
		expiresAt = pgtype.Timestamptz{Time: token.ExpiresAt, Valid: true}
	}
	_, err = s.queries.UpsertEmailOAuthToken(ctx, channelsqlc.UpsertEmailOAuthTokenParams{
		EmailProviderID: providerID, EmailAddress: token.EmailAddress,
		AccessToken: token.AccessToken, RefreshToken: token.RefreshToken,
		ExpiresAt: expiresAt, Scope: token.Scope, State: "",
	})
	return err
}

func (s *OAuthTokenStore) SetPendingState(ctx context.Context, providerID, state string) error {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return err
	}
	return s.queries.UpdateEmailOAuthState(ctx, channelsqlc.UpdateEmailOAuthStateParams{
		EmailProviderID: parsed, State: state,
	})
}

func (s *OAuthTokenStore) GetByState(ctx context.Context, state string) (*emailport.OAuthToken, error) {
	row, err := s.queries.GetEmailOAuthTokenByState(ctx, state)
	if err != nil {
		return nil, fmt.Errorf("get oauth token by state: %w", classifyError(err))
	}
	return oauthToken(row), nil
}

func (s *OAuthTokenStore) Delete(ctx context.Context, providerID string) error {
	parsed, err := db.ParseUUID(providerID)
	if err != nil {
		return err
	}
	return s.queries.DeleteEmailOAuthToken(ctx, parsed)
}

func oauthToken(row channelsqlc.ChannelEmailOauthToken) *emailport.OAuthToken {
	token := &emailport.OAuthToken{
		ProviderID: row.EmailProviderID.String(), EmailAddress: row.EmailAddress,
		AccessToken: row.AccessToken, RefreshToken: row.RefreshToken, Scope: row.Scope,
	}
	if row.ExpiresAt.Valid {
		token.ExpiresAt = db.TimeFromPg(row.ExpiresAt)
	}
	return token
}
