package providers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/models"
)

func TestListGitHubCopilotRemoteModels(t *testing.T) {
	t.Parallel()

	t.Run("uses picker catalog and maps capabilities", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/models" {
				t.Fatalf("path = %q, want /models", r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer github-oauth-token" {
				t.Fatalf("authorization = %q", got)
			}
			if got := r.Header.Get("Copilot-Integration-Id"); got == "" {
				t.Fatal("Copilot-Integration-Id header is missing")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":                   "reasoning-model",
						"name":                 "Reasoning Model",
						"vendor":               "Example",
						"model_picker_enabled": true,
						"policy":               map[string]any{"state": "enabled"},
						"supported_endpoints":  []string{"/chat/completions", "/responses"},
						"capabilities": map[string]any{
							"type":   "chat",
							"limits": map[string]any{"max_context_window_tokens": 200000},
							"supports": map[string]any{
								"tool_calls":       true,
								"vision":           true,
								"reasoning_effort": []string{" none ", "low", "max", "ultra", "none", "low"},
							},
						},
					},
					{
						"id":                   "off-only-model",
						"name":                 "Off-only Model",
						"model_picker_enabled": true,
						"supported_endpoints":  []string{"/chat/completions"},
						"capabilities": map[string]any{
							"type": "chat",
							"supports": map[string]any{
								"reasoning_effort": []string{"none"},
							},
						},
					},
					{
						"id": "not-in-picker", "model_picker_enabled": false,
						"capabilities": map[string]any{"type": "chat"},
					},
					{
						"id": "disabled", "model_picker_enabled": true,
						"policy":       map[string]any{"state": "disabled"},
						"capabilities": map[string]any{"type": "chat"},
					},
					{
						"id": "responses-only", "model_picker_enabled": true,
						"supported_endpoints": []string{"/responses"},
						"capabilities":        map[string]any{"type": "chat"},
					},
					{
						"id": "embedding", "model_picker_enabled": true,
						"capabilities": map[string]any{"type": "embeddings"},
					},
				},
			})
		}))
		defer server.Close()

		svc := &Service{httpClient: server.Client()}
		remoteModels, err := svc.listGitHubCopilotRemoteModels(context.Background(), server.URL, "github-oauth-token")
		if err != nil {
			t.Fatalf("list GitHub Copilot models: %v", err)
		}
		if len(remoteModels) != 2 {
			t.Fatalf("models = %d, want 2: %#v", len(remoteModels), remoteModels)
		}
		model := remoteModels[0]
		if model.ID != "reasoning-model" || model.Name != "Reasoning Model" || model.OwnedBy != "Example" {
			t.Fatalf("unexpected model: %#v", model)
		}
		if got := strings.Join(model.Compatibilities, ","); got != "tool-call,vision,reasoning" {
			t.Fatalf("compatibilities = %q", got)
		}
		if got := strings.Join(model.ReasoningEfforts, ","); got != "disable,low,max" {
			t.Fatalf("reasoning efforts = %q", got)
		}
		if model.ThinkingMode != models.ThinkingModeToggle {
			t.Fatalf("thinking mode = %q", model.ThinkingMode)
		}
		if model.ContextWindow == nil || *model.ContextWindow != 200000 {
			t.Fatalf("context window = %#v", model.ContextWindow)
		}
		if !model.CapabilitiesKnown {
			t.Fatal("Copilot catalog capabilities should be authoritative")
		}
		offOnlyModel := remoteModels[1]
		if got := strings.Join(offOnlyModel.ReasoningEfforts, ","); got != "disable" {
			t.Fatalf("off-only reasoning efforts = %q", got)
		}
		if got := strings.Join(offOnlyModel.Compatibilities, ","); got != "reasoning" {
			t.Fatalf("off-only compatibilities = %q", got)
		}
		if offOnlyModel.ThinkingMode != models.ThinkingModeToggle {
			t.Fatalf("off-only thinking mode = %q", offOnlyModel.ThinkingMode)
		}
	})

	t.Run("falls back when all picker flags are false", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{
					{
						"id":           "default-chat",
						"capabilities": map[string]any{"type": "chat"},
					},
					{
						"id":                  "enabled-chat",
						"policy":              map[string]any{"state": "enabled"},
						"supported_endpoints": []string{"/chat/completions"},
						"capabilities":        map[string]any{"type": "chat"},
					},
					{
						"id":           "disabled-chat",
						"policy":       map[string]any{"state": "disabled"},
						"capabilities": map[string]any{"type": "chat"},
					},
					{
						"id":                  "responses-only",
						"supported_endpoints": []string{"/responses"},
						"capabilities":        map[string]any{"type": "chat"},
					},
				},
			})
		}))
		defer server.Close()

		svc := &Service{httpClient: server.Client()}
		remoteModels, err := svc.listGitHubCopilotRemoteModels(context.Background(), server.URL, "github-oauth-token")
		if err != nil {
			t.Fatalf("list GitHub Copilot models: %v", err)
		}
		if len(remoteModels) != 2 || remoteModels[0].ID != "default-chat" || remoteModels[1].ID != "enabled-chat" {
			t.Fatalf("unexpected fallback models: %#v", remoteModels)
		}
	})
}

func TestIsManagedModelCatalogClientType(t *testing.T) {
	t.Parallel()

	if !IsManagedModelCatalogClientType(models.ClientTypeOpenAICodex) {
		t.Fatal("Codex should use a managed model catalog")
	}
	if !IsManagedModelCatalogClientType(models.ClientTypeGitHubCopilot) {
		t.Fatal("GitHub Copilot should use a managed model catalog")
	}
	if IsManagedModelCatalogClientType(models.ClientTypeOpenAICompletions) {
		t.Fatal("OpenAI Completions should remain manually managed")
	}
}
