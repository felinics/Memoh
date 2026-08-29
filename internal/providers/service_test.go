package providers

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
)

func TestMaskAPIKey(t *testing.T) {
	t.Parallel()

	t.Run("short key is fully masked", func(t *testing.T) {
		t.Parallel()
		if got := maskAPIKey("sk-12"); got != "*****" {
			t.Fatalf("expected fully masked, got %q", got)
		}
	})

	t.Run("long key preserves prefix", func(t *testing.T) {
		t.Parallel()
		key := "sk-1234567890abcdef"
		masked := maskAPIKey(key)
		if masked == key {
			t.Fatal("masked key should differ from original")
		}
		if len(masked) != len(key) {
			t.Fatalf("masked length %d != original length %d", len(masked), len(key))
		}
		if masked[:8] != key[:8] {
			t.Fatalf("prefix mismatch: %q vs %q", masked[:8], key[:8])
		}
	})

	t.Run("empty key returns empty", func(t *testing.T) {
		t.Parallel()
		if got := maskAPIKey(""); got != "" {
			t.Fatalf("expected empty, got %q", got)
		}
	})
}

func TestFetchTemplateModelsUsesPresetSource(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, "zai.yaml")
	if err := os.WriteFile(path, []byte(`
name: Z.AI GLM
client_type: openai-completions
icon: zhipu-color
base_url: https://api.z.ai/api/paas/v4

models:
  - model_id: glm-5
    name: GLM 5
    type: chat
    config:
      compatibilities: [tool-call, reasoning]
      context_window: 200000
      thinking_mode: toggle
      reasoning_efforts: [low, medium, high]
`), 0o600); err != nil {
		t.Fatalf("write template: %v", err)
	}

	svc := NewService(nil, nil, "", dir)
	models, ok := svc.fetchTemplateModels(context.Background(), sqlc.Provider{
		Metadata: []byte(`{"preset":{"source":"zai.yaml"}}`),
	})
	if !ok {
		t.Fatal("expected template models")
	}
	if len(models) != 1 {
		t.Fatalf("models = %d, want 1", len(models))
	}
	model := models[0]
	if model.ID != "glm-5" || model.Name != "GLM 5" || model.Type != "chat" {
		t.Fatalf("unexpected model: %#v", model)
	}
	if got := strings.Join(model.Compatibilities, ","); got != "tool-call,reasoning" {
		t.Fatalf("compatibilities = %q", got)
	}
	if !model.CapabilitiesKnown {
		t.Fatal("expected legacy preset template capabilities to be authoritative")
	}
	if model.ContextWindow == nil || *model.ContextWindow != 200000 {
		t.Fatalf("context window = %#v", model.ContextWindow)
	}
}

