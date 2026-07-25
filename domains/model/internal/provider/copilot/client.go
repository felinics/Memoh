package copilot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	GitHubOAuthClientID = "Iv1.b507a08c87ecfe98"
	GitHubOAuthScope    = "read:user user:email"
	DefaultAPIBaseURL   = "https://api.githubcopilot.com"

	copilotTokenURL          = "https://api.github.com/copilot_internal/v2/token" //nolint:gosec // Fixed GitHub API endpoint, not a credential.
	copilotEditorVersion     = "vscode/1.110.1"
	copilotPluginVersion     = "copilot-chat/0.38.2"
	copilotUserAgent         = "GitHubCopilotChat/0.38.2"
	copilotAPIVersion        = "2025-10-01"
	copilotIntegrationID     = "vscode-chat"
	copilotTokenRefreshSkew  = time.Minute
	defaultHTTPClientTimeout = 15 * time.Second
)

type cachedToken struct {
	Token     string
	ExpiresAt time.Time
}

var tokenCache = struct {
	mu      sync.Mutex
	entries map[string]cachedToken
}{
	entries: map[string]cachedToken{},
}

type tokenResponse struct {
	Token     string `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
}

func ResolveToken(ctx context.Context, githubToken string) (string, error) {
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return "", errors.New("github token is required")
	}

	if token, ok := loadCachedToken(githubToken); ok {
		return token, nil
	}

	token, expiresAt, err := FetchCopilotToken(ctx, githubToken)
	if err != nil {
		return "", err
	}
	storeCachedToken(githubToken, token, expiresAt)
	return token, nil
}

func FetchCopilotToken(ctx context.Context, githubToken string) (string, time.Time, error) {
	githubToken = strings.TrimSpace(githubToken)
	if githubToken == "" {
		return "", time.Time{}, errors.New("github token is required")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, copilotTokenURL, nil)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("create copilot token request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "token "+githubToken)
	req.Header.Set("Editor-Version", copilotEditorVersion)
	req.Header.Set("Editor-Plugin-Version", copilotPluginVersion)
	req.Header.Set("User-Agent", copilotUserAgent)
	req.Header.Set("X-GitHub-Api-Version", copilotAPIVersion)

	resp, err := defaultHTTPClient(nil).Do(req) //nolint:gosec // Request targets a fixed GitHub API endpoint.
	if err != nil {
		return "", time.Time{}, fmt.Errorf("fetch copilot token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("read copilot token response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", time.Time{}, fmt.Errorf("copilot token request failed: %s", strings.TrimSpace(string(body)))
	}

	var parsed tokenResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", time.Time{}, fmt.Errorf("decode copilot token response: %w", err)
	}
	if strings.TrimSpace(parsed.Token) == "" {
		return "", time.Time{}, errors.New("copilot token response did not include a token")
	}

	var expiresAt time.Time
	if parsed.ExpiresAt > 0 {
		expiresAt = time.Unix(parsed.ExpiresAt, 0).UTC()
	}
	return parsed.Token, expiresAt, nil
}

func NewHTTPClient(base *http.Client) *http.Client {
	client := defaultHTTPClient(base)
	client.Transport = &headerRoundTripper{
		base: client.Transport,
		headers: map[string]string{
			"Copilot-Integration-Id": copilotIntegrationID,
			"Editor-Version":         copilotEditorVersion,
			"Editor-Plugin-Version":  copilotPluginVersion,
			"User-Agent":             copilotUserAgent,
			"X-GitHub-Api-Version":   copilotAPIVersion,
		},
	}
	return client
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
}

func (rt *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	for key, value := range rt.headers {
		clone.Header.Set(key, value)
	}
	if rt.base == nil {
		rt.base = http.DefaultTransport
	}
	return rt.base.RoundTrip(clone)
}

func loadCachedToken(githubToken string) (string, bool) {
	tokenCache.mu.Lock()
	defer tokenCache.mu.Unlock()

	entry, ok := tokenCache.entries[githubToken]
	if !ok {
		return "", false
	}
	if !entry.ExpiresAt.IsZero() && !time.Now().Add(copilotTokenRefreshSkew).Before(entry.ExpiresAt) {
		delete(tokenCache.entries, githubToken)
		return "", false
	}
	return entry.Token, true
}

func storeCachedToken(githubToken, token string, expiresAt time.Time) {
	tokenCache.mu.Lock()
	defer tokenCache.mu.Unlock()
	tokenCache.entries[githubToken] = cachedToken{
		Token:     token,
		ExpiresAt: expiresAt,
	}
}

func defaultHTTPClient(base *http.Client) *http.Client {
	if base != nil {
		clone := *base
		if clone.Timeout == 0 {
			clone.Timeout = defaultHTTPClientTimeout
		}
		return &clone
	}
	return &http.Client{Timeout: defaultHTTPClientTimeout}
}
