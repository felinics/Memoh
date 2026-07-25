package provider

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	providerport "github.com/memohai/memoh/domains/model/internal/port/provider"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type fakeLegacyQueries struct {
	legacyQueries
	createProviderInput dbsqlc.CreateProviderParams
	createdProvider     dbsqlc.ModelProvider
	providerReadError   error
	oauthUpsertInput    dbsqlc.UpsertProviderOAuthTokenParams
}

func (q *fakeLegacyQueries) CreateProvider(_ context.Context, input dbsqlc.CreateProviderParams) (dbsqlc.ModelProvider, error) {
	q.createProviderInput = input
	return q.createdProvider, nil
}

func (q *fakeLegacyQueries) GetProviderByID(context.Context, pgtype.UUID) (dbsqlc.ModelProvider, error) {
	return dbsqlc.ModelProvider{}, q.providerReadError
}

func (q *fakeLegacyQueries) UpsertProviderOAuthToken(_ context.Context, input dbsqlc.UpsertProviderOAuthTokenParams) (dbsqlc.ModelProviderOauthToken, error) {
	q.oauthUpsertInput = input
	return dbsqlc.ModelProviderOauthToken{}, nil
}

func TestStoreCreateProviderConvertsPersistenceTypes(t *testing.T) {
	providerID, err := db.ParseUUID("3a16ce55-20e3-48aa-b5c5-8c07b9480b73")
	if err != nil {
		t.Fatalf("parse provider ID: %v", err)
	}
	createdAt := time.Date(2026, time.July, 23, 1, 2, 3, 0, time.UTC)
	queries := &fakeLegacyQueries{createdProvider: dbsqlc.ModelProvider{
		ID:         providerID,
		Name:       "OpenAI",
		ClientType: "openai-completions",
		Icon:       pgtype.Text{String: "openai", Valid: true},
		Enable:     true,
		Config:     []byte(`{"base_url":"https://example.test"}`),
		Metadata:   []byte(`{"source":"test"}`),
		CreatedAt:  pgtype.Timestamptz{Time: createdAt, Valid: true},
	}}
	store := NewStoreWithQueries(queries)

	record, err := store.CreateProvider(t.Context(), providerport.CreateProviderCommand{
		Name:       "OpenAI",
		ClientType: "openai-completions",
		Icon:       "openai",
		Enable:     true,
		Config:     []byte(`{"api_key":"secret"}`),
		Metadata:   []byte(`{"region":"test"}`),
	})
	if err != nil {
		t.Fatalf("CreateProvider() error = %v", err)
	}
	if !queries.createProviderInput.Icon.Valid || queries.createProviderInput.Icon.String != "openai" {
		t.Fatalf("CreateProvider() icon input = %#v", queries.createProviderInput.Icon)
	}
	if record.ID != providerID.String() || record.Icon != "openai" || !record.CreatedAt.Equal(createdAt) {
		t.Fatalf("CreateProvider() record = %#v", record)
	}
}

func TestStoreSaveOAuthTokenPreservesExpiryAndMetadata(t *testing.T) {
	queries := &fakeLegacyQueries{}
	store := NewStoreWithQueries(queries)
	expiresAt := time.Date(2026, time.July, 23, 4, 5, 6, 0, time.UTC)

	err := store.SaveOAuthToken(t.Context(), providerport.OAuthTokenRecord{
		ProviderID:       "3a16ce55-20e3-48aa-b5c5-8c07b9480b73",
		AccessToken:      "access-token",
		RefreshToken:     "refresh-token",
		ExpiresAt:        expiresAt,
		Scope:            "read:user",
		TokenType:        "bearer",
		State:            "state",
		PKCECodeVerifier: "verifier",
		Metadata:         map[string]any{"account_login": "octocat"},
	})
	if err != nil {
		t.Fatalf("SaveOAuthToken() error = %v", err)
	}
	input := queries.oauthUpsertInput
	if !input.ExpiresAt.Valid || !input.ExpiresAt.Time.Equal(expiresAt) {
		t.Fatalf("SaveOAuthToken() expiry = %#v", input.ExpiresAt)
	}
	var metadata map[string]any
	if err := json.Unmarshal(input.Metadata, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	if metadata["account_login"] != "octocat" || input.PkceCodeVerifier != "verifier" {
		t.Fatalf("SaveOAuthToken() input = %#v, metadata = %#v", input, metadata)
	}
}

func TestStoreMapsMissingRowsToDomainErrors(t *testing.T) {
	queries := &fakeLegacyQueries{providerReadError: pgx.ErrNoRows}
	store := NewStoreWithQueries(queries)

	_, err := store.GetProvider(t.Context(), "3a16ce55-20e3-48aa-b5c5-8c07b9480b73")
	if !errors.Is(err, providerport.ErrProviderNotFound) {
		t.Fatalf("GetProvider() error = %v, want ErrProviderNotFound", err)
	}
}