func TestListCodexRemoteModels(t *testing.T) {
	t.Parallel()

	type observedRequest struct {
		method        string
		path          string
		clientVersion string
		authorization string
		accountID     string
		originator    string
	}
	requestCh := make(chan observedRequest, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCh <- observedRequest{
			method:        r.Method,
			path:          r.URL.Path,
			clientVersion: r.URL.Query().Get("client_version"),
			authorization: r.Header.Get("Authorization"),
			accountID:     r.Header.Get("chatgpt-account-id"),
			originator:    r.Header.Get("originator"),
		}

		_ = json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{
					"slug":         "gpt-5.6-sol",
					"display_name": "GPT-5.6-Sol",
					"visibility":   "list",
					"supported_reasoning_levels": []map[string]any{
						{"effort": " NONE "},
						{"effort": "low"},
						{"effort": "medium"},
						{"effort": "high"},
						{"effort": "future"},
						{"effort": "xhigh"},
						{"effort": "max"},
						{"effort": "ultra"},
						{"effort": "none"},
					},
					"context_window":   372000,
					"input_modalities": []string{"text", "image"},
				},
				{
					"slug":                       "internal-model",
					"display_name":               "Internal Model",
					"visibility":                 "hide",
					"supported_reasoning_levels": []map[string]any{{"effort": "medium"}},
				},
			},
		})
	}))
	defer server.Close()

	svc := &Service{httpClient: server.Client()}
	remoteModels, err := svc.listCodexRemoteModels(context.Background(), server.URL+"/backend-api", ModelCredentials{
		APIKey:         "access-token",
		CodexAccountID: "acct_123",
	})
	if err != nil {
		t.Fatalf("list Codex models: %v", err)
	}
	request := <-requestCh
	if request.method != http.MethodGet {
		t.Errorf("method = %s, want GET", request.method)
	}
	if request.path != "/backend-api/codex/models" {
		t.Errorf("path = %q, want /backend-api/codex/models", request.path)
	}
	if request.clientVersion != codexModelsClientVersion {
		t.Errorf("client_version = %q, want %q", request.clientVersion, codexModelsClientVersion)
	}
	if request.authorization != "Bearer access-token" {
		t.Errorf("authorization = %q", request.authorization)
	}
	if request.accountID != "acct_123" {
		t.Errorf("chatgpt-account-id = %q", request.accountID)
	}
	if request.originator != "codex_cli_rs" {
		t.Errorf("originator = %q", request.originator)
	}
	if len(remoteModels) != 1 {
		t.Fatalf("models = %d, want 1", len(remoteModels))
	}
	model := remoteModels[0]
	if model.ID != "gpt-5.6-sol" || model.Name != "GPT-5.6-Sol" {
		t.Fatalf("unexpected model: %#v", model)
	}
	if got := strings.Join(model.Compatibilities, ","); got != "tool-call,vision,reasoning" {
		t.Fatalf("compatibilities = %q", got)
	}
	if got := strings.Join(model.ReasoningEfforts, ","); got != "disable,low,medium,high,xhigh,max" {
		t.Fatalf("reasoning efforts = %q", got)
	}
	if model.ThinkingMode != models.ThinkingModeToggle {
		t.Fatalf("thinking mode = %q", model.ThinkingMode)
	}
	if model.ContextWindow == nil || *model.ContextWindow != 372000 {
		t.Fatalf("context window = %#v", model.ContextWindow)
	}
}

func TestNormalizeProviderConfig(t *testing.T) {
	t.Parallel()

	t.Run("github copilot drops legacy secrets", func(t *testing.T) {
		t.Parallel()

		cfg := normalizeProviderConfig("github-copilot", map[string]any{
			"api_key":                  "gh-secret",
			configOAuthClientSecretKey: "oauth-secret",
			"base_url":                 "ignored",
		})

		if _, exists := cfg[configOAuthClientSecretKey]; exists {
			t.Fatalf("expected oauth client secret to be removed, got %#v", cfg[configOAuthClientSecretKey])
		}
		if _, exists := cfg["api_key"]; exists {
			t.Fatalf("expected legacy api_key to be removed, got %#v", cfg["api_key"])
		}
	})

	t.Run("non copilot providers keep api key key", func(t *testing.T) {
		t.Parallel()

		cfg := normalizeProviderConfig("openai-completions", map[string]any{
			"api_key": "sk-live",
		})

		if got, ok := cfg["api_key"].(string); !ok || got != "sk-live" {
			t.Fatalf("expected api_key to remain untouched, got %#v", cfg["api_key"])
		}
	})
}

