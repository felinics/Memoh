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
	"github.com/google/jsonschema-go/jsonschema"
)

func TestOpenCodeGoWire(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		model, path, protocol string
	}{
		{"glm-5.2", "/chat/completions", string(ClientTypeOpenAICompletions)},
		{"gpt-5.6-luna", "/responses", string(ClientTypeOpenAIResponses)},
		{"minimax-m2.7", "/messages", string(ClientTypeAnthropicMessages)},
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
						if len(body["thinking"]) > 0 || len(body["output_config"]) > 0 {
							t.Errorf("Claude thinking controls sent to a Go model: %s %s", body["thinking"], body["output_config"])
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
				// A plain client, like the agent's, does not add Memoh's User-Agent itself.
				cfg := SDKModelConfig{ModelID: tc.model, ClientType: string(ClientTypeOpenCodeGo), BaseURL: srv.URL + "/zen/go/v1", HTTPClient: srv.Client(), ReasoningConfig: &ReasoningConfig{Active: true, Effort: "high"}}
				model := NewSDKChatModel(cfg)
				if ResolveClientType(model) != tc.protocol || ResolveModelClientType(cfg.ClientType, tc.model) != tc.protocol {
					t.Fatal("wrong effective protocol")
				}
				system, messages, _ := ApplyPromptCache(model, "5m", "system", []sdk.Message{sdk.UserMessage("hi")}, nil)
				req := sdk.Request{System: system, Messages: messages}
				ApplyReasoningToRequest(&req, cfg)
				ctx := WithModelSession(context.Background(), "thread-1")
				var err error
				if streaming {
					var stream sdk.ModelStream
					stream, err = model.Stream(ctx, req)
					if err == nil {
						for range stream.Parts {
						}
						_, err = stream.Result()
					}
				} else {
					_, err = model.Generate(ctx, req)
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
				Content          json.RawMessage `json:"content"`
				ReasoningContent *string         `json:"reasoning_content"`
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
		// Some Go Completions routes reject a replayed tool call without
		// reasoning_content; Twilight's Go provider pads it.
		if step == 2 && (len(body.Messages) < 2 || body.Messages[1].ReasoningContent == nil) {
			t.Error("replayed tool call was not adapted for Go's Completions routes")
		}
		w.Header().Set("Content-Type", "application/json")
		if step == 1 {
			_, _ = fmt.Fprint(w, `{"id":"a","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"","tool_calls":[{"id":"call-1","type":"function","function":{"name":"lookup","arguments":"{}"}}]}}]}`)
		} else {
			_, _ = fmt.Fprint(w, `{"id":"b","choices":[{"index":0,"finish_reason":"stop","message":{"role":"assistant","content":"done"}}]}`)
		}
	}))
	defer srv.Close()
	model := NewSDKChatModel(SDKModelConfig{ClientType: string(ClientTypeOpenCodeGo), ModelID: "glm-5.2", BaseURL: srv.URL})
	tools := []sdk.ToolDefinition{{Name: "lookup", Parameters: &jsonschema.Schema{Type: "object"}}}
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Go(func() {
			session := fmt.Sprintf("thread-%d", i)
			ctx := WithModelSession(context.Background(), session)
			req := sdk.Request{Messages: []sdk.Message{sdk.UserMessage(session)}, Tools: tools}
			first, err := model.Generate(ctx, req)
			if err != nil || len(first.ToolCalls) != 1 {
				t.Errorf("tool call: result = %+v, error = %v", first, err)
				return
			}
			call := first.ToolCalls[0]
			req.Messages = append(req.Messages,
				sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{ToolCallID: call.ToolCallID, ToolName: call.ToolName, Input: call.Input}}},
				sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: call.ToolCallID, ToolName: call.ToolName, Result: sdk.TextOutput("found")}),
			)
			result, err := model.Generate(ctx, req)
			if err != nil || result.Text != "done" {
				t.Errorf("tool continuation: result = %+v, error = %v", result, err)
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

func TestOpenCodeGoProbesAndUnlistedModels(t *testing.T) {
	t.Parallel()
	if !IsLLMClientType(ClientTypeOpenCodeGo) {
		t.Fatal("OpenCode Go must be eligible for model import and chat selection")
	}
	var mu sync.Mutex
	var sessions, paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sessions = append(sessions, r.Header.Get(opencodego.SessionHeader))
		paths = append(paths, r.URL.Path)
		mu.Unlock()
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
	// Twilight sends models outside its exception table to Completions.
	model := NewSDKChatModel(SDKModelConfig{ClientType: string(ClientTypeOpenCodeGo), ModelID: "future-model", BaseURL: srv.URL})
	if ResolveClientType(model) != string(ClientTypeOpenAICompletions) {
		t.Fatalf("unlisted model protocol = %s", ResolveClientType(model))
	}
	if _, err := model.Generate(context.Background(), sdk.Request{Messages: []sdk.Message{sdk.UserMessage("hi")}}); err != nil || paths[2] != "/chat/completions" {
		t.Fatalf("unlisted model: path = %v, error = %v", paths, err)
	}
	// Merely carrying session context must not send it to ordinary providers.
	model = NewSDKChatModel(SDKModelConfig{ClientType: string(ClientTypeOpenAICompletions), ModelID: "test", BaseURL: srv.URL})
	_, err := model.Generate(WithModelSession(context.Background(), "private-thread"), sdk.Request{Messages: []sdk.Message{sdk.UserMessage("hi")}})
	if err != nil || len(sessions) != 4 || sessions[3] != "" {
		t.Fatalf("session metadata leaked to another provider: %v", err)
	}
}
