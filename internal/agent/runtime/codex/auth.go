package codex

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/workspace/bridge"
)

type chatGPTCredential struct {
	accessToken  string
	idToken      string
	refreshToken string
	accountID    string
	lastRefresh  time.Time
}

func materializeChatGPTCredential(ctx context.Context, client *bridge.Client, botAgentID string, credential agentcredential.ResolvedCredential) error {
	if credential.AuthKind != agentcredential.AuthKindOpenAICodexOAuth {
		return agentcredential.ErrIncompatible
	}
	lastRefresh, _ := time.Parse(time.RFC3339Nano, metadataString(credential.AccountMetadata, "last_refresh"))
	payload, err := json.Marshal(map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]string{
			"access_token":  credential.Secret["access_token"],
			"id_token":      credential.Secret["id_token"],
			"refresh_token": credential.Secret["refresh_token"],
			"account_id":    credential.Secret["account_id"],
		},
		"last_refresh": lastRefresh.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return err
	}
	home := codexHome(botAgentID)
	if err := client.Mkdir(ctx, home); err != nil {
		return err
	}
	return client.WriteFile(ctx, path.Join(home, "auth.json"), append(payload, '\n'))
}

func readChatGPTCredential(ctx context.Context, client *bridge.Client, botAgentID string) (chatGPTCredential, error) {
	response, err := client.ReadFile(ctx, path.Join(codexHome(botAgentID), "auth.json"), 0, 0)
	if err != nil {
		return chatGPTCredential{}, err
	}
	var payload struct {
		AuthMode    string            `json:"auth_mode"`
		Tokens      map[string]string `json:"tokens"`
		LastRefresh string            `json:"last_refresh"`
	}
	if err := json.Unmarshal([]byte(response.GetContent()), &payload); err != nil {
		return chatGPTCredential{}, err
	}
	credential := chatGPTCredential{
		accessToken:  strings.TrimSpace(payload.Tokens["access_token"]),
		idToken:      strings.TrimSpace(payload.Tokens["id_token"]),
		refreshToken: strings.TrimSpace(payload.Tokens["refresh_token"]),
		accountID:    strings.TrimSpace(payload.Tokens["account_id"]),
	}
	credential.lastRefresh, _ = time.Parse(time.RFC3339Nano, strings.TrimSpace(payload.LastRefresh))
	if payload.AuthMode != "chatgpt" || credential.accessToken == "" || credential.idToken == "" || credential.refreshToken == "" || credential.accountID == "" {
		return chatGPTCredential{}, errors.New("codex auth.json does not contain a complete ChatGPT credential")
	}
	return credential, nil
}

func (d *Driver) persistChatGPTCredential(ctx context.Context, client *bridge.Client, input external.PromptInput, stored agentcredential.ResolvedCredential) {
	current, err := readChatGPTCredential(context.WithoutCancel(ctx), client, input.BotAgentID)
	if err != nil {
		d.logger.Warn("read refreshed codex credential failed", slog.Any("error", err))
		return
	}
	if current.accessToken == stored.Secret["access_token"] &&
		current.idToken == stored.Secret["id_token"] &&
		current.refreshToken == stored.Secret["refresh_token"] &&
		current.accountID == stored.Secret["account_id"] {
		return
	}
	metadata := map[string]any{
		"account_id":   current.accountID,
		"last_refresh": current.lastRefresh.UTC().Format(time.RFC3339Nano),
	}
	_, err = d.credentials.UpdateSecretCAS(
		context.WithoutCancel(ctx),
		stored.ID,
		stored.CredentialVersion,
		map[string]string{
			"access_token":  current.accessToken,
			"id_token":      current.idToken,
			"refresh_token": current.refreshToken,
			"account_id":    current.accountID,
		},
		metadata,
		stored.ExpiresAt,
	)
	if err != nil {
		d.logger.Warn("persist refreshed codex credential failed", slog.Any("error", err))
	}
}
