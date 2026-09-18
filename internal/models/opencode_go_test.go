package models

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	opencodego "github.com/felinics/twilight/provider/opencode/go"
	"github.com/felinics/twilight/sdk"
)

func TestOpenCodeGoWire(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model, path, protocol string
	}{
		{"glm-5.2", "/chat/completions", string(ClientTypeOpenAICompletions)},
		{"gpt-5.6-luna", "/responses", string(ClientTypeOpenAIResponses)},
		{"qwen3.7-max", "/messages", string(ClientTypeAnthropicMessages)},
	} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.model, streaming), func(t *testing.T) {
				t.Parallel()
				var requests atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					if r.URL.Path != "/zen/go/v1"+tc.path {
						t.Errorf("path = %s", r.URL.Path)
					}
					if r.Header.Get(opencodego.SessionHeader) != "thread-1" || r.Header.Get("User-Agent") != DefaultProviderUserAgent() {
						t.Error("missing session or application identity")
					}
					var body map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if string(body["model"]) != fmt.Sprintf("%q", tc.model) {
						t.Errorf("model = %s", body["model"])
					}
					switch tc.protocol {
					case string(ClientTypeAnthropicMessages):
						if !strings.Contains(string(body["thinking"]), `"type":"enabled"`) || !strings.Contains(string(body["thinking"]), `"budget_tokens"`) {
							t.Errorf("thinking configuration lost: %s", body["thinking"])
						}
						if len(body["output_config"]) > 0 {
							t.Errorf("legacy thinking must not send effort: %s", body["output_config"])
						}
						if !strings.Contains(string(body["system"]), "cache_control") {
							t.Error("Messages prompt cache decoration lost")
						}
					case string(ClientTypeOpenAIResponses):
						if !strings.Contains(string(body["reasoning"]), `"effort":"high"`) {
							t.Errorf("Responses reasoning = %s", body["reasoning"])
						}
					case string(ClientTypeOpenAICompletions):
						if string(body["reasoning_effort"]) != `"high"` {
							t.Errorf("Completions reasoning = %s", body["reasoning_effort"])
						}
					}
					// A rejected upstream request must remain a failure for both APIs.
					http.Error(w, "rejected", http.StatusBadRequest)
				}))
				defer srv.Close()
				cfg := SDKModelConfig{ModelID: tc.model, ClientType: string(ClientTypeOpenCodeGo), BaseURL: srv.URL + "/zen/go/v1", ReasoningConfig: &ReasoningConfig{Active: true, Effort: "high"}}
				model := NewSDKChatModel(cfg)
				if ResolveClientType(model) != tc.protocol || ResolveModelClientType(cfg.ClientType, tc.model) != tc.protocol {
					t.Fatal("wrong effective protocol")
				}
				system, messages, _ := ApplyPromptCache(model, "5m", "system", []sdk.Message{sdk.UserMessage("hi")}, nil)
				opts := append([]sdk.GenerateOption{sdk.WithModel(model), sdk.WithSystem(system), sdk.WithMessages(messages)}, BuildReasoningOptions(cfg)...)
				ctx := WithModelSession(context.Background(), "thread-1")
				var err error
				if streaming {
					var result *sdk.StreamResult
					result, err = sdk.StreamText(ctx, opts...)
					if err == nil {
						_, err = result.ToResult()
					}
				} else {
					_, err = sdk.GenerateTextResult(ctx, opts...)
				}
				if err == nil || requests.Load() != 1 {
					t.Fatalf("error = %v, requests = %d", err, requests.Load())
				}
			})
		}
	}
}

func TestOpenCodeGoConcurrentToolSessions(t *testing.T) {
	t.Parallel()
	var counts sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		session := r.Header.Get(opencodego.SessionHeader)
		if session == "" || len(body.Messages) == 0 || !strings.Contains(string(body.Messages[0].Content), session) {
			t.Error("request session differs from its conversation")
		}
		value, _ := counts.LoadOrStore(session, &atomic.Int32{})
		step := value.(*atomic.Int32).Add(1)
		w.Header().Set("Content-Type", "application/json")
		if step == 1 {
			_, _ = fmt.Fprint(w, `{"id":"a","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`)
		} else {
			_, _ = fmt.Fprint(w, `{"id":"b","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`)
		}
	}))
	defer srv.Close()
	model := NewSDKChatModel(SDKModelConfig{ClientType: string(ClientTypeOpenCodeGo), ModelID: "glm-5.2", BaseURL: srv.URL})
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			session := fmt.Sprintf("thread-%d", i)
			ctx := WithModelSession(context.Background(), session)
			result, err := sdk.GenerateTextResult(ctx, sdk.WithModel(model), sdk.WithMessages([]sdk.Message{sdk.UserMessage(session)}), sdk.WithMaxSteps(3), sdk.WithTools([]sdk.Tool{{
				Name: "lookup", Parameters: map[string]any{"type": "object"},
				Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) { return "found", nil },
			}}))
			if err != nil || result.Text != "done" {
				t.Errorf("tool continuation: result = %v, error = %v", result, err)
			}
		})
	}
	wg.Wait()
	counts.Range(func(key, value any) bool {
		if value.(*atomic.Int32).Load() != 2 {
			t.Errorf("session %s did not keep identity across tool continuation", key)
		}
		return true
	})
}

func TestOpenCodeGoProbesAndUnknownModels(t *testing.T) {
	t.Parallel()
	if !IsLLMClientType(ClientTypeOpenCodeGo) {
		t.Fatal("OpenCode Go must be eligible for model import and chat selection")
	}
	var sessions []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions = append(sessions, r.Header.Get(opencodego.SessionHeader))
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"id":"a","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer srv.Close()
	for range 2 {
		p := NewSDKProvider(srv.URL, "", "", ClientTypeOpenCodeGo, 0, nil)
		result, err := p.TestModel(context.Background(), "glm-5.2")
		if err != nil || !result.Supported {
			t.Fatalf("probe = %v, %v", result, err)
		}
	}
	if len(sessions) != 2 || sessions[0] == "" || sessions[1] == "" || sessions[0] == sessions[1] {
		t.Fatalf("standalone probes must have independent IDs: %v", sessions)
	}
	model := NewSDKChatModel(SDKModelConfig{ClientType: string(ClientTypeOpenCodeGo), ModelID: "unknown-model", BaseURL: srv.URL})
	_, err := sdk.GenerateTextResult(context.Background(), sdk.WithModel(model), sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}))
	if err == nil || !strings.Contains(err.Error(), "no protocol registered") || len(sessions) != 2 {
		t.Fatalf("unknown model reached the network: %v", err)
	}
	// Merely carrying session context must not send it to ordinary providers.
	model = NewSDKChatModel(SDKModelConfig{ClientType: string(ClientTypeOpenAICompletions), ModelID: "test", BaseURL: srv.URL})
	_, err = sdk.GenerateTextResult(WithModelSession(context.Background(), "private-thread"), sdk.WithModel(model), sdk.WithMessages([]sdk.Message{sdk.UserMessage("hi")}))
	if err != nil || len(sessions) != 3 || sessions[2] != "" {
		t.Fatalf("session metadata leaked to another provider: %v", err)
	}
}
