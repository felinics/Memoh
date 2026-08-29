package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestBuildACPClaudeCodeOAuthAuthorizeURL(t *testing.T) {
	rawURL := buildACPClaudeCodeOAuthAuthorizeURL("state-123", "challenge-123")
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme+"://"+parsed.Host+parsed.Path != claudeCodeOAuthAuthorizeURL {
		t.Fatalf("authorize url = %q, want %q", parsed.Scheme+"://"+parsed.Host+parsed.Path, claudeCodeOAuthAuthorizeURL)
	}
	query := parsed.Query()
	assertQuery := func(key, want string) {
		t.Helper()
		if got := query.Get(key); got != want {
			t.Fatalf("query %s = %q, want %q", key, got, want)
		}
	}
	assertQuery("code", "true")
	assertQuery("client_id", claudeCodeOAuthClientID)
	assertQuery("response_type", "code")
	assertQuery("redirect_uri", claudeCodeOAuthRedirectURI)
	assertQuery("scope", claudeCodeOAuthScope)
	assertQuery("code_challenge", "challenge-123")
	assertQuery("code_challenge_method", "S256")
	assertQuery("state", "state-123")
}

func TestParseACPClaudeCodeOAuthCode(t *testing.T) {
	code, state := parseACPClaudeCodeOAuthCode(" code-123 # state-123 ")
	if code != "code-123" || state != "state-123" {
		t.Fatalf("parse code/state = %q/%q", code, state)
	}
	code, state = parseACPClaudeCodeOAuthCode("https://platform.claude.com/oauth/code/callback?code=query-code&state=query-state")
	if code != "query-code" || state != "query-state" {
		t.Fatalf("parse callback URL = %q/%q", code, state)
	}
	code, state = parseACPClaudeCodeOAuthCode("https://platform.claude.com/oauth/code/callback#code=fragment-code&state=fragment-state")
	if code != "fragment-code" || state != "fragment-state" {
		t.Fatalf("parse callback URL fragment = %q/%q", code, state)
	}
	code, state = parseACPClaudeCodeOAuthCode("code=query-code&state=query-state")
	if code != "query-code" || state != "query-state" {
		t.Fatalf("parse query string = %q/%q", code, state)
	}
	code, state = parseACPClaudeCodeOAuthCode("code-only")
	if code != "code-only" || state != "" {
		t.Fatalf("parse code-only = %q/%q", code, state)
	}
}

func TestExchangeACPClaudeCodeOAuthTokenRequestsSetupToken(t *testing.T) {
	var captured map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("content-type = %q, want application/json", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&captured); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"oauth-token","token_type":"bearer","expires_in":31536000,"scope":"user:inference"}`))
	}))
	defer server.Close()

	resp, err := exchangeACPClaudeCodeOAuthToken(context.Background(), server.Client(), server.URL, "auth-code", "verifier", "state-123")
	if err != nil {
		t.Fatal(err)
	}
	if resp.AccessToken != "oauth-token" {
		t.Fatalf("access token = %q", resp.AccessToken)
	}
	want := map[string]any{
		"code":          "auth-code",
		"grant_type":    "authorization_code",
		"client_id":     claudeCodeOAuthClientID,
		"redirect_uri":  claudeCodeOAuthRedirectURI,
		"code_verifier": "verifier",
		"expires_in":    float64(claudeCodeOAuthExpiresIn),
		"state":         "state-123",
	}
	for key, wantValue := range want {
		if got := captured[key]; got != wantValue {
			t.Fatalf("request body %s = %#v, want %#v", key, got, wantValue)
		}
	}
}

func TestExchangeACPClaudeCodeOAuthTokenReturnsServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "bad code", http.StatusBadRequest)
	}))
	defer server.Close()

	_, err := exchangeACPClaudeCodeOAuthToken(context.Background(), server.Client(), server.URL, "bad-code", "verifier", "")
	if err == nil || !strings.Contains(err.Error(), "status 400") {
		t.Fatalf("error = %v, want status 400", err)
	}
}

func TestUpsertClaudeCodeOAuthMetadataStoresBotScopedToken(t *testing.T) {
	metadata := map[string]any{
		"workspace": map[string]any{"backend": "docker"},
		"acp": map[string]any{
			"agents": map[string]any{
				"claude-code": map[string]any{
					"enabled":    false,
					"setup_mode": "api_key",
					"managed": map[string]any{
						"api_key":  "sk-ant-test",
						"base_url": "https://api.anthropic.com",
					},
				},
			},
		},
	}

	next := upsertClaudeCodeOAuthMetadata(metadata, " oauth-token ")
	if !claudeCodeOAuthTokenConfigured(next) {
		t.Fatalf("token was not detected in updated metadata")
	}
	acp := next["acp"].(map[string]any)
	agents := acp["agents"].(map[string]any)
	agent := agents["claude-code"].(map[string]any)
	managed := agent["managed"].(map[string]any)
	if agent["enabled"] != true || agent["setup_mode"] != "oauth" {
		t.Fatalf("agent = %#v, want enabled oauth", agent)
	}
	if managed["oauth_token"] != "oauth-token" {
		t.Fatalf("oauth token = %#v", managed["oauth_token"])
	}
	if managed["api_key"] != "sk-ant-test" || managed["base_url"] != "https://api.anthropic.com" {
		t.Fatalf("managed fields were not preserved: %#v", managed)
	}
	if claudeCodeOAuthTokenConfigured(metadata) {
		t.Fatalf("original metadata was mutated")
	}
}

func TestACPClaudeCodeEnsureManagedWorkspaceUsesNativeTarget(t *testing.T) {
	workspaceProvider := &usersACPConfigWorkspace{}
	handler := &ACPClaudeCodeOAuthHandler{acpWorkspace: workspaceProvider}

	if err := handler.ensureManagedWorkspace(context.Background(), "bot-1"); err != nil {
		t.Fatalf("ensureManagedWorkspace() error = %v", err)
	}
	assertWorkspaceTargetsNative(t, "Claude Code WorkspaceInfo", workspaceProvider.workspaceInfoTargetsSnapshot())
}
