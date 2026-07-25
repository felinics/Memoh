package execution

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	modeldomain "github.com/memohai/memoh/domains/model"
	executionport "github.com/memohai/memoh/domains/model/internal/port/execution"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	providers "github.com/memohai/memoh/domains/model/provider"
	"github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/oauth"
)

type executionProviderQueriesFake struct {
	row   dbsqlc.ModelProvider
	err   error
	id    pgtype.UUID
	calls int
}

func (f *executionProviderQueriesFake) GetProviderByID(_ context.Context, id pgtype.UUID) (dbsqlc.ModelProvider, error) {
	f.id = id
	f.calls++
	return f.row, f.err
}

type modelCredentialResolverFake struct {
	credentials providers.ModelCredentials
	err         error
	record      providers.ProviderRecord
	userID      string
	calls       int
}

func (f *modelCredentialResolverFake) ResolveModelCredentials(ctx context.Context, record providers.ProviderRecord) (providers.ModelCredentials, error) {
	f.calls++
	f.record = record
	f.userID = oauth.UserIDFromContext(ctx)
	return f.credentials, f.err
}

func TestExecutionProviderSourceSeparatesCatalogAndCredentialResolution(t *testing.T) {
	const providerID = "3a16ce55-20e3-48aa-b5c5-8c07b9480b73"
	id, err := db.ParseUUID(providerID)
	if err != nil {
		t.Fatal(err)
	}
	queries := &executionProviderQueriesFake{row: dbsqlc.ModelProvider{
		ID:         id,
		Name:       "provider-a",
		ClientType: string(modeldomain.ClientTypeOpenAICompletions),
		Enable:     true,
		Config: []byte(`{
			"api_key":"stored-key",
			"base_url":"https://api.example/v1",
			"prompt_cache_ttl":"24h",
			"chat_completions_compat":"strict"
		}`),
	}}
	credentials := &modelCredentialResolverFake{credentials: providers.ModelCredentials{
		APIKey:         "runtime-key",
		CodexAccountID: "account-1",
	}}
	source := NewProviderSourceWithQueries(queries, credentials)

	catalogProvider, err := source.LookupProvider(t.Context(), providerID)
	if err != nil {
		t.Fatalf("LookupProvider() error = %v", err)
	}
	if catalogProvider.Name != "provider-a" {
		t.Fatalf("LookupProvider() = %#v", catalogProvider)
	}
	if credentials.calls != 0 {
		t.Fatalf("LookupProvider() credential calls = %d, want 0", credentials.calls)
	}

	ctx := oauth.WithUserID(t.Context(), "user-1")
	resolved, err := source.ResolveProvider(ctx, providerID)
	if err != nil {
		t.Fatalf("ResolveProvider() error = %v", err)
	}
	if resolved.ClientType != modeldomain.ClientTypeOpenAICompletions || resolved.APIKey != "runtime-key" || resolved.CodexAccountID != "account-1" {
		t.Fatalf("ResolveProvider() = %#v", resolved)
	}
	if resolved.PromptCacheTTL != "24h" || resolved.ChatCompletionsCompat != "strict" {
		t.Fatalf("ResolveProvider() policy = %#v", resolved)
	}
	if !slices.Equal(resolved.SensitiveValues, []string{"stored-key", "runtime-key"}) {
		t.Fatalf("ResolveProvider() sensitive values = %q", resolved.SensitiveValues)
	}
	if credentials.calls != 1 || credentials.userID != "user-1" || credentials.record.ID != providerID {
		t.Fatalf("credential resolution = calls %d, user %q, record %#v", credentials.calls, credentials.userID, credentials.record)
	}
	if queries.calls != 2 || queries.id != id {
		t.Fatalf("provider queries = calls %d, id %s", queries.calls, queries.id.String())
	}
}

func TestExecutionProviderSourceSanitizesCredentialErrors(t *testing.T) {
	const (
		providerID = "3a16ce55-20e3-48aa-b5c5-8c07b9480b73"
		secret     = "secret-1234567890"
	)
	id, err := db.ParseUUID(providerID)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("credential rejected: " + secret)
	source := NewProviderSourceWithQueries(
		&executionProviderQueriesFake{row: dbsqlc.ModelProvider{ID: id, Config: []byte(`{"api_key":"` + secret + `"}`)}},
		&modelCredentialResolverFake{err: sentinel},
	)

	_, err = source.ResolveProvider(t.Context(), providerID)
	if !errors.Is(err, sentinel) {
		t.Fatalf("ResolveProvider() error = %v, want wrapped sentinel", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("ResolveProvider() leaked credential: %v", err)
	}
}

func TestExecutionProviderSourceRejectsMissingIDBeforeQuery(t *testing.T) {
	queries := &executionProviderQueriesFake{}
	source := NewProviderSourceWithQueries(queries, &modelCredentialResolverFake{})

	_, err := source.LookupProvider(t.Context(), "  ")
	if err == nil || err.Error() != "provider id missing" {
		t.Fatalf("LookupProvider() error = %v", err)
	}
	if queries.calls != 0 {
		t.Fatalf("provider query calls = %d, want 0", queries.calls)
	}
}

func TestExecutionProviderSourceClassifiesMissingProvider(t *testing.T) {
	source := NewProviderSourceWithQueries(
		&executionProviderQueriesFake{err: pgx.ErrNoRows},
		&modelCredentialResolverFake{},
	)

	_, err := source.LookupProvider(t.Context(), "3a16ce55-20e3-48aa-b5c5-8c07b9480b73")
	if !errors.Is(err, executionport.ErrProviderNotFound) {
		t.Fatalf("LookupProvider() error = %v, want ErrProviderNotFound", err)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("LookupProvider() leaked pgx.ErrNoRows: %v", err)
	}
}
