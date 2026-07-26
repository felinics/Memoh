package mcp

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	mcppersistence "github.com/memohai/memoh/domains/agent/mcp/persistence"
	"github.com/memohai/memoh/internal/db"
)

type oauthQueries interface {
	UpsertMCPOAuthDiscovery(context.Context, dbsqlc.UpsertMCPOAuthDiscoveryParams) (dbsqlc.AgentMcpOauthToken, error)
	GetMCPOAuthToken(context.Context, pgtype.UUID) (dbsqlc.AgentMcpOauthToken, error)
	GetMCPOAuthTokenByState(context.Context, string) (dbsqlc.AgentMcpOauthToken, error)
	UpdateMCPOAuthPKCEState(context.Context, dbsqlc.UpdateMCPOAuthPKCEStateParams) error
	UpdateMCPOAuthClientSecret(context.Context, dbsqlc.UpdateMCPOAuthClientSecretParams) error
	UpdateMCPOAuthTokens(context.Context, dbsqlc.UpdateMCPOAuthTokensParams) error
	UpdateMCPConnectionAuthType(context.Context, dbsqlc.UpdateMCPConnectionAuthTypeParams) error
	ClearMCPOAuthTokens(context.Context, pgtype.UUID) error
}

// OAuthStore adapts Agent-owner SQLC statements to MCP's OAuth persistence port.
type OAuthStore struct {
	queries oauthQueries
}

var _ mcppersistence.OAuthStore = (*OAuthStore)(nil)

func NewOAuthStore(queries oauthQueries) *OAuthStore {
	return &OAuthStore{queries: queries}
}

func NewOAuthStoreFromDB(db dbsqlc.DBTX) *OAuthStore {
	return NewOAuthStore(dbsqlc.New(db))
}

func (s *OAuthStore) SaveDiscovery(ctx context.Context, connectionID string, result mcppersistence.DiscoveryResult) error {
	id, err := db.ParseUUID(connectionID)
	if err != nil {
		return err
	}
	scopes := result.ScopesSupported
	if scopes == nil {
		scopes = []string{}
	}
	_, err = s.queries.UpsertMCPOAuthDiscovery(ctx, dbsqlc.UpsertMCPOAuthDiscoveryParams{
		ConnectionID:           id,
		ResourceMetadataUrl:    result.ResourceMetadataURL,
		AuthorizationServerUrl: result.AuthorizationServerURL,
		AuthorizationEndpoint:  result.AuthorizationEndpoint,
		TokenEndpoint:          result.TokenEndpoint,
		RegistrationEndpoint:   result.RegistrationEndpoint,
		ScopesSupported:        scopes,
		ResourceUri:            result.ResourceURI,
	})
	return mapNotFound(err)
}

func (s *OAuthStore) GetOAuthToken(ctx context.Context, connectionID string) (mcppersistence.OAuthToken, error) {
	id, err := db.ParseUUID(connectionID)
	if err != nil {
		return mcppersistence.OAuthToken{}, err
	}
	row, err := s.queries.GetMCPOAuthToken(ctx, id)
	if err != nil {
		return mcppersistence.OAuthToken{}, mapNotFound(err)
	}
	return oauthToken(row), nil
}

func (s *OAuthStore) GetOAuthTokenByState(ctx context.Context, state string) (mcppersistence.OAuthToken, error) {
	row, err := s.queries.GetMCPOAuthTokenByState(ctx, state)
	if err != nil {
		return mcppersistence.OAuthToken{}, mapNotFound(err)
	}
	return oauthToken(row), nil
}

func (s *OAuthStore) SavePKCEState(ctx context.Context, connectionID string, state mcppersistence.PKCEState) error {
	id, err := db.ParseUUID(connectionID)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.UpdateMCPOAuthPKCEState(ctx, dbsqlc.UpdateMCPOAuthPKCEStateParams{
		ConnectionID:     id,
		PkceCodeVerifier: state.CodeVerifier,
		StateParam:       state.State,
		ClientID:         state.ClientID,
		RedirectUri:      state.RedirectURI,
	}))
}

func (s *OAuthStore) SaveClientSecret(ctx context.Context, connectionID, clientSecret string) error {
	id, err := db.ParseUUID(connectionID)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.UpdateMCPOAuthClientSecret(ctx, dbsqlc.UpdateMCPOAuthClientSecretParams{
		ConnectionID: id,
		ClientSecret: clientSecret,
	}))
}

func (s *OAuthStore) SaveTokens(ctx context.Context, connectionID string, tokens mcppersistence.OAuthTokens) error {
	id, err := db.ParseUUID(connectionID)
	if err != nil {
		return err
	}
	expiresAt := pgtype.Timestamptz{}
	if tokens.ExpiresAt != nil {
		expiresAt = pgtype.Timestamptz{Time: *tokens.ExpiresAt, Valid: true}
	}
	return mapNotFound(s.queries.UpdateMCPOAuthTokens(ctx, dbsqlc.UpdateMCPOAuthTokensParams{
		ConnectionID: id,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		TokenType:    tokens.TokenType,
		ExpiresAt:    expiresAt,
		Scope:        tokens.Scope,
	}))
}

func (s *OAuthStore) SetConnectionAuthType(ctx context.Context, connectionID, authType string) error {
	id, err := db.ParseUUID(connectionID)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.UpdateMCPConnectionAuthType(ctx, dbsqlc.UpdateMCPConnectionAuthTypeParams{ID: id, AuthType: authType}))
}

func (s *OAuthStore) ClearTokens(ctx context.Context, connectionID string) error {
	id, err := db.ParseUUID(connectionID)
	if err != nil {
		return err
	}
	return mapNotFound(s.queries.ClearMCPOAuthTokens(ctx, id))
}

func oauthToken(row dbsqlc.AgentMcpOauthToken) mcppersistence.OAuthToken {
	var expiresAt *time.Time
	if row.ExpiresAt.Valid {
		value := db.TimeFromPg(row.ExpiresAt)
		expiresAt = &value
	}
	return mcppersistence.OAuthToken{
		ConnectionID:           row.ConnectionID.String(),
		AuthorizationServerURL: row.AuthorizationServerUrl,
		AuthorizationEndpoint:  row.AuthorizationEndpoint,
		TokenEndpoint:          row.TokenEndpoint,
		RegistrationEndpoint:   row.RegistrationEndpoint,
		ScopesSupported:        row.ScopesSupported,
		ClientID:               row.ClientID,
		ClientSecret:           row.ClientSecret,
		AccessToken:            row.AccessToken,
		RefreshToken:           row.RefreshToken,
		ExpiresAt:              expiresAt,
		Scope:                  row.Scope,
		PKCECodeVerifier:       row.PkceCodeVerifier,
		ResourceURI:            row.ResourceUri,
		RedirectURI:            row.RedirectUri,
	}
}
