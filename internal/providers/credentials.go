package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	memohcopilot "github.com/felinics/memoh/internal/copilot"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
)

const openAIAuthClaimPath = "https://api.openai.com/auth"

type ModelCredentials struct {
	APIKey         string //nolint:gosec // runtime credential material used to construct SDK providers
	CodexAccountID string
}

type OpenAICodexOAuthCredentials struct {
	AccessToken  string //nolint:gosec // runtime credential material used to construct Codex auth.json
	IDToken      string //nolint:gosec // runtime credential material used to construct Codex auth.json
	RefreshToken string //nolint:gosec // runtime credential material used to construct Codex auth.json
	AccountID    string
	BaseURL      string
	ExpiresAt    time.Time
	LastRefresh  time.Time
}

func (s *Service) ResolveModelCredentials(ctx context.Context, provider sqlc.Provider) (ModelCredentials, error) {
	switch models.ClientType(provider.ClientType) {
	case models.ClientTypeGitHubCopilot:
		githubToken, err := s.GetValidAccessToken(ctx, provider.ID.String())
		if err != nil {
			return ModelCredentials{}, err
		}
		copilotToken, err := memohcopilot.ResolveToken(ctx, githubToken)
		if err != nil {
			return ModelCredentials{}, err
		}
		return ModelCredentials{APIKey: copilotToken}, nil

	case models.ClientTypeOpenAICodex:
		token, err := s.GetValidAccessToken(ctx, provider.ID.String())
		if err != nil {
			return ModelCredentials{}, err
		}
		accountID, tokenErr := codexAccountIDFromToken(token)
		if tokenErr != nil {
			tokenRow, rowErr := s.getOAuthToken(ctx, provider.ID.String())
			if rowErr != nil {
				return ModelCredentials{}, tokenErr
			}
			accountID = strings.TrimSpace(stringValue(tokenRow.Metadata, metadataCodexAccountIDKey))
			if accountID == "" {
				return ModelCredentials{}, tokenErr
			}
		}
		return ModelCredentials{
			APIKey:         token,
			CodexAccountID: accountID,
		}, nil

	default:
		apiKey := ProviderConfigString(provider, "api_key")
		return ModelCredentials{
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
