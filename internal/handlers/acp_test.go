package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	acpprofile "github.com/felinics/memoh/internal/agent/runtime/acp/profile"
)

func TestACPProfilesResponseIsSafeMetadata(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodGet, "/acp/profiles", nil)
	rec := httptest.NewRecorder()

	if err := NewACPHandler().ListProfiles(e.NewContext(req, rec)); err != nil {
		t.Fatalf("ListProfiles() error = %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var resp acpprofile.ProfilesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	wantIDs := []string{
		acpprofile.AgentACPID,
		acpprofile.AgentClaudeCodeID,
		acpprofile.AgentCodexID,
		acpprofile.AgentHermesID,
	}
	if len(resp.Items) != len(wantIDs) {
		t.Fatalf("profiles len = %d, want %d", len(resp.Items), len(wantIDs))
	}
	for i, wantID := range wantIDs {
		if resp.Items[i].ID != wantID {
			t.Fatalf("profile[%d] id = %q, want %q", i, resp.Items[i].ID, wantID)
		}
	}
	if len(resp.Items[2].ManagedFields) == 0 {
		t.Fatalf("managed fields should be exposed for schema-driven UI")
	}

	raw := rec.Body.String()
	for _, forbidden := range []string{"codex-acp", "claude-agent-acp", "hermes-acp", "npx", "uvx", "OPENAI_API_KEY", "OPENROUTER_API_KEY", "ANTHROPIC_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "HERMES_HOME"} {
		if jsonContainsSubstring(raw, forbidden) {
			t.Fatalf("profiles response leaked unsafe implementation detail %q: %s", forbidden, raw)
		}
	}
	for _, forbidden := range []string{"sk-test-secret"} {
		if jsonContainsValue(raw, forbidden) {
			t.Fatalf("profiles response leaked unsafe implementation detail %q: %s", forbidden, raw)
		}
	}
}

func jsonContainsSubstring(raw, value string) bool {
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return false
	}
	return containsStringSubstring(decoded, value)
}

func jsonContainsValue(raw, value string) bool {
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return false
	}
	return containsString(decoded, value)
}

func containsString(value any, needle string) bool {
	switch v := value.(type) {
	case string:
		return v == needle
	case []any:
		for _, item := range v {
			if containsString(item, needle) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if containsString(item, needle) {
				return true
			}
		}
	}
	return false
}

func containsStringSubstring(value any, needle string) bool {
	switch v := value.(type) {
	case string:
		return strings.Contains(v, needle)
	case []any:
		for _, item := range v {
			if containsStringSubstring(item, needle) {
				return true
			}
		}
	case map[string]any:
		for _, item := range v {
			if containsStringSubstring(item, needle) {
				return true
			}
		}
	}
	return false
}
