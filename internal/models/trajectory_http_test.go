package models

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/trajectory"
)

type trajectoryTestSink struct {
	mu     sync.Mutex
	events []trajectory.Event
	texts  map[string]string
}

func (s *trajectoryTestSink) Append(_ context.Context, event trajectory.Event, contents []trajectory.Content) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.texts == nil {
		s.texts = make(map[string]string)
	}
	for _, content := range contents {
		s.texts[content.Hash] = string(content.Data)
	}
	s.events = append(s.events, event)
	return nil
}

func TestTrajectoryCapturesActualProviderBodyBeforeEndpointReceivesIt(t *testing.T) {
	sink := &trajectoryTestSink{}
	recorder := trajectory.NewRecorder(sink)
	recorder.Bind("run", "session")
	var received []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		received = body
		sink.mu.Lock()
		if len(sink.events) == 0 || sink.events[0].Stage != "wire_request" {
			t.Error("endpoint received request before trajectory captured it")
		}
		sink.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"test","choices":[{"message":{"role":"assistant","content":"done"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	model := NewSDKChatModel(SDKModelConfig{
		ClientType: "openai-completions", BaseURL: server.URL, ModelID: "fixture",
		APIKey: "synthetic-auth-must-not-be-captured", HTTPClient: server.Client(),
	})
	ctx := trajectory.WithRequest(trajectory.WithRecorder(t.Context(), recorder), 23)
	_, err := model.Provider.DoGenerate(ctx, sdk.GenerateParams{
		Model: model, System: "SYSTEM_ORIGIN",
		Messages: []sdk.Message{sdk.UserMessage(strings.Repeat("unique context\n", 25000) + "FINAL_TAIL")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(sink.events) != 2 || sink.events[0].Request != 23 || sink.events[1].Stage != "wire_result" {
		t.Fatalf("wire events = %#v", sink.events)
	}
	var captured strings.Builder
	for _, block := range sink.events[0].Blocks {
		if block.Kind == "request_body" {
			for _, hash := range block.Chunks {
				captured.WriteString(sink.texts[hash])
			}
		}
	}
	if captured.String() != string(received) || !strings.Contains(captured.String(), "FINAL_TAIL") {
		t.Fatal("captured body differs from the bytes received by the endpoint")
	}
	for _, text := range sink.texts {
		if strings.Contains(text, "synthetic-auth-must-not-be-captured") {
			t.Fatal("capture contains authentication configuration")
		}
	}
}

func TestTrajectoryWireCaptureProviderMatrix(t *testing.T) {
	for _, clientType := range []string{"openai-completions", "openai-responses", "anthropic-messages", "google-generative-ai"} {
		for _, stream := range []bool{false, true} {
			name := clientType + "/generate"
			if stream {
				name = clientType + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				sink := &trajectoryTestSink{}
				recorder := trajectory.NewRecorder(sink)
				recorder.Bind("run", "session")
				received := make(chan string, 4)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					data, _ := io.ReadAll(r.Body)
					received <- string(data)
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"fixture rejection"}}`)
				}))
				defer server.Close()
				model := NewSDKChatModel(SDKModelConfig{
					ClientType: clientType, BaseURL: server.URL, ModelID: "fixture",
					APIKey: "synthetic-key", HTTPClient: server.Client(),
				})
				ctx := trajectory.WithRecorder(t.Context(), recorder)
				params := sdk.GenerateParams{Model: model, System: "ORIGINAL_SYSTEM", Messages: []sdk.Message{sdk.UserMessage("LAST_USER_PART")}}
				if stream {
					result, _ := model.Provider.DoStream(ctx, params)
					if result != nil && result.Stream != nil {
						for range result.Stream {
						}
					}
				} else {
					_, _ = model.Provider.DoGenerate(ctx, params)
				}
				var sent string
				select {
				case sent = <-received:
				default:
					t.Fatal("provider never reached the endpoint")
				}
				var captured strings.Builder
				for _, event := range sink.events {
					if event.Stage != "wire_request" {
						continue
					}
					for _, block := range event.Blocks {
						if block.Kind == "request_body" {
							for _, hash := range block.Chunks {
								captured.WriteString(sink.texts[hash])
							}
						}
					}
				}
				if captured.String() != sent || !strings.Contains(sent, "LAST_USER_PART") {
					t.Fatal("provider-specific request was not captured byte for byte")
				}
				for _, text := range sink.texts {
					if strings.Contains(text, "synthetic-key") {
						t.Fatal("provider authentication leaked into trajectory")
					}
				}
			})
		}
	}
}
