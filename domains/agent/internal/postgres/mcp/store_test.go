package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	mcppersistence "github.com/memohai/memoh/domains/agent/mcp/persistence"
	"github.com/memohai/memoh/internal/db"
)

const (
	testBotID        = "11111111-1111-1111-1111-111111111111"
	testConnectionID = "22222222-2222-2222-2222-222222222222"
)

func TestConnectionStoreCreateMapsDomainInput(t *testing.T) {
	queries := &connectionQueryFake{row: connectionRow(t)}
	store := NewConnectionStore(queries)

	got, err := store.CreateConnection(t.Context(), mcppersistence.ConnectionWrite{
		BotID: testBotID, Name: "docs", Type: "http",
		Config: map[string]any{"url": "https://example.test/mcp"},
		Active: true, AuthType: "oauth",
	})
	if err != nil {
		t.Fatalf("CreateConnection() error = %v", err)
	}
	if queries.create.BotID.String() != testBotID || queries.create.Name != "docs" {
		t.Fatalf("CreateMCPConnection params = %#v", queries.create)
	}
	if string(queries.create.Config) != `{"url":"https://example.test/mcp"}` {
		t.Fatalf("config = %s", queries.create.Config)
	}
	if !queries.create.IsActive || queries.create.AuthType != "oauth" {
		t.Fatalf("active/auth = %v/%q", queries.create.IsActive, queries.create.AuthType)
	}
	if got.ID != testConnectionID || got.BotID != testBotID || got.Name != "docs" {
		t.Fatalf("connection = %#v", got)
	}
	if len(got.ToolsCache) != 0 || got.Config["url"] != "https://example.test/mcp" {
		t.Fatalf("normalized fields = %#v", got)
	}
}

func TestConnectionStoreSaveProbeMapsTools(t *testing.T) {
	queries := &connectionQueryFake{}
	store := NewConnectionStore(queries)

	err := store.SaveConnectionProbe(t.Context(), mcppersistence.ConnectionProbeWrite{
		BotID: testBotID, ConnectionID: testConnectionID, Status: "healthy",
		Tools: []mcppersistence.ToolDescriptor{{Name: "search"}}, StatusMessage: "ok",
	})
	if err != nil {
		t.Fatalf("SaveConnectionProbe() error = %v", err)
	}
	if queries.probe.BotID.String() != testBotID || queries.probe.ID.String() != testConnectionID {
		t.Fatalf("probe IDs = %#v", queries.probe)
	}
	if string(queries.probe.ToolsCache) != `[{"name":"search","inputSchema":null}]` {
		t.Fatalf("tools cache = %s", queries.probe.ToolsCache)
	}
}

