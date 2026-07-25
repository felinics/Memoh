package catalog

import (
	"context"

	modeldomain "github.com/memohai/memoh/domains/model"
	"github.com/memohai/memoh/domains/model/execution"
)

// ExecutionModelReader adapts catalog.Service to execution.ModelReader without
// exposing catalog DTOs into the execution package.
func (s *Service) ExecutionModelReader() execution.ModelReader {
	return executionModelReader{svc: s}
}

type executionModelReader struct {
	svc *Service
}

func (r executionModelReader) GetByID(ctx context.Context, id string) (execution.ModelSnapshot, error) {
	resp, err := r.svc.GetByID(ctx, id)
	if err != nil {
		return execution.ModelSnapshot{}, err
	}
	return snapshotFromResponse(resp), nil
}

func (r executionModelReader) ListEnabledByType(ctx context.Context, modelType modeldomain.ModelType) ([]execution.ModelSnapshot, error) {
	items, err := r.svc.ListEnabledByType(ctx, modelType)
	if err != nil {
		return nil, err
	}
	out := make([]execution.ModelSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, snapshotFromResponse(item))
	}
	return out, nil
}

func snapshotFromResponse(resp GetResponse) execution.ModelSnapshot {
	return execution.ModelSnapshot{
		ID:         resp.ID,
		ModelID:    resp.ModelID,
		ProviderID: resp.ProviderID,
		Type:       resp.Type,
		Enable:     resp.Enable,
		Config:     resp.Config,
	}
}