func TestIsHiddenRegistryTemplate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider sqlc.Provider
		want     bool
	}{
		{
			name: "disabled registry provider template is hidden",
			provider: sqlc.Provider{
				Enable:   false,
				Metadata: []byte(`{"registry":{"source":"deepseek.yaml"}}`),
			},
			want: true,
		},
		{
			name: "disabled custom provider remains visible",
			provider: sqlc.Provider{
				Enable:   false,
				Metadata: []byte(`{}`),
			},
			want: false,
		},
		{
			name: "disabled configured registry provider remains visible",
			provider: sqlc.Provider{
				Enable:   false,
				Config:   []byte(`{"base_url":"https://api.deepseek.com/v1","api_key":"sk-existing"}`),
				Metadata: []byte(`{"registry":{"source":"deepseek.yaml"}}`),
			},
			want: false,
		},
		{
			name: "enabled registry-derived provider remains visible",
			provider: sqlc.Provider{
				Enable:   true,
				Metadata: []byte(`{"registry":{"source":"deepseek.yaml"}}`),
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isHiddenRegistryTemplate(tt.provider); got != tt.want {
				t.Fatalf("isHiddenRegistryTemplate() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMaskConfigSecrets(t *testing.T) {
	t.Parallel()

	cfg := maskConfigSecrets("openai-completions", map[string]any{
		"api_key": "sk-secret-123456",
	})

	masked, _ := cfg["api_key"].(string)
	if masked == "" || masked == "sk-secret-123456" {
		t.Fatalf("expected api key to be masked, got %q", masked)
	}
}

func TestPreserveMaskedConfigSecret(t *testing.T) {
	t.Parallel()

	merged := map[string]any{
		configOAuthClientSecretKey: "*************",
	}
	existing := map[string]any{
		configOAuthClientSecretKey: "gh-secret-1234",
	}
	incoming := map[string]any{
		configOAuthClientSecretKey: maskAPIKey("gh-secret-1234"),
	}

	preserveMaskedConfigSecret(merged, existing, incoming, configOAuthClientSecretKey)

	if got, _ := merged[configOAuthClientSecretKey].(string); got != "gh-secret-1234" {
		t.Fatalf("expected masked value to be restored to original secret, got %q", got)
	}
}

func TestDeviceMetadataRoundTrip(t *testing.T) {
	t.Parallel()

	expiresAt := time.Date(2026, time.April, 11, 12, 0, 0, 0, time.UTC)
	device := oauthDeviceMetadata{
		DeviceCode:      "device-code",
		UserCode:        "ABCD-EFGH",
		VerificationURI: "https://github.com/login/device",
		ExpiresAt:       expiresAt,
		IntervalSeconds: 5,
	}

	parsed := deviceMetadataFromMap(device.toMetadata())
	if parsed.DeviceCode != device.DeviceCode {
		t.Fatalf("expected device code %q, got %q", device.DeviceCode, parsed.DeviceCode)
	}
	if parsed.UserCode != device.UserCode {
		t.Fatalf("expected user code %q, got %q", device.UserCode, parsed.UserCode)
	}
	if parsed.VerificationURI != device.VerificationURI {
		t.Fatalf("expected verification uri %q, got %q", device.VerificationURI, parsed.VerificationURI)
	}
	if !parsed.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("expected expiresAt %s, got %s", expiresAt, parsed.ExpiresAt)
	}
	if parsed.IntervalSeconds != device.IntervalSeconds {
		t.Fatalf("expected interval %d, got %d", device.IntervalSeconds, parsed.IntervalSeconds)
	}

	status := parsed.toStatus()
	if status == nil || !status.Pending {
		t.Fatalf("expected pending device status, got %#v", status)
	}
}

func TestAccountMetadataRoundTrip(t *testing.T) {
	t.Parallel()

	account := oauthAccountMetadata{
		Label:      "octocat",
		Login:      "octocat",
		Name:       "The Octocat",
		Email:      "octocat@github.com",
		AvatarURL:  "https://avatars.githubusercontent.com/u/1?v=4",
		ProfileURL: "https://github.com/octocat",
	}

	parsed := accountMetadataFromMap(account.toMetadata())
	if parsed.Label != account.Label {
		t.Fatalf("expected label %q, got %q", account.Label, parsed.Label)
	}
	if parsed.Login != account.Login {
		t.Fatalf("expected login %q, got %q", account.Login, parsed.Login)
	}
	if parsed.Name != account.Name {
		t.Fatalf("expected name %q, got %q", account.Name, parsed.Name)
	}
	if parsed.Email != account.Email {
		t.Fatalf("expected email %q, got %q", account.Email, parsed.Email)
	}
	if parsed.AvatarURL != account.AvatarURL {
		t.Fatalf("expected avatar url %q, got %q", account.AvatarURL, parsed.AvatarURL)
	}
	if parsed.ProfileURL != account.ProfileURL {
		t.Fatalf("expected profile url %q, got %q", account.ProfileURL, parsed.ProfileURL)
	}

	status := parsed.toStatus()
	if status == nil {
		t.Fatal("expected account status")
		return
	}
	if status.Label != account.Label {
		t.Fatalf("expected status label %q, got %q", account.Label, status.Label)
	}
}

func TestOAuthConfigForGitHubCopilotUsesFixedDeviceFlowSettings(t *testing.T) {
	t.Parallel()

	service := &Service{}
	cfg := service.oauthConfigForProvider(sqlc.Provider{
		ClientType: string(models.ClientTypeGitHubCopilot),
		Config:     []byte(`{"api_key":"legacy","oauth_client_secret":"legacy-secret"}`),
		Metadata:   []byte(`{"oauth_client_id":"custom","oauth_scopes":"repo"}`),
	})

	if cfg.ClientID != "Iv1.b507a08c87ecfe98" {
		t.Fatalf("expected fixed client id, got %q", cfg.ClientID)
	}
	if cfg.ClientSecret != "" {
		t.Fatalf("expected empty client secret, got %q", cfg.ClientSecret)
	}
	if cfg.Scopes != "read:user user:email" {
		t.Fatalf("expected fixed scope, got %q", cfg.Scopes)
	}
}

func TestOpenAICodexACPDeviceAuthorizationRequestsUserCode(t *testing.T) {
	restore := overrideOpenAICodexDeviceAuthTestHooks(t, "")
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/usercode" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %q", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["client_id"] != defaultOpenAICodexClientID {
			t.Fatalf("client_id = %q", body["client_id"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_auth_id": "device-auth-123",
			"usercode":       "CODE-123",
			"interval":       0,
		})
	}))
	defer server.Close()
	openAICodexACPAuthIssuer = server.URL

	service := &Service{httpClient: server.Client()}
	device, err := service.StartOpenAICodexACPDeviceAuthorization(context.Background())
	if err != nil {
		t.Fatalf("start device auth: %v", err)
	}
	if device.DeviceAuthID != "device-auth-123" {
		t.Fatalf("device auth id = %q", device.DeviceAuthID)
	}
	if device.UserCode != "CODE-123" {
		t.Fatalf("user code = %q", device.UserCode)
	}
	if device.VerificationURL != server.URL+"/codex/device" {
		t.Fatalf("verification url = %q", device.VerificationURL)
	}
	if device.IntervalSeconds != 5 {
		t.Fatalf("interval = %d, want 5", device.IntervalSeconds)
	}
}

func TestOpenAICodexACPDeviceAuthorizationAcceptsStringInterval(t *testing.T) {
	restore := overrideOpenAICodexDeviceAuthTestHooks(t, "")
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/usercode" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"device_auth_id": "device-auth-123",
			"user_code":      "CODE-123",
			"interval":       "7",
		})
	}))
	defer server.Close()
	openAICodexACPAuthIssuer = server.URL

	service := &Service{httpClient: server.Client()}
	device, err := service.StartOpenAICodexACPDeviceAuthorization(context.Background())
	if err != nil {
		t.Fatalf("start device auth: %v", err)
	}
	if device.IntervalSeconds != 7 {
		t.Fatalf("interval = %d, want 7", device.IntervalSeconds)
	}
}

