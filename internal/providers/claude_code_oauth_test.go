package providers

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

type claudeOAuthTransport func(*http.Request) (*http.Response, error)

func (f claudeOAuthTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestClaudeCodeAuthorizationPKCEAndExchange(t *testing.T) {
	svc := &Service{}
	auth, err := svc.StartClaudeCodeAuthorization()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(auth.AuthorizationURL)
	if err != nil {
		t.Fatal(err)
	}
	challenge := sha256.Sum256([]byte(auth.CodeVerifier))
	if parsed.Query().Get("code_challenge") != base64.RawURLEncoding.EncodeToString(challenge[:]) || parsed.Query().Get("state") != auth.State {
		t.Fatal("authorization URL is not bound to the stored PKCE session")
	}
	second, err := svc.StartClaudeCodeAuthorization()
	if err != nil || second.State == auth.State || second.CodeVerifier == auth.CodeVerifier {
		t.Fatal("authorization sessions reuse PKCE material")
	}
	svc.httpClient = &http.Client{Transport: claudeOAuthTransport(func(req *http.Request) (*http.Response, error) {
		if req.Method != http.MethodPost || req.URL.String() != claudeCodeOAuthTokenURL {
			t.Error("unexpected token endpoint")
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["code"] != "returned-code" || body["code_verifier"] != auth.CodeVerifier || body["state"] != auth.State || body["redirect_uri"] != parsed.Query().Get("redirect_uri") || body["client_id"] != parsed.Query().Get("client_id") || body["expires_in"] != float64(31536000) {
			t.Error("exchange did not preserve the browser flow or long-lived token request")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"access_token":"SECRET-token"}`))}, nil
	})}
	for _, input := range []string{"returned-code", "returned-code#" + auth.State, claudeCodeOAuthRedirectURI + "?code=returned-code&state=" + auth.State, "code=returned-code&state=" + auth.State, "https://example.test/#code=returned-code&state=" + auth.State} {
		token, err := svc.ExchangeClaudeCodeAuthorization(context.Background(), input, auth.State, auth.CodeVerifier)
		if err != nil || token != "SECRET-token" {
			t.Fatalf("exchange failed: %v", err)
		}
	}
}

func TestClaudeCodeAuthorizationRejectsWrongStateAndRedactsProviderErrors(t *testing.T) {
	calls := 0
	status := http.StatusBadRequest
	svc := &Service{httpClient: &http.Client{Transport: claudeOAuthTransport(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(`{"error":"SECRET-provider-response"}`))}, nil
	})}}
	ctx := context.Background()
	for _, code := range []string{"", "code#wrong-state"} {
		if _, err := svc.ExchangeClaudeCodeAuthorization(ctx, code, "state", "verifier"); !errors.Is(err, ErrClaudeCodeAuthorizationCodeInvalid) {
			t.Fatal("invalid code was accepted")
		}
	}
	if calls != 0 {
		t.Fatal("invalid state was sent to the provider")
	}
	if _, err := svc.ExchangeClaudeCodeAuthorization(ctx, "code#state", "state", "verifier"); !errors.Is(err, ErrClaudeCodeAuthorizationCodeInvalid) {
		t.Fatal("rejected code is not retryable")
	}
	status = http.StatusServiceUnavailable
	_, err := svc.ExchangeClaudeCodeAuthorization(ctx, "code#state", "state", "verifier")
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatal("provider error was not redacted")
	}
}
