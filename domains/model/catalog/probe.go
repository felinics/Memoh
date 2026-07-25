package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
	"github.com/memohai/memoh/domains/model/execution"
)

const probeTimeout = execution.DefaultProviderProbeTimeout

// Test probes a model's provider endpoint using the Twilight AI SDK
// to verify connectivity, authentication, and model availability.
func (s *Service) Test(ctx context.Context, id string) (TestResponse, error) {
	if s == nil || s.store == nil {
		return TestResponse{}, errors.New("models persistence is not configured")
	}
	if s.providerResolver == nil {
		return TestResponse{}, errors.New("provider resolver is not configured")
	}
	model, err := s.store.GetByID(ctx, id)
	if err != nil {
		return TestResponse{}, fmt.Errorf("get model: %w", err)
	}

	provider, err := s.providerResolver.ResolveModelProvider(ctx, model.ProviderID)
	if err != nil {
		return TestResponse{}, fmt.Errorf("get provider: %w", err)
	}

	baseURL := strings.TrimRight(provider.BaseURL, "/")
	clientType := provider.ClientType

	if model.Type == modeldomain.ModelTypeEmbedding {
		return s.testEmbeddingModel(ctx, string(clientType), baseURL, provider.APIKey, model.ModelID, nil)
	}

	sdkProvider := execution.NewSDKProvider(baseURL, provider.APIKey, provider.CodexAccountID, clientType, probeTimeout, nil)

	start := time.Now()

	providerResult := sdkProvider.Test(ctx)
	switch providerResult.Status {
	case sdk.ProviderStatusUnreachable:
		return TestResponse{
			Status:    TestStatusError,
			Reachable: false,
			LatencyMs: time.Since(start).Milliseconds(),
			Message:   providerResult.Message,
		}, nil
	case sdk.ProviderStatusUnhealthy:
		return TestResponse{
			Status:    TestStatusAuthError,
			Reachable: true,
			LatencyMs: time.Since(start).Milliseconds(),
			Message:   providerResult.Message,
		}, nil
	}

	modelResult, err := sdkProvider.TestModel(ctx, model.ModelID)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return TestResponse{
			Status:    TestStatusError,
			Reachable: true,
			LatencyMs: latency,
			Message:   err.Error(),
		}, nil
	}

	if !modelResult.Supported {
		return TestResponse{
			Status:    TestStatusModelNotSupported,
			Reachable: true,
			LatencyMs: latency,
			Message:   modelResult.Message,
		}, nil
	}

	return TestResponse{
		Status:    TestStatusOK,
		Reachable: true,
		LatencyMs: latency,
		Message:   modelResult.Message,
	}, nil
}

// testEmbeddingModel probes an embedding model by performing a minimal
// embedding request via the Twilight SDK, verifying that the model is
// reachable and functional rather than merely checking HTTP connectivity.
func (*Service) testEmbeddingModel(ctx context.Context, clientType, baseURL, apiKey, modelID string, httpClient *http.Client) (TestResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	start := time.Now()
	_, err := execution.InferEmbeddingDimensions(ctx, clientType, baseURL, apiKey, modelID, probeTimeout, httpClient)
	latency := time.Since(start).Milliseconds()

	if err != nil {
		return TestResponse{
			Status:    TestStatusError,
			Reachable: false,
			LatencyMs: latency,
			Message:   err.Error(),
		}, nil
	}

	return TestResponse{
		Status:    TestStatusOK,
		Reachable: true,
		LatencyMs: latency,
		Message:   "embedding model is operational",
	}, nil
}