func TestOpenAICodexACPDeviceAuthorizationSanitizesNon404FailureBody(t *testing.T) {
	restore := overrideOpenAICodexDeviceAuthTestHooks(t, "")
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/usercode" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"sensitive-body"}`))
	}))
	defer server.Close()
	openAICodexACPAuthIssuer = server.URL

	service := &Service{httpClient: server.Client()}
	_, err := service.StartOpenAICodexACPDeviceAuthorization(context.Background())
	if err == nil {
		t.Fatal("expected user code failure")
	}
	if strings.Contains(err.Error(), "sensitive-body") {
		t.Fatalf("error should not expose upstream body: %v", err)
	}
	if !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("error should keep status code context: %v", err)
	}
}

func TestOpenAICodexACPDevicePollPendingAndSuccess(t *testing.T) {
	restore := overrideOpenAICodexDeviceAuthTestHooks(t, "")
	defer restore()

	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/token" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %q", r.Method)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body["device_auth_id"] != "device-auth-123" {
			t.Fatalf("device_auth_id = %q", body["device_auth_id"])
		}
		if body["user_code"] != "CODE-123" {
			t.Fatalf("user_code = %q", body["user_code"])
		}
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if attempts == 2 {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_code": "auth-code-123",
			"code_challenge":     "challenge-123",
			"code_verifier":      "verifier-123",
		})
	}))
	defer server.Close()
	openAICodexACPAuthIssuer = server.URL

	service := &Service{httpClient: server.Client()}
	pending, err := service.PollOpenAICodexACPDeviceAuthorization(context.Background(), "device-auth-123", "CODE-123")
	if err != nil {
		t.Fatalf("poll pending: %v", err)
	}
	if !pending.Pending {
		t.Fatalf("expected forbidden pending result")
	}
	pending, err = service.PollOpenAICodexACPDeviceAuthorization(context.Background(), "device-auth-123", "CODE-123")
	if err != nil {
		t.Fatalf("poll pending: %v", err)
	}
	if !pending.Pending {
		t.Fatalf("expected not found pending result")
	}
	success, err := service.PollOpenAICodexACPDeviceAuthorization(context.Background(), "device-auth-123", "CODE-123")
	if err != nil {
		t.Fatalf("poll success: %v", err)
	}
	if success.Pending {
		t.Fatalf("success result should not be pending")
	}
	if success.AuthorizationCode != "auth-code-123" || success.CodeVerifier != "verifier-123" {
		t.Fatalf("unexpected success result: %#v", success)
	}
}

