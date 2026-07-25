package provider

import (
	"context"
	"errors"
	"net/http"
	"time"

	anthropicmessages "github.com/memohai/twilight-ai/provider/anthropic/messages"
	googleembedding "github.com/memohai/twilight-ai/provider/google/embedding"
	googlegenerative "github.com/memohai/twilight-ai/provider/google/generativeai"
	openaicodex "github.com/memohai/twilight-ai/provider/openai/codex"
	openaicompletions "github.com/memohai/twilight-ai/provider/openai/completions"
	openaiembedding "github.com/memohai/twilight-ai/provider/openai/embedding"
	openairesponses "github.com/memohai/twilight-ai/provider/openai/responses"
	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
)

type defaultProbeSDK struct{}

func (defaultProbeSDK) NewProvider(baseURL, apiKey, codexAccountID string, clientType modeldomain.ClientType, timeout time.Duration, httpClient *http.Client) sdk.Provider {
	return NewSDKProvider(baseURL, apiKey, codexAccountID, clientType, timeout, httpClient)
}

func (defaultProbeSDK) InferEmbeddingDimensions(ctx context.Context, clientType, baseURL, apiKey, modelID string, timeout time.Duration, httpClient *http.Client) (int, error) {
	return InferEmbeddingDimensions(ctx, clientType, baseURL, apiKey, modelID, timeout, httpClient)
}

func (s *Service) resolveProbe() ProbeSDK {
	if s != nil && s.probe != nil {
		return s.probe
	}
	return defaultProbeSDK{}
}

// NewSDKProvider creates a Twilight AI SDK Provider for the given client type.
func NewSDKProvider(baseURL, apiKey, codexAccountID string, clientType modeldomain.ClientType, timeout time.Duration, httpClient *http.Client) sdk.Provider {
	if httpClient == nil {
		if timeout <= 0 {
			timeout = DefaultProbeTimeout
		}
		httpClient = &http.Client{Timeout: timeout}
	}

	switch clientType {
	case modeldomain.ClientTypeOpenAIResponses:
		opts := []openairesponses.Option{
			openairesponses.WithAPIKey(apiKey),
			openairesponses.WithHTTPClient(httpClient),
		}
		if baseURL != "" {
			opts = append(opts, openairesponses.WithBaseURL(baseURL))
		}
		return openairesponses.New(opts...)

	case modeldomain.ClientTypeOpenAICodex:
		opts := []openaicodex.Option{
			openaicodex.WithAccessToken(apiKey),
			openaicodex.WithHTTPClient(httpClient),
		}
		if codexAccountID != "" {
			opts = append(opts, openaicodex.WithAccountID(codexAccountID))
		}
		return openaicodex.New(opts...)

	case modeldomain.ClientTypeGitHubCopilot:
		return NewCopilotSDKProvider(apiKey, httpClient)

	case modeldomain.ClientTypeAnthropicMessages:
		opts := []anthropicmessages.Option{
			anthropicmessages.WithAPIKey(apiKey),
			anthropicmessages.WithHTTPClient(httpClient),
		}
		if baseURL != "" {
			opts = append(opts, anthropicmessages.WithBaseURL(baseURL))
		}
		return anthropicmessages.New(opts...)

	case modeldomain.ClientTypeGoogleGenerativeAI:
		opts := []googlegenerative.Option{
			googlegenerative.WithAPIKey(apiKey),
			googlegenerative.WithHTTPClient(httpClient),
		}
		if baseURL != "" {
			opts = append(opts, googlegenerative.WithBaseURL(baseURL))
		}
		return googlegenerative.New(opts...)

	default:
		opts := []openaicompletions.Option{
			openaicompletions.WithAPIKey(apiKey),
			openaicompletions.WithHTTPClient(httpClient),
		}
		if baseURL != "" {
			opts = append(opts, openaicompletions.WithBaseURL(baseURL))
		}
		return openaicompletions.New(opts...)
	}
}

// NewSDKEmbeddingModel creates a Twilight embedding model for dimension probes.
func NewSDKEmbeddingModel(clientType, baseURL, apiKey, modelID string, timeout time.Duration, httpClient *http.Client) *sdk.EmbeddingModel {
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: timeout}
	}

	switch modeldomain.ClientType(clientType) {
	case modeldomain.ClientTypeGoogleGenerativeAI:
		opts := []googleembedding.Option{
			googleembedding.WithAPIKey(apiKey),
			googleembedding.WithHTTPClient(httpClient),
		}
		if baseURL != "" {
			opts = append(opts, googleembedding.WithBaseURL(baseURL))
		}
		return googleembedding.New(opts...).EmbeddingModel(modelID)
	default:
		opts := []openaiembedding.Option{
			openaiembedding.WithAPIKey(apiKey),
			openaiembedding.WithHTTPClient(httpClient),
		}
		if baseURL != "" {
			opts = append(opts, openaiembedding.WithBaseURL(baseURL))
		}
		return openaiembedding.New(opts...).EmbeddingModel(modelID)
	}
}

// InferEmbeddingDimensions probes the embedding endpoint and returns vector length.
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
