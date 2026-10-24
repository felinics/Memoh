package fetchproviders

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type fetchProviderQueries struct {
	dbstore.Queries
	created []sqlc.CreateFetchProviderParams
}

func (*fetchProviderQueries) ListFetchProviders(context.Context) ([]sqlc.FetchProvider, error) {
	return nil, nil
}

func (q *fetchProviderQueries) CreateFetchProvider(_ context.Context, params sqlc.CreateFetchProviderParams) (sqlc.FetchProvider, error) {
	q.created = append(q.created, params)
	return sqlc.FetchProvider{Name: params.Name, Provider: params.Provider, Config: params.Config, Enable: params.Enable}, nil
}

func TestDeepSeekProviderCatalogAndCreation(t *testing.T) {
	queries := &fetchProviderQueries{}
	service := NewService(slog.New(slog.DiscardHandler), queries)
	var found bool
	for _, meta := range service.ListMeta(context.Background()) {
		if meta.Provider != "deepseek" {
			continue
		}
		found = true
		if meta.DisplayName != "DeepSeek Fetch" {
			t.Errorf("display name = %q", meta.DisplayName)
		}
		key := meta.ConfigSchema.Fields["api_key"]
		if key.Type != "secret" || !key.Required {
			t.Errorf("api key = %+v, want required secret", key)
		}
		if got := meta.ConfigSchema.Fields["base_url"].Example; got != "https://api.deepseek.com" {
			t.Errorf("base_url = %v", got)
		}
		if got := meta.ConfigSchema.Fields["timeout_seconds"].Type; got != "number" {
			t.Errorf("timeout type = %q", got)
		}
	}
	if !found {
		t.Error("DeepSeek Fetch is absent from metadata")
	}
	created, err := service.Create(context.Background(), CreateRequest{Name: "DeepSeek Fetch", Provider: "deepseek", Config: map[string]any{"api_key": "key"}})
	if err != nil {
		t.Fatalf("create DeepSeek Fetch: %v", err)
	}
	if created.Provider != "deepseek" || created.Enable || created.Config["api_key"] != "key" {
		t.Errorf("created = %+v, want disabled configured DeepSeek provider", created)
	}
}

func TestEnsureDefaultsIncludesDisabledDeepSeekProvider(t *testing.T) {
	queries := &fetchProviderQueries{}
	service := NewService(slog.New(slog.DiscardHandler), queries)
	if err := service.EnsureDefaults(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, provider := range queries.created {
		if provider.Provider == "deepseek" {
			if provider.Name != "DeepSeek Fetch" || provider.Enable {
				t.Errorf("default = %+v, want disabled DeepSeek Fetch", provider)
			}
			return
		}
	}
	t.Fatal("DeepSeek Fetch default was not created")
}

func TestCreateRejectsUnknownProvider(t *testing.T) {
	_, err := (&Service{}).Create(context.Background(), CreateRequest{Name: "x", Provider: "bogus"})
	if !errors.Is(err, ErrInvalidProvider) {
		t.Fatalf("errors.Is(%v, ErrInvalidProvider) = false", err)
	}
	if err.Error() != "invalid provider: bogus" {
		t.Fatalf("message changed: %q", err.Error())
	}
}
