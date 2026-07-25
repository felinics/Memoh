package provider

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	modeldomain "github.com/memohai/memoh/domains/model"
	memohcopilot "github.com/memohai/memoh/domains/model/internal/provider/copilot"
)

const openAIAuthClaimPath = "https://api.openai.com/auth"

type ResolvedCredentials struct {
	APIKey         string //nolint:gosec // runtime credential material used to construct SDK providers
	CodexAccountID string
}

// ModelCredentials is a compatibility alias for ResolvedCredentials.
type ModelCredentials = ResolvedCredentials

type OpenAICodexOAuthCredentials struct {
	AccessToken  string //nolint:gosec // runtime credential material used to construct Codex auth.json
	IDToken      string //nolint:gosec // runtime credential material used to construct Codex auth.json
	RefreshToken string //nolint:gosec // runtime credential material used to construct Codex auth.json
	AccountID    string
	BaseURL      string
	ExpiresAt    time.Time
	LastRefresh  time.Time
}

func (s *Service) ResolveModelCredentials(ctx context.Context, provider ProviderRecord) (ResolvedCredentials, error) {
	switch modeldomain.ClientType(provider.ClientType) {
	case modeldomain.ClientTypeGitHubCopilot:
		githubToken, err := s.GetValidAccessToken(ctx, provider.ID)
		if err != nil {
			return ResolvedCredentials{}, err
		}
		copilotToken, err := memohcopilot.ResolveToken(ctx, githubToken)
		if err != nil {
			return ResolvedCredentials{}, err
		}
		return ResolvedCredentials{APIKey: copilotToken}, nil

	case modeldomain.ClientTypeOpenAICodex:
		token, err := s.GetValidAccessToken(ctx, provider.ID)
		if err != nil {
			return ResolvedCredentials{}, err
		}
		accountID, tokenErr := codexAccountIDFromToken(token)
		if tokenErr != nil {
			tokenRow, rowErr := s.getOAuthToken(ctx, provider.ID)
			if rowErr != nil {
				return ResolvedCredentials{}, tokenErr
			}
			accountID = strings.TrimSpace(stringValue(tokenRow.Metadata, metadataCodexAccountIDKey))
			if accountID == "" {
				return ResolvedCredentials{}, tokenErr
			}
		}
		return ResolvedCredentials{
			APIKey:         token,
			CodexAccountID: accountID,
		}, nil

	default:
		apiKey := ProviderConfigString(provider.Config, "api_key")
		return ResolvedCredentials{
			APIKey: apiKey,
		}, nil
	}
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
		return "", fmt.Errorf("oauth access token missing %s.chatgpt_account_id", openAIAuthClaimPath)
	}
	return accountID, nil
}

func codexAccountIDFromTokens(accessToken, idToken string) string {
	if accountID, err := codexAccountIDFromToken(idToken); err == nil {
		return accountID
	}
	accountID, err := codexAccountIDFromToken(accessToken)
	if err != nil {
		return ""
	}
	return accountID
}
