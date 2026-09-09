package models

import (
	"context"
	"errors"
	"net/http"
	"time"

	googleembedding "github.com/felinics/twilight/provider/google/embedding"
	openaiembedding "github.com/felinics/twilight/provider/openai/embedding"
	sdk "github.com/felinics/twilight/sdk"
)

// NewSDKEmbeddingModel creates a Twilight AI SDK EmbeddingModel for the given
// provider configuration. It dispatches to the native Google embedding provider
// when clientType is "google-generative-ai", and falls back to the
// OpenAI-compatible /embeddings endpoint for all other provider types.
func NewSDKEmbeddingModel(clientType, baseURL, apiKey, modelID string, timeout time.Duration, httpClient *http.Client) *sdk.EmbeddingModel {
	if timeout <= 0 {
		timeout = DefaultProviderRequestTimeout
	}
	if httpClient == nil {
		httpClient = NewProviderHTTPClient(timeout)
	}

	switch ClientType(clientType) {
	case ClientTypeGoogleGenerativeAI:
		opts := []googleembedding.Option{
			googleembedding.WithAPIKey(apiKey),
			googleembedding.WithHTTPClient(httpClient),
		}
		if baseURL != "" {
			opts = append(opts, googleembedding.WithBaseURL(baseURL))
		}
		p := googleembedding.New(opts...)
		return p.EmbeddingModel(modelID)
	default:
		opts := []openaiembedding.Option{
			openaiembedding.WithAPIKey(apiKey),
			openaiembedding.WithHTTPClient(httpClient),
		}
		if baseURL != "" {
			opts = append(opts, openaiembedding.WithBaseURL(baseURL))
		}
		p := openaiembedding.New(opts...)
		return p.EmbeddingModel(modelID)
	}
}

// InferEmbeddingDimensions probes the embedding endpoint and returns the vector
// length produced by the provider for a minimal input.
func InferEmbeddingDimensions(ctx context.Context, clientType, baseURL, apiKey, modelID string, timeout time.Duration, httpClient *http.Client) (int, error) {
	model := NewSDKEmbeddingModel(clientType, baseURL, apiKey, modelID, timeout, httpClient)
	client := sdk.NewClient()
	vector, err := client.Embed(ctx, "dimensions", sdk.WithEmbeddingModel(model))
	if err != nil {
		return 0, err
	}
	if len(vector) == 0 {
		return 0, errors.New("embedding provider returned no vector values")
	}
	return len(vector), nil
}
