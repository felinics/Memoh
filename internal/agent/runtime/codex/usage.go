package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/felinics/memoh/internal/agentcredential"
	"github.com/felinics/memoh/internal/apperror"
	modelspkg "github.com/felinics/memoh/internal/models"
)

const (
	chatGPTBackendURL   = "https://chatgpt.com/backend-api"
	usageRequestTimeout = 15 * time.Second
	usageResponseLimit  = 1 << 20
	usageErrorBodyLimit = 4 << 10
)

// AccountUsage is the ChatGPT account's Codex usage: one entry per rolling
// window (the short window first, then the weekly one when the plan has it).
type AccountUsage struct {
	LimitReached bool
	Windows      []UsageWindow
}

type UsageWindow struct {
	UsedPercent   int
	WindowMinutes int
	ResetsAt      *time.Time
}

type usagePayload struct {
	RateLimit *struct {
		Allowed         bool                `json:"allowed"`
		LimitReached    bool                `json:"limit_reached"`
		PrimaryWindow   *usageWindowPayload `json:"primary_window"`
		SecondaryWindow *usageWindowPayload `json:"secondary_window"`
	} `json:"rate_limit"`
}

type usageWindowPayload struct {
	UsedPercent        float64 `json:"used_percent"`
	LimitWindowSeconds int64   `json:"limit_window_seconds"`
	ResetAt            int64   `json:"reset_at"`
}

// AccountUsage reads the usage windows of the Agent's ChatGPT account from the
// same endpoint the Codex CLI uses, without starting the app-server.
//
// It never refreshes the token. Refresh tokens rotate on use, and the running
// app-server refreshes on its own and has its tokens written back after every
// turn; refreshing here as well would invalidate the app-server's copy.
func (d *Driver) AccountUsage(ctx context.Context, botID, botAgentID string) (AccountUsage, error) {
	cfg, credential, err := d.resolveAgentConfig(ctx, botID, botAgentID, true)
	if err != nil {
		return AccountUsage{}, err
	}
	if cfg.Auth != AuthChatGPT {
		return AccountUsage{}, apperror.Wrap(apperror.CodeAgentCredentialIncompatible, agentcredential.ErrIncompatible, nil)
	}
	return fetchAccountUsage(ctx, modelspkg.NewProviderHTTPClient(usageRequestTimeout), chatGPTBackendURL,
		credential.Secret["access_token"], credential.Secret["account_id"])
}

func fetchAccountUsage(ctx context.Context, client *http.Client, baseURL, accessToken, accountID string) (AccountUsage, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/wham/usage", nil)
	if err != nil {
		return AccountUsage{}, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("ChatGPT-Account-Id", accountID)
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req) //nolint:gosec // The URL is the fixed ChatGPT backend; only tests substitute a local server.
	if err != nil {
		return AccountUsage{}, apperror.Wrap(apperror.CodeAgentCredentialUsageUnavailable, err, nil)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusUnauthorized {
		return AccountUsage{}, apperror.New(apperror.CodeAgentCredentialUsageAuthExpired, nil)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, usageErrorBodyLimit))
		return AccountUsage{}, apperror.Wrap(apperror.CodeAgentCredentialUsageUnavailable,
			fmt.Errorf("codex usage request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body))), nil)
	}
	var payload usagePayload
	if err := json.NewDecoder(io.LimitReader(resp.Body, usageResponseLimit)).Decode(&payload); err != nil {
		return AccountUsage{}, apperror.Wrap(apperror.CodeAgentCredentialUsageUnavailable, fmt.Errorf("decode codex usage: %w", err), nil)
	}
	usage := AccountUsage{Windows: []UsageWindow{}}
	if limit := payload.RateLimit; limit != nil {
		usage.LimitReached = limit.LimitReached || !limit.Allowed
		for _, window := range []*usageWindowPayload{limit.PrimaryWindow, limit.SecondaryWindow} {
			if window != nil {
				usage.Windows = append(usage.Windows, window.usageWindow())
			}
		}
	}
	return usage, nil
}

func (w usageWindowPayload) usageWindow() UsageWindow {
	window := UsageWindow{UsedPercent: int(math.Round(w.UsedPercent))}
	if w.LimitWindowSeconds > 0 {
		window.WindowMinutes = int((w.LimitWindowSeconds + 59) / 60)
	}
	if w.ResetAt > 0 {
		resetsAt := time.Unix(w.ResetAt, 0).UTC()
		window.ResetsAt = &resetsAt
	}
	return window
}
