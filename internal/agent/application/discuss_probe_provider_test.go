package application

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/models"
)

// captureProbeRequest sends one probe-shaped generation at a stub server and
// returns the decoded request body the provider adapter actually produced.
func captureProbeRequest(t *testing.T, clientType models.ClientType) map[string]any {
	t.Helper()
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Errorf("decode request body: %v (%s)", err, raw)
		}
		w.Header().Set("Content-Type", "application/json")
		// Enough of a response for each adapter to parse without erroring; the
		// assertion is on the request, not the reply.
		_, _ = w.Write([]byte(`{"id":"x","choices":[{"message":{"role":"assistant","content":""},"finish_reason":"stop"}],` +
			`"output":[],"content":[],"candidates":[{"content":{"parts":[{"text":""}],"role":"model"},"finishReason":"STOP"}]}`))
	}))
	defer server.Close()

	model := models.NewSDKChatModel(models.SDKModelConfig{
		ModelID:    "probe-model",
		ClientType: string(clientType),
		APIKey:     "test-key",
		BaseURL:    server.URL,
	})
	// Built by the production helper, not assembled here: a request the test
	// wrote itself would only prove the SDK converts a constant correctly.
	_, _ = sdk.NewClient().GenerateTextResult(context.Background(),
		discussProbeGenerateOptions(model, "judge",
			[]sdk.Message{sdk.UserMessage("hello")},
			[]sdk.Tool{discussProbeTool()})...)
	if body == nil {
		t.Fatalf("%s adapter sent no decodable request", clientType)
	}
	return body
}

// The probe is only a gate if the judge is actually forced to answer. Each
// provider adapter spells forcing differently, and two of them silently accept
// a spelling they do not understand: the Responses adapter forwards an unknown
// object verbatim, and the Gemini adapter drops non-string choices entirely —
// leaving the model free to reply in prose, which the gate then reads as a
// missing verdict and shuts the bot up for good.
func TestDiscussProbeForcesToolCallOnEveryProvider(t *testing.T) {
	t.Run("openai-completions", func(t *testing.T) {
		body := captureProbeRequest(t, models.ClientTypeOpenAICompletions)
		if got := body["tool_choice"]; got != "required" {
			t.Fatalf("tool_choice = %#v, want \"required\"", got)
		}
	})

	t.Run("openai-responses", func(t *testing.T) {
		body := captureProbeRequest(t, models.ClientTypeOpenAIResponses)
		got, ok := body["tool_choice"]
		if !ok {
			t.Fatal("tool_choice absent; the judge is not forced to decide")
		}
		// The nested Chat Completions object form is not valid here: this API
		// wants either a string mode or a top-level name.
		if nested, isObject := got.(map[string]any); isObject {
			if _, hasFunction := nested["function"]; hasFunction {
				t.Fatalf("tool_choice carries a nested function object the Responses API rejects: %#v", nested)
			}
		} else if got != "required" {
			t.Fatalf("tool_choice = %#v, want \"required\"", got)
		}
	})

	t.Run("anthropic-messages", func(t *testing.T) {
		body := captureProbeRequest(t, models.ClientTypeAnthropicMessages)
		choice, ok := body["tool_choice"].(map[string]any)
		if !ok {
			t.Fatalf("tool_choice = %#v, want an object", body["tool_choice"])
		}
		if choice["type"] != "any" && choice["type"] != "tool" {
			t.Fatalf("tool_choice.type = %#v, want any or tool", choice["type"])
		}
	})

	t.Run("google-generative-ai", func(t *testing.T) {
		body := captureProbeRequest(t, models.ClientTypeGoogleGenerativeAI)
		cfg, ok := body["toolConfig"].(map[string]any)
		if !ok {
			t.Fatalf("toolConfig absent; Gemini drops tool choices it cannot read, leaving the judge free to answer in prose: %v", keys(body))
		}
		calling, ok := cfg["functionCallingConfig"].(map[string]any)
		if !ok {
			t.Fatalf("functionCallingConfig absent: %#v", cfg)
		}
		if mode := calling["mode"]; mode != "ANY" {
			t.Fatalf("functionCallingConfig.mode = %#v, want ANY", mode)
		}
	})
}

func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// The tool definition must survive alongside the forced choice; "required" with
// no tools is a request no provider can satisfy.
func TestDiscussProbeSendsItsToolDefinition(t *testing.T) {
	body := captureProbeRequest(t, models.ClientTypeOpenAICompletions)
	raw, err := json.Marshal(body["tools"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), discussProbeToolName) {
		t.Fatalf("decide tool missing from the request: %s", raw)
	}
}