func TestOpenAICodexACPDevicePollRequiresCodeChallenge(t *testing.T) {
	restore := overrideOpenAICodexDeviceAuthTestHooks(t, "")
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/token" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"authorization_code": "auth-code-123",
			"code_verifier":      "verifier-123",
		})
	}))
	defer server.Close()
	openAICodexACPAuthIssuer = server.URL

	service := &Service{httpClient: server.Client()}
	_, err := service.PollOpenAICodexACPDeviceAuthorization(context.Background(), "device-auth-123", "CODE-123")
	if err == nil {
		t.Fatal("expected incomplete poll response failure")
	}
	if !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("error = %v, want incomplete response", err)
	}
}

func TestOpenAICodexACPDevicePollSanitizesHardFailureBody(t *testing.T) {
	restore := overrideOpenAICodexDeviceAuthTestHooks(t, "")
	defer restore()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/accounts/deviceauth/token" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":"sensitive-body"}`))
	}))
	defer server.Close()
	openAICodexACPAuthIssuer = server.URL

	service := &Service{httpClient: server.Client()}
	_, err := service.PollOpenAICodexACPDeviceAuthorization(context.Background(), "device-auth-123", "CODE-123")
	if err == nil {
		t.Fatal("expected poll failure")
	}
	if strings.Contains(err.Error(), "sensitive-body") {
		t.Fatalf("error should not expose upstream body: %v", err)
	}
	if !strings.Contains(err.Error(), "status 401") {
		t.Fatalf("error should keep status code context: %v", err)
	}
}

func TestCodexAccountIDFromTokensPrefersIDToken(t *testing.T) {
	accessToken := testCodexJWT(t, "account-from-access-token")
	idToken := testCodexJWT(t, "account-from-id-token")

	accountID := codexAccountIDFromTokens(accessToken, idToken)
	if accountID != "account-from-id-token" {
		t.Fatalf("account id = %q, want id token account", accountID)
	}
}

func TestOpenAICodexACPDeviceExchangeUsesDeviceCallbackAndIDTokenFallback(t *testing.T) {
	restore := overrideOpenAICodexDeviceAuthTestHooks(t, "")
	defer restore()

	idToken := testCodexJWT(t, "account-from-id-token")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("parse form: %v", err)
		}
		if got := r.Form.Get("redirect_uri"); got != serverURL(r)+"/deviceauth/callback" {
			t.Fatalf("redirect_uri = %q", got)
		}
		if got := r.Form.Get("code_verifier"); got != "verifier-123" {
			t.Fatalf("code_verifier = %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "opaque-access-token",
			"id_token":      idToken,
			"refresh_token": "refresh-token-123",
		})
	}))
	defer server.Close()
	openAICodexACPAuthIssuer = server.URL

	service := &Service{httpClient: server.Client()}
	creds, err := service.ExchangeOpenAICodexACPDeviceCode(context.Background(), "auth-code-123", "verifier-123")
	if err != nil {
		t.Fatalf("exchange device code: %v", err)
	}
	if creds.AccountID != "account-from-id-token" {
		t.Fatalf("account id = %q", creds.AccountID)
	}
	if creds.AccessToken != "opaque-access-token" || creds.RefreshToken != "refresh-token-123" {
		t.Fatalf("unexpected creds: %#v", creds)
	}
}

func TestOpenAICodexACPDeviceExchangeAllowsMissingAccountID(t *testing.T) {
	restore := overrideOpenAICodexDeviceAuthTestHooks(t, "")
	defer restore()

	emptyIDToken := testCodexJWT(t, "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  emptyIDToken,
			"id_token":      emptyIDToken,
			"refresh_token": "refresh-token-123",
		})
	}))
	defer server.Close()
	openAICodexACPAuthIssuer = server.URL

	service := &Service{httpClient: server.Client()}
	creds, err := service.ExchangeOpenAICodexACPDeviceCode(context.Background(), "auth-code-123", "verifier-123")
	if err != nil {
		t.Fatalf("exchange device code: %v", err)
	}
	if creds.AccountID != "" {
		t.Fatalf("account id = %q, want empty", creds.AccountID)
	}
	if creds.AccessToken == "" || creds.IDToken == "" || creds.RefreshToken == "" {
		t.Fatalf("tokens should still be preserved: %#v", creds)
	}
}

func overrideOpenAICodexDeviceAuthTestHooks(t *testing.T, issuer string) func() {
	t.Helper()
	previousIssuer := openAICodexACPAuthIssuer
	previousValidateTokenURL := validateOAuthTokenURLFunc
	previousValidateDeviceURL := validateOpenAICodexACPAuthURLFunc
	if issuer != "" {
		openAICodexACPAuthIssuer = issuer
	}
	validateOAuthTokenURLFunc = func(models.ClientType, string) error { return nil }
	validateOpenAICodexACPAuthURLFunc = func(string) error { return nil }
	return func() {
		openAICodexACPAuthIssuer = previousIssuer
		validateOAuthTokenURLFunc = previousValidateTokenURL
		validateOpenAICodexACPAuthURLFunc = previousValidateDeviceURL
	}
}

func testCodexJWT(t *testing.T, accountID string) string {
	t.Helper()
	encode := func(v any) string {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal jwt segment: %v", err)
		}
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	authClaims := map[string]string{}
	if accountID != "" {
		authClaims["chatgpt_account_id"] = accountID
	}
	return strings.Join([]string{
		encode(map[string]string{"alg": "none"}),
		encode(map[string]any{
			openAIAuthClaimPath: authClaims,
		}),
		"sig",
	}, ".")
}

func serverURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func TestFetchRemoteModelsViaSDK(t *testing.T) {
	t.Parallel()

	t.Run("anthropic", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/v1/models" {
				t.Fatalf("expected /v1/models path (auto-appended), got %q", r.URL.Path)
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":           "claude-sonnet-4-20250514",
						"display_name": "Claude Sonnet 4",
						"type":         "model",
					},
				},
				"has_more": false,
			})
		}))
		defer server.Close()

		s := &Service{}
		remoteModels, err := s.fetchRemoteModelsViaSDK(context.Background(), sqlc.Provider{
			ClientType: string(models.ClientTypeAnthropicMessages),
			Config:     []byte(`{"base_url":"` + server.URL + `","api_key":"sk-ant-test"}`),
		})
		if err != nil {
			t.Fatalf("fetch remote models: %v", err)
		}
		if len(remoteModels) != 1 {
			t.Fatalf("expected 1 model, got %d", len(remoteModels))
		}
		if remoteModels[0].Name != "Claude Sonnet 4" {
			t.Fatalf("expected display name, got %q", remoteModels[0].Name)
		}
		if remoteModels[0].Type != string(models.ModelTypeChat) {
			t.Fatalf("expected chat type, got %q", remoteModels[0].Type)
		}
	})

	t.Run("google gemini", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/models":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"models": []map[string]any{
						{
							"name":                       "models/gemini-2.0-flash",
							"displayName":                "Gemini 2.0 Flash",
							"supportedGenerationMethods": []string{"generateContent", "countTokens"},
						},
						{
							"name":                       "models/gemini-embedding-001",
							"displayName":                "Gemini Embedding 001",
							"supportedGenerationMethods": []string{"embedContent", "countTokens"},
						},
					},
				})
			case "/models/gemini-embedding-001:embedContent":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"embedding": map[string]any{
						"values": []float64{0.1, 0.2, 0.3, 0.4, 0.5},
					},
				})
			default:
				t.Fatalf("unexpected path %q", r.URL.Path)
			}
		}))
		defer server.Close()

		s := &Service{}
		remoteModels, err := s.fetchRemoteModelsViaSDK(context.Background(), sqlc.Provider{
			ClientType: string(models.ClientTypeGoogleGenerativeAI),
			Config:     []byte(`{"base_url":"` + server.URL + `","api_key":"gm-test"}`),
		})
		if err != nil {
			t.Fatalf("fetch remote models: %v", err)
		}
		if len(remoteModels) != 2 {
			t.Fatalf("expected 2 models, got %d", len(remoteModels))
		}
		if remoteModels[0].ID != "gemini-2.0-flash" {
			t.Fatalf("expected models/ prefix stripped, got %q", remoteModels[0].ID)
		}
		if remoteModels[0].Name != "Gemini 2.0 Flash" {
			t.Fatalf("expected display name, got %q", remoteModels[0].Name)
		}
		if remoteModels[0].Type != string(models.ModelTypeChat) {
			t.Fatalf("expected chat type, got %q", remoteModels[0].Type)
		}
		if remoteModels[1].ID != "gemini-embedding-001" {
			t.Fatalf("expected embedding model imported, got %q", remoteModels[1].ID)
		}
		if remoteModels[1].Type != string(models.ModelTypeEmbedding) {
			t.Fatalf("expected embedding type, got %q", remoteModels[1].Type)
		}
		if remoteModels[1].Dimensions == nil || *remoteModels[1].Dimensions != 5 {
			t.Fatalf("expected inferred embedding dimensions 5, got %v", remoteModels[1].Dimensions)
		}
	})

	t.Run("google skips embedding when dimensions probe fails", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/models":
				_ = json.NewEncoder(w).Encode(map[string]any{
					"models": []map[string]any{
						{
							"name":                       "models/gemini-2.0-flash",
							"displayName":                "Gemini 2.0 Flash",
							"supportedGenerationMethods": []string{"generateContent", "countTokens"},
						},
						{
							"name":                       "models/gemini-embedding-001",
							"displayName":                "Gemini Embedding 001",
							"supportedGenerationMethods": []string{"embedContent", "countTokens"},
						},
					},
				})
			case "/models/gemini-embedding-001:embedContent":
				http.Error(w, `{"error":{"message":"quota exceeded"}}`, http.StatusTooManyRequests)
			default:
				t.Fatalf("unexpected path %q", r.URL.Path)
			}
		}))
		defer server.Close()

		s := &Service{}
		remoteModels, err := s.fetchRemoteModelsViaSDK(context.Background(), sqlc.Provider{
			ClientType: string(models.ClientTypeGoogleGenerativeAI),
			Config:     []byte(`{"base_url":"` + server.URL + `","api_key":"gm-test"}`),
		})
		if err != nil {
			t.Fatalf("fetch remote models: %v", err)
		}
		if len(remoteModels) != 1 {
			t.Fatalf("expected only chat model after failed embedding probe, got %d", len(remoteModels))
		}
		if remoteModels[0].ID != "gemini-2.0-flash" {
			t.Fatalf("expected chat model to still import, got %q", remoteModels[0].ID)
		}
	})

	t.Run("openai completions", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/models" {
				t.Fatalf("expected /models path, got %q", r.URL.Path)
			}

			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{"id": "gpt-4o", "object": "model", "created": 1700000000, "owned_by": "openai"},
					{"id": "text-embedding-ada-002", "object": "model", "created": 1700000000, "owned_by": "openai-internal"},
				},
			})
		}))
		defer server.Close()

		s := &Service{}
		remoteModels, err := s.fetchRemoteModelsViaSDK(context.Background(), sqlc.Provider{
			ClientType: string(models.ClientTypeOpenAICompletions),
			Config:     []byte(`{"base_url":"` + server.URL + `","api_key":"sk-test"}`),
		})
		if err != nil {
			t.Fatalf("fetch remote models: %v", err)
		}
		if len(remoteModels) != 2 {
			t.Fatalf("expected 2 models, got %d", len(remoteModels))
		}
		if remoteModels[0].ID != "gpt-4o" {
			t.Fatalf("expected gpt-4o, got %q", remoteModels[0].ID)
		}
		if remoteModels[0].Name != "gpt-4o" {
			t.Fatalf("expected Name to fall back to ID when DisplayName is empty, got %q", remoteModels[0].Name)
		}
	})
}

// providerTestQueries stubs dbstore.Queries with the single row Test needs;
// every other method nil-panics, which keeps this test honest about how
// little the probe path is allowed to touch the database.
type providerTestQueries struct {
	dbstore.Queries
	provider sqlc.Provider
}

func (s providerTestQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return s.provider, nil
}

// Regression for #1042: a successful models list is conclusive for
// reachability + auth. The fake-model generation probe that used to run
// afterwards turned into an "Invalid API key" false positive on gateways
// that answer 401 for an unknown model. This test fails if any request
// beyond the models list is attempted.
func TestTestSkipsModelProbeAfterSuccessfulModelsList(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{{"id": "real-model"}},
			})
			return
		}
		t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	providerID := pgtype.UUID{Bytes: [16]byte{0x10, 0x42}, Valid: true}
	service := &Service{queries: providerTestQueries{provider: sqlc.Provider{
		ID:         providerID,
		ClientType: string(models.ClientTypeOpenAICompletions),
		Config:     []byte(`{"api_key":"sk-test","base_url":"` + server.URL + `"}`),
	}}}

	resp, err := service.Test(context.Background(), providerID.String())
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if resp.Status != TestStatusOK {
		t.Fatalf("status = %q, want %q (message: %s)", resp.Status, TestStatusOK, resp.Message)
	}
	if !resp.Reachable {
		t.Fatal("reachable = false, want true")
	}
}

// Locks the #1087 outcome mapping: only an auth failure on the models list
// is an auth_error; any other HTTP response is unverified (not a failure);
// a transport-level failure stays a hard error.
func TestTestModelsListOutcomeMapping(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		modelsStatus int
		wantStatus   TestStatus
	}{
		{"auth failure stays auth_error", http.StatusUnauthorized, TestStatusAuthError},
		{"missing models endpoint is unverified", http.StatusNotFound, TestStatusUnverified},
		{"upstream 5xx is unverified", http.StatusInternalServerError, TestStatusUnverified},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/models" {
					t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.modelsStatus)
			}))
			defer server.Close()

			providerID := pgtype.UUID{Bytes: [16]byte{0x10, 0x87}, Valid: true}
			service := &Service{queries: providerTestQueries{provider: sqlc.Provider{
				ID:         providerID,
				ClientType: string(models.ClientTypeOpenAICompletions),
				Config:     []byte(`{"api_key":"sk-test","base_url":"` + server.URL + `"}`),
			}}}

			resp, err := service.Test(context.Background(), providerID.String())
			if err != nil {
				t.Fatalf("Test() error = %v", err)
			}
			if resp.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q (message: %s)", resp.Status, tc.wantStatus, resp.Message)
			}
			if !resp.Reachable {
				t.Fatal("reachable = false, want true")
			}
		})
	}
}

func TestTestUnreachableStaysHardError(t *testing.T) {
	t.Parallel()

	// Port 1 is reserved and refuses connections deterministically.
	providerID := pgtype.UUID{Bytes: [16]byte{0x10, 0x87, 0x01}, Valid: true}
	service := &Service{queries: providerTestQueries{provider: sqlc.Provider{
		ID:         providerID,
		ClientType: string(models.ClientTypeOpenAICompletions),
		Config:     []byte(`{"api_key":"sk-test","base_url":"http://127.0.0.1:1"}`),
	}}}

	resp, err := service.Test(context.Background(), providerID.String())
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if resp.Status != TestStatusError {
		t.Fatalf("status = %q, want %q", resp.Status, TestStatusError)
	}
	if resp.Reachable {
		t.Fatal("reachable = true, want false")
	}
}
