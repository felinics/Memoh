package assembly

import (
	"context"
	"fmt"

	"github.com/memohai/memoh/domains/agent/chat/usage"
	"github.com/memohai/memoh/domains/model/catalog"
	"github.com/memohai/memoh/domains/model/provider"
)

type usageModelCatalog interface {
	List(context.Context) ([]catalog.GetResponse, error)
}

type usageProviderCatalog interface {
	List(context.Context) ([]provider.GetResponse, error)
}

type usageModelReader struct {
	models    usageModelCatalog
	providers usageProviderCatalog
}

// NewUsageModelReader adapts the public Model catalogs to Agent usage's batch port.
func NewUsageModelReader(models *catalog.Service, providers *provider.Service) usage.ModelProjectionReader {
	return newUsageModelReader(models, providers)
}

func newUsageModelReader(models usageModelCatalog, providers usageProviderCatalog) usage.ModelProjectionReader {
	return &usageModelReader{models: models, providers: providers}
}

func (r *usageModelReader) GetModelProjections(ctx context.Context, modelIDs []string) (map[string]usage.ModelProjection, error) {
	if len(modelIDs) == 0 {
		return map[string]usage.ModelProjection{}, nil
	}
	modelRows, err := r.models.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list usage models: %w", err)
	}
	providerRows, err := r.providers.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list usage model providers: %w", err)
	}

	wanted := make(map[string]struct{}, len(modelIDs))
	for _, id := range modelIDs {
		wanted[id] = struct{}{}
	}
	providerNames := make(map[string]string, len(providerRows))
	for _, row := range providerRows {
		providerNames[row.ID] = row.Name
	}
	projections := make(map[string]usage.ModelProjection, len(wanted))
	for _, row := range modelRows {
		if _, ok := wanted[row.ID]; !ok {
			continue
		}
		projections[row.ID] = usage.ModelProjection{
			ModelSlug:    row.ModelID,
			ModelName:    row.Name,
			ProviderName: providerNames[row.ProviderID],
		}
	}
	return projections, nil
}
