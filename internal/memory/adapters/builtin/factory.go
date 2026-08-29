package builtin

import (
	"context"
	"errors"
	"log/slog"

	pgvectordb "github.com/felinics/memoh/internal/db/pgvector"
	dbstore "github.com/felinics/memoh/internal/db/store"
	adapters "github.com/felinics/memoh/internal/memory/adapters"
	storefs "github.com/felinics/memoh/internal/memory/storefs"
	"github.com/felinics/memoh/internal/memory/wikistore"
)

// NewBuiltinRuntimeFromConfig returns the graph Runtime for the provider's
// persisted config. Graph is the only supported mode: PG memory nodes/edges are
// the source of truth, with Markdown as a derived view. Returns an error if
// the wiki store is not configured.
//
// queries is used to resolve the optional embedding_model_id from the main
// relational store. The pgvector semantic seed index itself uses the dedicated
// [pgvector] database, so Local stores intentionally run graph-only.
func NewBuiltinRuntimeFromConfig(logger *slog.Logger, providerConfig map[string]any, store *storefs.Service, queries dbstore.Queries, vectorStore *pgvectordb.Store, wikiStore wikistore.Store) (Runtime, error) {
	return NewBuiltinRuntimeFromConfigContext(context.Background(), logger, providerConfig, store, queries, vectorStore, wikiStore, nil)
}

// NewBuiltinRuntimeFromConfigContext builds a team-owned runtime. resolver is
// fixed by the registry at provider instantiation time so asynchronous index
// retries retain the same team after the request context is gone.
func NewBuiltinRuntimeFromConfigContext(ctx context.Context, logger *slog.Logger, providerConfig map[string]any, store *storefs.Service, queries dbstore.Queries, vectorStore *pgvectordb.Store, wikiStore wikistore.Store, resolver adapters.TeamIDResolver) (Runtime, error) {
	if wikiStore == nil {
		return nil, errors.New("graph runtime: wiki store not configured")
	}
	runtime := NewGraphRuntime(logger, wikiStore, store)
	semantic, err := newPGVectorIndex(ctx, logger, providerConfig, queries, vectorStore, resolver)
	if err != nil {
		return nil, err
	}
	runtime.SetSemanticIndex(ctx, semantic)
	return runtime, nil
}
