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
	"github.com/felinics/memoh/internal/errs"
)

const probeTimeout = DefaultProviderProbeTimeout

// Test probes a model's provider endpoint using the Twilight AI SDK
// to verify connectivity, authentication, and model availability.
// Every outcome other than ok and model_not_supported carries the SDK error
// as Cause.
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
	resp := probeChatModel(ctx, sdkProvider, model.ModelID)
	resp.LatencyMs = time.Since(start).Milliseconds()
	return resp, nil
}

// probeChatModel checks the provider first and then the model.
//
// The provider check gives an early verdict only when it is conclusive: an
// auth failure (the models list carries no model parameter, so a 401 or 403
// there is about the credentials), or no answer at all. Any other rejection,
// e.g. 404 because the provider does not implement the models list, falls
// through to the model check, the only check that can give a definitive
// answer for such providers (#1087).
//
// A failed model check is error whatever its kind (#1042): some
// OpenAI-compatible gateways validate the model before auth and answer 401
// for an unknown model, so a 401 there does not prove the key is wrong.
func probeChatModel(ctx context.Context, provider sdk.Provider, modelID string) TestResponse {
	if err := provider.Test(ctx); err != nil {
		if kind := sdk.KindOf(err); kind == sdk.KindAuthentication || kind == sdk.KindPermissionDenied {
			return TestResponse{Status: TestStatusAuthError, Reachable: true, Cause: errs.WrapDependency(err, "test provider")}
		}
		if !answered(err) {
			return TestResponse{Status: TestStatusError, Cause: errs.WrapDependency(err, "test provider")}
		}
	}

	result, err := provider.TestModel(ctx, modelID)
	if err != nil {
		return TestResponse{Status: TestStatusError, Reachable: answered(err), Cause: errs.WrapDependency(err, "test model")}
	}
	if !result.Supported {
		return TestResponse{Status: TestStatusModelNotSupported, Reachable: true}
	}
	return TestResponse{Status: TestStatusOK, Reachable: true}
}

// answered reports whether the provider answered the request that failed
// with err. The SDK returns an *sdk.APIError for every response it rejects
// and some other error when no response arrived.
func answered(err error) bool {
	var apiErr *sdk.APIError
	return errors.As(err, &apiErr)
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
			Reachable: answered(err) || errors.Is(err, ErrEmptyEmbeddingVector),
			LatencyMs: latency,
			Cause:     errs.WrapDependency(err, "test embedding model"),
		}, nil
	}

	return TestResponse{
		Status:    TestStatusOK,
		Reachable: true,
		LatencyMs: latency,
	}, nil
}

// NewSDKProvider creates a Twilight AI SDK Provider for the given client type.
// It is exported so that other packages (e.g. providers) can reuse it for testing.
func NewSDKProvider(baseURL, apiKey, codexAccountID string, clientType ClientType, timeout time.Duration, httpClient *http.Client) sdk.Provider {
	if httpClient == nil {
		httpClient = NewProviderHTTPClient(timeout)
	}

	switch clientType {
	case ClientTypeOpenCodeGo:
		return newOpenCodeGoProvider(baseURL, apiKey, httpClient, string(ClientTypeOpenCodeGo))

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
