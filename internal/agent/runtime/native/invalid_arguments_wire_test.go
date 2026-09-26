package native

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/felinics/twilight/provider/openai/completions"
	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// End to end through the real chat-completions provider: a model turn whose
// tool call carries arguments that are not a JSON document is answered with
// an error result, and the next request the loop sends replays that call
// with "{}" as its arguments and the error text in the tool message. A
// backend that parses historical arguments never sees the broken text.
func TestInvalidToolArgumentsReplayOnTheWire(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	var requests []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		mu.Lock()
		requests = append(requests, body)
		call := len(requests)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if call == 1 {
			_, _ = w.Write([]byte(`{"id":"c1","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\": \"fold"}}]}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"c2","object":"chat.completion","created":1700000000,"model":"m","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"corrected"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer srv.Close()

	provider := completions.New(completions.WithAPIKey("k"), completions.WithBaseURL(srv.URL))
	var executed bool
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
			executed = true
			return sdk.TextOutput("never"), nil
		},
	}}}})
	result, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "m", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("go")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if executed {
		t.Fatal("the tool ran on arguments that were not a JSON document")
	}
	if result.Text != "corrected" || len(requests) != 2 {
		t.Fatalf("text=%q requests=%d, want the corrected answer after two requests", result.Text, len(requests))
	}
	messages, _ := requests[1]["messages"].([]any)
	if len(messages) < 3 {
		t.Fatalf("second request messages = %#v, want user, assistant, tool", messages)
	}
	assistant, _ := messages[1].(map[string]any)
	toolCalls, _ := assistant["tool_calls"].([]any)
	if len(toolCalls) != 1 {
		t.Fatalf("assistant tool_calls = %#v", assistant)
	}
	function, _ := toolCalls[0].(map[string]any)["function"].(map[string]any)
	if function["arguments"] != "{}" {
		t.Fatalf("replayed arguments = %#v, want {}", function["arguments"])
	}
	toolMsg, _ := messages[2].(map[string]any)
	if content, _ := toolMsg["content"].(string); !strings.Contains(content, "not a JSON document") || !strings.Contains(content, `{"q": "fold`) {
		t.Fatalf("tool message = %#v, want the error result carrying the model's text", toolMsg)
	}
}