func TestConnectionStoreMapsMissingRows(t *testing.T) {
	tests := []struct {
		name string
		run  func(*ConnectionStore) error
	}{
		{
			name: "get",
			run: func(store *ConnectionStore) error {
				_, err := store.GetConnection(t.Context(), testBotID, testConnectionID)
				return err
			},
		},
		{
			name: "update",
			run: func(store *ConnectionStore) error {
				_, err := store.UpdateConnection(t.Context(), mcppersistence.ConnectionWrite{
					ID: testConnectionID, BotID: testBotID, Name: "docs", Type: "http",
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queries := &connectionQueryFake{getErr: pgx.ErrNoRows, updateErr: pgx.ErrNoRows}
			err := test.run(NewConnectionStore(queries))
			if !errors.Is(err, mcppersistence.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
			if errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("error leaked pgx.ErrNoRows: %v", err)
			}
		})
	}
}

func TestConnectionRejectsInvalidPersistedConfig(t *testing.T) {
	row := connectionRow(t)
	row.Config = []byte(`{"url":`)
	if _, err := connection(row); err == nil {
		t.Fatal("connection() error = nil, want invalid JSON error")
	}
}

func TestOAuthStoreMapsTokens(t *testing.T) {
	expiresAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	queries := &oauthQueryFake{}
	store := NewOAuthStore(queries)

	err := store.SaveTokens(t.Context(), testConnectionID, mcppersistence.OAuthTokens{
		AccessToken: "access", RefreshToken: "refresh", TokenType: "Bearer",
		ExpiresAt: &expiresAt, Scope: "mcp:connect",
	})
	if err != nil {
		t.Fatalf("SaveTokens() error = %v", err)
	}
	if queries.tokens.ConnectionID.String() != testConnectionID || !queries.tokens.ExpiresAt.Valid {
		t.Fatalf("token params = %#v", queries.tokens)
	}
	if !queries.tokens.ExpiresAt.Time.Equal(expiresAt) || queries.tokens.RefreshToken != "refresh" {
		t.Fatalf("token values = %#v", queries.tokens)
	}
}

func TestOAuthStoreMapsMissingRows(t *testing.T) {
	tests := []struct {
		name string
		run  func(*OAuthStore) error
	}{
		{
			name: "connection",
			run: func(store *OAuthStore) error {
				_, err := store.GetOAuthToken(t.Context(), testConnectionID)
				return err
			},
		},
		{
			name: "state",
			run: func(store *OAuthStore) error {
				_, err := store.GetOAuthTokenByState(t.Context(), "state")
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewOAuthStore(&oauthQueryFake{
				getErr:        pgx.ErrNoRows,
				getByStateErr: pgx.ErrNoRows,
			})
			err := test.run(store)
			if !errors.Is(err, mcppersistence.ErrNotFound) {
				t.Fatalf("error = %v, want ErrNotFound", err)
			}
			if errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("error leaked pgx.ErrNoRows: %v", err)
			}
		})
	}
}

func TestOAuthTokenMapsPersistedState(t *testing.T) {
	expiresAt := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	got := oauthToken(dbsqlc.AgentMcpOauthToken{
		ConnectionID:           db.ParseUUIDOrEmpty(testConnectionID),
		AuthorizationServerUrl: "https://auth.example.test",
		PkceCodeVerifier:       "verifier",
		ResourceUri:            "https://resource.example.test",
		ExpiresAt:              pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if got.ConnectionID != testConnectionID || got.AuthorizationServerURL != "https://auth.example.test" {
		t.Fatalf("OAuthToken = %#v", got)
	}
	if got.PKCECodeVerifier != "verifier" || got.ResourceURI != "https://resource.example.test" {
		t.Fatalf("OAuthToken aliases = %#v", got)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("ExpiresAt = %v", got.ExpiresAt)
	}
}

func connectionRow(t *testing.T) dbsqlc.AgentMcpConnection {
	t.Helper()
	return dbsqlc.AgentMcpConnection{
		ID: db.ParseUUIDOrEmpty(testConnectionID), BotID: db.ParseUUIDOrEmpty(testBotID),
		Name: " docs ", Type: "http", Config: []byte(`{"url":"https://example.test/mcp"}`),
		IsActive: true, ToolsCache: []byte(`not-json`), Metadata: []byte(`{}`),
		CreatedAt: pgtype.Timestamptz{Time: time.Unix(10, 0), Valid: true},
		UpdatedAt: pgtype.Timestamptz{Time: time.Unix(20, 0), Valid: true},
	}
}

type connectionQueryFake struct {
	row       dbsqlc.AgentMcpConnection
	getErr    error
	updateErr error
	create    dbsqlc.CreateMCPConnectionParams
	probe     dbsqlc.UpdateMCPConnectionProbeResultParams
}

func (f *connectionQueryFake) ListMCPConnectionsByBotID(context.Context, pgtype.UUID) ([]dbsqlc.AgentMcpConnection, error) {
	return []dbsqlc.AgentMcpConnection{f.row}, nil
}

func (f *connectionQueryFake) GetMCPConnectionByID(context.Context, dbsqlc.GetMCPConnectionByIDParams) (dbsqlc.AgentMcpConnection, error) {
	return f.row, f.getErr
}

func (f *connectionQueryFake) CreateMCPConnection(_ context.Context, arg dbsqlc.CreateMCPConnectionParams) (dbsqlc.AgentMcpConnection, error) {
	f.create = arg
	return f.row, nil
}

func (f *connectionQueryFake) CreateManagedMCPConnection(context.Context, dbsqlc.CreateManagedMCPConnectionParams) (dbsqlc.AgentMcpConnection, error) {
	return f.row, nil
}

func (f *connectionQueryFake) UpdateMCPConnection(context.Context, dbsqlc.UpdateMCPConnectionParams) (dbsqlc.AgentMcpConnection, error) {
	return f.row, f.updateErr
}

func (f *connectionQueryFake) UpsertMCPConnectionByName(context.Context, dbsqlc.UpsertMCPConnectionByNameParams) (dbsqlc.AgentMcpConnection, error) {
	return f.row, nil
}

func (*connectionQueryFake) DeleteMCPConnection(context.Context, dbsqlc.DeleteMCPConnectionParams) error {
	return nil
}

func (*connectionQueryFake) UpdateMCPConnectionsActiveByPlugin(context.Context, dbsqlc.UpdateMCPConnectionsActiveByPluginParams) error {
	return nil
}

func (*connectionQueryFake) DeleteMCPConnectionsByPlugin(context.Context, dbsqlc.DeleteMCPConnectionsByPluginParams) error {
	return nil
}

func (f *connectionQueryFake) UpdateMCPConnectionProbeResult(_ context.Context, arg dbsqlc.UpdateMCPConnectionProbeResultParams) error {
	f.probe = arg
	return nil
}

type oauthQueryFake struct {
	tokens        dbsqlc.UpdateMCPOAuthTokensParams
	getErr        error
	getByStateErr error
}

func (*oauthQueryFake) UpsertMCPOAuthDiscovery(context.Context, dbsqlc.UpsertMCPOAuthDiscoveryParams) (dbsqlc.AgentMcpOauthToken, error) {
	return dbsqlc.AgentMcpOauthToken{}, nil
}

func (f *oauthQueryFake) GetMCPOAuthToken(context.Context, pgtype.UUID) (dbsqlc.AgentMcpOauthToken, error) {
	return dbsqlc.AgentMcpOauthToken{}, f.getErr
}

func (f *oauthQueryFake) GetMCPOAuthTokenByState(context.Context, string) (dbsqlc.AgentMcpOauthToken, error) {
	return dbsqlc.AgentMcpOauthToken{}, f.getByStateErr
}

func (*oauthQueryFake) UpdateMCPOAuthPKCEState(context.Context, dbsqlc.UpdateMCPOAuthPKCEStateParams) error {
	return nil
}

func (*oauthQueryFake) UpdateMCPOAuthClientSecret(context.Context, dbsqlc.UpdateMCPOAuthClientSecretParams) error {
	return nil
}

func (f *oauthQueryFake) UpdateMCPOAuthTokens(_ context.Context, arg dbsqlc.UpdateMCPOAuthTokensParams) error {
	f.tokens = arg
	return nil
}

func (*oauthQueryFake) UpdateMCPConnectionAuthType(context.Context, dbsqlc.UpdateMCPConnectionAuthTypeParams) error {
	return nil
}

func (*oauthQueryFake) ClearMCPOAuthTokens(context.Context, pgtype.UUID) error {
	return nil
}
