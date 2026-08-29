package models

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	anthropicmessages "github.com/felinics/twilight/provider/anthropic/messages"
	googlegenerative "github.com/felinics/twilight/provider/google/generativeai"
	openaicodex "github.com/felinics/twilight/provider/openai/codex"
	openaicompletions "github.com/felinics/twilight/provider/openai/completions"
	openairesponses "github.com/felinics/twilight/provider/openai/responses"
	sdk "github.com/felinics/twilight/sdk"

	memohcopilot "github.com/felinics/memoh/internal/copilot"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const probeTimeout = DefaultProviderProbeTimeout

// Test probes a model's provider endpoint using the Twilight AI SDK
// to verify connectivity, authentication, and model availability.
func (s *Service) Test(ctx context.Context, id string) (TestResponse, error) {
	modelID, err := db.ParseUUID(id)
	if err != nil {
		return TestResponse{}, fmt.Errorf("invalid model id: %w", err)
	}

	model, err := s.queries.GetModelByID(ctx, modelID)
	if err != nil {
		return TestResponse{}, fmt.Errorf("get model: %w", err)
	}

	provider, err := s.queries.GetProviderByID(ctx, model.ProviderID)
	if err != nil {
		return TestResponse{}, fmt.Errorf("get provider: %w", err)
	}

	baseURL := strings.TrimRight(providerConfigString(provider.Config, "base_url"), "/")
	clientType := ClientType(provider.ClientType)
	creds, err := s.resolveModelCredentials(ctx, provider)
	if err != nil {
		return TestResponse{}, err
	}

	if model.Type == string(ModelTypeEmbedding) {
		return s.testEmbeddingModel(ctx, string(clientType), baseURL, creds.APIKey, model.ModelID, nil)
	}

	sdkProvider := NewSDKProvider(baseURL, creds.APIKey, creds.CodexAccountID, clientType, probeTimeout, nil)

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
		// Only an auth failure justifies an early verdict. Any other
		// unhealthy result (e.g. 404 because the provider does not implement
		// the models list at all) must not be reported as "Invalid API key" —
		// fall through to the real-model generation probe, which is the only
		// check that can give a definitive answer for such providers (#1087).
		if strings.Contains(providerResult.Message, "authentication failed") {
			return TestResponse{
				Status:    TestStatusAuthError,
				Reachable: true,
				LatencyMs: time.Since(start).Milliseconds(),
				Message:   providerResult.Message,
			}, nil
		}
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
	_, err := InferEmbeddingDimensions(ctx, clientType, baseURL, apiKey, modelID, probeTimeout, httpClient)
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

// NewSDKProvider creates a Twilight AI SDK Provider for the given client type.
// It is exported so that other packages (e.g. providers) can reuse it for testing.
func NewSDKProvider(baseURL, apiKey, codexAccountID string, clientType ClientType, timeout time.Duration, httpClient *http.Client) sdk.Provider {
	if httpClient == nil {
		httpClient = NewProviderHTTPClient(timeout)
	}

	switch clientType {
	case ClientTypeOpenAIResponses:
		opts := []openairesponses.Option{
			openairesponses.WithAPIKey(apiKey),
			openairesponses.WithHTTPClient(httpClient),
		}
		if baseURL != "" {
			opts = append(opts, openairesponses.WithBaseURL(baseURL))
		}
		return openairesponses.New(opts...)

	case ClientTypeOpenAICodex:
		opts := []openaicodex.Option{
			openaicodex.WithAccessToken(apiKey),
			openaicodex.WithHTTPClient(httpClient),
		}
		if codexAccountID != "" {
			opts = append(opts, openaicodex.WithAccountID(codexAccountID))
		}
		return openaicodex.New(opts...)

	case ClientTypeGitHubCopilot:
		return memohcopilot.NewProvider(apiKey, httpClient)

	case ClientTypeAnthropicMessages:
		opts := []anthropicmessages.Option{
			anthropicmessages.WithAPIKey(apiKey),
			anthropicmessages.WithHTTPClient(httpClient),
		}
		if baseURL := anthropicMessagesBaseURL(baseURL); baseURL != "" {
			opts = append(opts, anthropicmessages.WithBaseURL(baseURL))
		}
		return anthropicmessages.New(opts...)

	case ClientTypeGoogleGenerativeAI:
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

type modelCredentials struct {
	APIKey         string //nolint:gosec // runtime credential material used to construct SDK providers
	CodexAccountID string
}

func (s *Service) resolveModelCredentials(ctx context.Context, provider sqlc.Provider) (modelCredentials, error) {
	apiKey := providerConfigString(provider.Config, "api_key")

	switch ClientType(provider.ClientType) {
	case ClientTypeGitHubCopilot:
		token, err := s.resolveGitHubCopilotAccessToken(ctx, provider)
		if err != nil {
			return modelCredentials{}, err
		}
		return modelCredentials{APIKey: token}, nil

	case ClientTypeOpenAICodex:
		tokenRow, err := s.queries.GetProviderOAuthTokenByProvider(ctx, provider.ID)
		if err != nil {
			return modelCredentials{}, err
		}
		accessToken := strings.TrimSpace(tokenRow.AccessToken)
		if accessToken == "" {
			return modelCredentials{}, errors.New("oauth token is missing access token")
		}
		accountID, err := codexAccountIDFromToken(accessToken)
		if err != nil {
			return modelCredentials{}, err
		}
		return modelCredentials{
			APIKey:         accessToken,
			CodexAccountID: accountID,
		}, nil

	default:
		return modelCredentials{APIKey: apiKey}, nil
	}
}

func (s *Service) resolveGitHubCopilotAccessToken(ctx context.Context, provider sqlc.Provider) (string, error) {
	row, err := s.queries.GetProviderOAuthTokenByProvider(ctx, provider.ID)
	if err != nil {
		return "", err
	}
	accessToken := strings.TrimSpace(row.AccessToken)
	if accessToken == "" {
		return "", errors.New("oauth token is missing access token")
	}
	copilotToken, err := memohcopilot.ResolveToken(ctx, accessToken)
	if err != nil {
		return "", err
	}
	return copilotToken, nil
}

func codexAccountIDFromToken(token string) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("invalid oauth access token")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode oauth token payload: %w", err)
	}
	var claims struct {
		OpenAIAuth struct {
			ChatGPTAccountID string `json:"chatgpt_account_id"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parse oauth token payload: %w", err)
	}
	accountID := strings.TrimSpace(claims.OpenAIAuth.ChatGPTAccountID)
	if accountID == "" {
		return "", errors.New("oauth access token missing chatgpt_account_id")
	}
	return accountID, nil
}
