package execution

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	modeldomain "github.com/memohai/memoh/domains/model"
	executionport "github.com/memohai/memoh/domains/model/internal/port/execution"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	providers "github.com/memohai/memoh/domains/model/provider"
	"github.com/memohai/memoh/internal/db"
)

type executionProviderQueries interface {
	GetProviderByID(context.Context, pgtype.UUID) (dbsqlc.ModelProvider, error)
}

type modelCredentialResolver interface {
	ResolveModelCredentials(context.Context, providers.ProviderRecord) (providers.ModelCredentials, error)
}

// ExecutionProviderSource adapts generated provider rows and credential
// resolution to the narrow executionport.ProviderSource consumed by Model.
type ExecutionProviderSource struct {
	queries     executionProviderQueries
	credentials modelCredentialResolver
}

var _ executionport.ProviderSource = (*ExecutionProviderSource)(nil)

func NewProviderSource(pool *pgxpool.Pool, credentials modelCredentialResolver) *ExecutionProviderSource {
	return &ExecutionProviderSource{queries: dbsqlc.New(pool), credentials: credentials}
}

// NewProviderSourceWithQueries creates a provider source with an injected query surface (tests).
func NewProviderSourceWithQueries(queries executionProviderQueries, credentials modelCredentialResolver) *ExecutionProviderSource {
	return &ExecutionProviderSource{queries: queries, credentials: credentials}
}

func (s *ExecutionProviderSource) LookupProvider(ctx context.Context, id string) (executionport.ProviderDescriptor, error) {
	row, err := s.lookup(ctx, id)
	if err != nil {
		return executionport.ProviderDescriptor{}, err
	}
	return providerDescriptor(row), nil
}

func (s *ExecutionProviderSource) ResolveProvider(ctx context.Context, id string) (executionport.Provider, error) {
	row, err := s.lookup(ctx, id)
	if err != nil {
		return executionport.Provider{}, err
	}
	if s.credentials == nil {
		return executionport.Provider{}, errors.New("model provider credentials are not configured")
	}
	credentials, err := s.credentials.ResolveModelCredentials(ctx, providerCredentialRecord(row))
	if err != nil {
		return executionport.Provider{}, executionport.SanitizeError(
			err,
			providers.ProviderConfigString(row.Config, "api_key"),
		)
	}
	return executionProvider(row, credentials), nil
}

func (s *ExecutionProviderSource) lookup(ctx context.Context, id string) (dbsqlc.ModelProvider, error) {
	if s == nil || s.queries == nil {
		return dbsqlc.ModelProvider{}, errors.New("model provider persistence is not configured")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return dbsqlc.ModelProvider{}, errors.New("provider id missing")
	}
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return dbsqlc.ModelProvider{}, err
	}
	row, err := s.queries.GetProviderByID(ctx, providerID)
	if errors.Is(err, pgx.ErrNoRows) {
		return dbsqlc.ModelProvider{}, executionport.ErrProviderNotFound
	}
	return row, err
}

func executionProvider(row dbsqlc.ModelProvider, credentials providers.ModelCredentials) executionport.Provider {
	storedAPIKey := providers.ProviderConfigString(row.Config, "api_key")
	return executionport.Provider{
		Name:                  row.Name,
		ClientType:            modeldomain.ClientType(row.ClientType),
		Enable:                row.Enable,
		BaseURL:               providers.ProviderConfigString(row.Config, "base_url"),
		APIKey:                credentials.APIKey,
		CodexAccountID:        credentials.CodexAccountID,
		PromptCacheTTL:        providers.ProviderConfigString(row.Config, "prompt_cache_ttl"),
		ChatCompletionsCompat: providers.ProviderConfigString(row.Config, "chat_completions_compat"),
		SensitiveValues:       []string{storedAPIKey, credentials.APIKey},
	}
}

func providerDescriptor(row dbsqlc.ModelProvider) executionport.ProviderDescriptor {
	return executionport.ProviderDescriptor{Name: row.Name}
}

func providerCredentialRecord(row dbsqlc.ModelProvider) providers.ProviderRecord {
	return providers.ProviderRecord{
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
