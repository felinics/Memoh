package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	claudeCodeOAuthClientID     = "9d1c250a-e61b-44d9-88ed-5944d1962f5e"
	claudeCodeOAuthAuthorizeURL = "https://claude.ai/oauth/authorize"
	claudeCodeOAuthTokenURL     = "https://platform.claude.com/v1/oauth/token" //nolint:gosec // Public OAuth endpoint, not a credential.
	claudeCodeOAuthRedirectURI  = "https://platform.claude.com/oauth/code/callback"
)

var ErrClaudeCodeAuthorizationCodeInvalid = errors.New("invalid Claude authorization code")

// ClaudeCodeAuthorization includes private PKCE material that callers must keep encrypted.
type ClaudeCodeAuthorization struct {
	AuthorizationURL string
	State            string
	CodeVerifier     string
}

func (*Service) StartClaudeCodeAuthorization() (ClaudeCodeAuthorization, error) {
	state, err := generateState()
	if err != nil {
		return ClaudeCodeAuthorization{}, err
	}
	verifier, err := generateCodeVerifier()
	if err != nil {
		return ClaudeCodeAuthorization{}, err
	}
	query := url.Values{
		"code": {"true"}, "client_id": {claudeCodeOAuthClientID}, "response_type": {"code"},
		"redirect_uri": {claudeCodeOAuthRedirectURI}, "scope": {"user:inference"},
		"code_challenge":        {computeCodeChallenge(verifier)},
		"code_challenge_method": {"S256"}, "state": {state},
	}
	return ClaudeCodeAuthorization{AuthorizationURL: claudeCodeOAuthAuthorizeURL + "?" + query.Encode(), State: state, CodeVerifier: verifier}, nil
}

// ExchangeClaudeCodeAuthorization accepts the code, code#state, or callback URL
// displayed by Claude. It requests the long-lived token used by setup-token.
func (s *Service) ExchangeClaudeCodeAuthorization(ctx context.Context, input, state, verifier string) (string, error) {
	code, codeState := parseClaudeCodeAuthorizationCode(input)
	if code == "" || state == "" || verifier == "" || (codeState != "" && codeState != state) {
		return "", ErrClaudeCodeAuthorizationCodeInvalid
	}
	payload, err := json.Marshal(map[string]any{
		"code": code, "grant_type": "authorization_code", "client_id": claudeCodeOAuthClientID,
		"redirect_uri": claudeCodeOAuthRedirectURI, "code_verifier": verifier, "state": state,
		"expires_in": 31536000,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, claudeCodeOAuthTokenURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	client := s.httpClient
	if client == nil {
		client = &http.Client{Timeout: providerOAuthHTTPTimeout}
	}
	resp, err := client.Do(req) //nolint:gosec // The destination is the fixed Claude OAuth token endpoint.
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusBadRequest {
		return "", ErrClaudeCodeAuthorizationCodeInvalid
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("claude code token exchange: status %d", resp.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"` //nolint:gosec // Provider response is stored encrypted by the authorization service.
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&token); err != nil {
		return "", err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return "", errors.New("claude code token exchange returned no access token")
	}
	return strings.TrimSpace(token.AccessToken), nil
}

func parseClaudeCodeAuthorizationCode(input string) (string, string) {
	input = strings.TrimSpace(input)
	if parsed, err := url.Parse(input); err == nil {
		if code := parsed.Query().Get("code"); code != "" {
			return code, parsed.Query().Get("state")
		}
		if fragment, err := url.ParseQuery(parsed.Fragment); err == nil && fragment.Get("code") != "" {
			return fragment.Get("code"), fragment.Get("state")
		}
	}
	if query, err := url.ParseQuery(input); err == nil && query.Get("code") != "" {
		return query.Get("code"), query.Get("state")
	}
	code, state, _ := strings.Cut(input, "#")
	return strings.TrimSpace(code), strings.TrimSpace(state)
}
