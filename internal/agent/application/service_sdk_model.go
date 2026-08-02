package application

import (
	"context"

	sdk "github.com/memohai/twilight-ai/sdk"

	"github.com/memohai/memoh/internal/db/postgres/sqlc"
	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/oauthctx"
	"github.com/memohai/memoh/internal/providers"
)

// resolvedSDKChatModel keeps provider-derived wire settings next to the SDK
// model built from them. Chat, title generation, and auxiliary vision all use
// this path so credential and compatibility handling cannot drift.
type resolvedSDKChatModel struct {
	Model                 *sdk.Model
	BaseURL               string
	ChatCompletionsCompat string
	PromptCacheTTL        string
}

func (s *Service) buildSDKChatModel(
	ctx context.Context,
	userID string,
	model models.GetResponse,
	provider sqlc.Provider,
	reasoningConfig *models.ReasoningConfig,
) (resolvedSDKChatModel, error) {
	authService := providers.NewService(nil, s.queries, "")
	authCtx := oauthctx.WithUserID(ctx, userID)
	creds, err := authService.ResolveModelCredentials(authCtx, provider)
	if err != nil {
		return resolvedSDKChatModel{}, err
	}

	baseURL := providers.ProviderConfigString(provider, "base_url")
	chatCompletionsCompat := models.ResolveChatCompletionsCompat(
		baseURL,
		providers.ProviderConfigString(provider, models.ChatCompletionsCompatConfigKey),
	)
	return resolvedSDKChatModel{
		Model: models.NewSDKChatModel(models.SDKModelConfig{
			ModelID:               model.ModelID,
			ClientType:            provider.ClientType,
			APIKey:                creds.APIKey,
			CodexAccountID:        creds.CodexAccountID,
			BaseURL:               baseURL,
			ChatCompletionsCompat: chatCompletionsCompat,
			HTTPClient:            s.streamHTTPClient,
			ReasoningConfig:       reasoningConfig,
		}),
		BaseURL:               baseURL,
		ChatCompletionsCompat: chatCompletionsCompat,
		PromptCacheTTL:        providers.ProviderConfigString(provider, "prompt_cache_ttl"),
	}, nil
}
