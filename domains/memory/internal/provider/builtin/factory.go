package builtin

import (
	"context"
	"errors"
	"log/slog"

	memport "github.com/memohai/memoh/domains/memory/internal/port"
	storefs "github.com/memohai/memoh/domains/memory/internal/store/fs"
	wikistore "github.com/memohai/memoh/domains/memory/internal/store/wiki"
	memreg "github.com/memohai/memoh/domains/memory/registry"
)

// EmbeddingModelSpec is the persistence-neutral model and provider state the
// builtin semantic index needs to construct an embedding client.
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

// EmbeddingModelResolver joins model state with the provider credential needed
// by Memory. EmbeddingModelEnabled intentionally remains a model-only lookup so
// runtime health checks do not re-resolve or mask provider credentials.
type EmbeddingModelResolver interface {
	ResolveEmbeddingModel(context.Context, string) (EmbeddingModelSpec, error)
	EmbeddingModelEnabled(context.Context, string) (bool, error)
}

// NewBuiltinRuntimeFromConfig returns the graph Runtime for the provider's
// persisted config. Graph is the only supported mode: PG memory nodes/edges are
// the source of truth, with Markdown as a derived view. Returns an error if
// the wiki store is not configured.
//
// modelResolver resolves the optional embedding_model_id without exposing its
// persistence representation. The pgvector semantic seed index itself uses the
// dedicated [pgvector] database, so Local stores intentionally run graph-only.
func NewBuiltinRuntimeFromConfig(logger *slog.Logger, providerConfig map[string]any, store *storefs.Service, modelResolver EmbeddingModelResolver, vectorStore memport.SemanticEmbeddingStore, wikiStore wikistore.Store) (Runtime, error) {
	return NewBuiltinRuntimeFromConfigContext(context.Background(), logger, providerConfig, store, modelResolver, vectorStore, wikiStore, nil)
}

// NewBuiltinRuntimeFromConfigContext builds a team-owned runtime. resolver is
// fixed by the registry at provider instantiation time so asynchronous index
// retries retain the same team after the request context is gone.
func NewBuiltinRuntimeFromConfigContext(ctx context.Context, logger *slog.Logger, providerConfig map[string]any, store *storefs.Service, modelResolver EmbeddingModelResolver, vectorStore memport.SemanticEmbeddingStore, wikiStore wikistore.Store, resolver memreg.TeamIDResolver) (Runtime, error) {
	if wikiStore == nil {
		return nil, errors.New("graph runtime: wiki store not configured")
	}
	runtime := NewGraphRuntime(logger, wikiStore, store)
	semantic, err := newPGVectorIndex(ctx, logger, providerConfig, modelResolver, vectorStore, resolver)
	if err != nil {
		return nil, err
	}
	runtime.SetSemanticIndex(ctx, semantic)
	return runtime, nil
}
