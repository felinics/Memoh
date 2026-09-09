package workspacedeps

import (
	"context"

	"github.com/felinics/memoh/internal/workspacedeps/catalog"
)

type (
	catalogContextKey  struct{}
	revisionContextKey struct{}
)

// WithDefinitionRevision binds a user's prepared operation to the immutable
// revision returned by catalog metadata or an optional script preview. Empty
// means resolve latest when the operation begins; it never changes halfway
// through a running script.
func WithDefinitionRevision(ctx context.Context, revision string) context.Context {
	return context.WithValue(ctx, revisionContextKey{}, revision)
}

func (s *Service) prepareCatalog(ctx context.Context, refresh, offline bool) (context.Context, CatalogResult, error) {
	if result, ok := ctx.Value(catalogContextKey{}).(CatalogResult); ok {
		return ctx, result, nil
	}
	result := CatalogResult{Catalog: s.catalog}
	var err error
	if s.provider != nil {
		if offline {
			result, err = s.provider.Cached(ctx)
		} else {
			result, err = s.provider.Snapshot(ctx, refresh)
		}
	}
	if err != nil {
		return ctx, CatalogResult{}, err
	}
	return context.WithValue(ctx, catalogContextKey{}, result), result, nil
}

func (s *Service) catalogFor(ctx context.Context) *catalog.Catalog {
	if result, ok := ctx.Value(catalogContextKey{}).(CatalogResult); ok {
		return result.Catalog
	}
	return s.catalog
}

func (s *Service) operationCatalog(ctx context.Context, depID string) (*catalog.Catalog, error) {
	revision, _ := ctx.Value(revisionContextKey{}).(string)
	// A revision returned by preparation is already the choice for this
	// operation. Do not re-resolve latest between preview and execution.
	ctx, result, err := s.prepareCatalog(ctx, revision == "", revision != "")
	if err != nil {
		return nil, err
	}
	if revision == "" || s.provider == nil {
		return result.Catalog, nil
	}
	definition, err := s.provider.Definition(ctx, depID, revision)
	if err != nil {
		return nil, err
	}
	if current, ok := result.Catalog.Get(depID); ok {
		definition = definition.WithRetired(current.Retired)
	} else {
		definition = definition.WithRetired(true)
	}
	return result.Catalog.Using(definition)
}
