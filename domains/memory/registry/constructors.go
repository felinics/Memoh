package registry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	memport "github.com/memohai/memoh/domains/memory/internal/port"
	memoryvector "github.com/memohai/memoh/domains/memory/internal/postgres/vector"
	wikipostgres "github.com/memohai/memoh/domains/memory/internal/postgres/wiki"
	"github.com/memohai/memoh/domains/memory/internal/provider/builtin"
	"github.com/memohai/memoh/domains/memory/internal/provider/mem0"
	"github.com/memohai/memoh/domains/memory/internal/provider/openviking"
	storefs "github.com/memohai/memoh/domains/memory/internal/store/fs"
	wikistore "github.com/memohai/memoh/domains/memory/internal/store/wiki"
	memprovider "github.com/memohai/memoh/domains/memory/provider"
	bridge "github.com/memohai/memoh/domains/runtime/bridge/client"
)

// EmbeddingModelSpec is the persistence-neutral embedding model state Memory
// needs to construct an embedding client.
type EmbeddingModelSpec struct {
	ID         string
	ModelID    string
	Type       string
	Enabled    bool
	Dimensions int
	ProviderID string
	ClientType string
	BaseURL    string
	APIKey     string //nolint:gosec // runtime credential passed to the embedding SDK.
}

// EmbeddingModelResolver joins model catalog state with provider credentials.
type EmbeddingModelResolver interface {
	ResolveEmbeddingModel(context.Context, string) (EmbeddingModelSpec, error)
	EmbeddingModelEnabled(context.Context, string) (bool, error)
}

// PGVectorConfig carries connection settings for the optional semantic seed index.
type PGVectorConfig struct {
	Enabled  bool
	Host     string
	Port     int
	User     string
	Password string
	Database string
	SSLMode  string
}

// Deps are the explicit public inputs required to assemble a runtime Registry.
type Deps struct {
	Log             *slog.Logger
	Pool            *pgxpool.Pool
	Bridge          bridge.Provider
	LLM             memprovider.LLM
	EmbeddingModels EmbeddingModelResolver
	PGVector        PGVectorConfig
	TeamIDResolver  memprovider.TeamIDResolver
}

// NewRegistry assembles a Registry with builtin/mem0/openviking factories and
// the default builtin fallback. The returned cleanup closes the optional
// pgvector store; always call it on process shutdown (in addition to Registry.Close).
func NewPostgresRegistry(deps Deps) (*Registry, func(), error) {
	log := deps.Log
	if log == nil {
		log = slog.Default()
	}
	var resolvers []memprovider.TeamIDResolver
	if deps.TeamIDResolver != nil {
		resolvers = append(resolvers, deps.TeamIDResolver)
	}
	reg := NewRegistry(log, resolvers...)
	fileStore := storefs.New(log, deps.Bridge)

	cleanup := func() {}
	var vectorStore memport.SemanticEmbeddingStore
	if deps.PGVector.Enabled {
		store, err := memoryvector.Open(context.Background(), memoryvector.Config{
			Host:     deps.PGVector.Host,
			Port:     deps.PGVector.Port,
			User:     deps.PGVector.User,
			Password: deps.PGVector.Password,
			Database: deps.PGVector.Database,
			SSLMode:  deps.PGVector.SSLMode,
		})
		if err != nil {
			return nil, nil, fmt.Errorf("open memory vector store: %w", err)
		}
		vectorStore = store
		cleanup = store.Close
	}

	var wikiStore wikistore.Store
	if deps.Pool != nil {
		if ws := wikipostgres.NewStoreFromPool(deps.Pool); ws != nil {
			wikiStore = ws
		}
	}

	embeddingModels := embeddingModelResolverAdapter{inner: deps.EmbeddingModels}

	reg.RegisterFactory(memprovider.ProviderBuiltin, func(ctx context.Context, teamID, _ string, providerConfig map[string]any) (memprovider.Instance, error) {
		if wikiStore == nil {
			return nil, errors.New("graph runtime: wiki store not configured")
		}
		runtime, err := builtin.NewBuiltinRuntimeFromConfigContext(ctx, log, providerConfig, fileStore, embeddingModels, vectorStore, wikiStore, memprovider.FixedTeamIDResolver(teamID))
		if err != nil {
			return nil, err
		}
		p := builtin.NewBuiltinProvider(log, runtime)
		p.SetLLM(deps.LLM)
		p.ApplyProviderConfig(providerConfig)
		return p, nil
	})
	reg.RegisterFactory(memprovider.ProviderMem0, func(_ context.Context, _, _ string, providerConfig map[string]any) (memprovider.Instance, error) {
		return mem0.NewMem0Provider(log, providerConfig, fileStore)
	})
	reg.RegisterFactory(memprovider.ProviderOpenViking, func(_ context.Context, _, _ string, providerConfig map[string]any) (memprovider.Instance, error) {
		return openviking.NewOpenVikingProvider(log, providerConfig)
	})

	var defaultRuntime builtin.Runtime
	if wikiStore != nil {
		defaultRuntime = builtin.NewGraphRuntime(log, wikiStore, fileStore)
	} else {
		defaultRuntime = builtin.NewFileRuntime(fileStore)
	}
	defaultProvider := builtin.NewBuiltinProvider(log, defaultRuntime)
	defaultProvider.SetLLM(deps.LLM)
	reg.Register(memprovider.DefaultBuiltinProviderID, defaultProvider)
	return reg, cleanup, nil
}

type embeddingModelResolverAdapter struct {
	inner EmbeddingModelResolver
}

func (a embeddingModelResolverAdapter) ResolveEmbeddingModel(ctx context.Context, ref string) (builtin.EmbeddingModelSpec, error) {
	if a.inner == nil {
		return builtin.EmbeddingModelSpec{}, errors.New("embedding model resolver not configured")
	}
	spec, err := a.inner.ResolveEmbeddingModel(ctx, ref)
	if err != nil {
		return builtin.EmbeddingModelSpec{}, err
	}
	return builtin.EmbeddingModelSpec(spec), nil
}

func (a embeddingModelResolverAdapter) EmbeddingModelEnabled(ctx context.Context, ref string) (bool, error) {
	if a.inner == nil {
		return false, nil
	}
	return a.inner.EmbeddingModelEnabled(ctx, ref)
}
