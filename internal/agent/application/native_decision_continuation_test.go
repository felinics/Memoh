package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/apperror"
)

// A decision continuation whose model call the provider refuses ends with the
// provider's code: the forwarded error event carries the code and its detail,
// the run's lifecycle cause is named by the same code, and the cause itself
// is kept only in the server log.
func TestNativeDecisionContinuationNamesTheProviderFailure(t *testing.T) {
	t.Parallel()

	logger, logs := captureLogs()
	service := &Service{
		agent:          native.New(native.Deps{Logger: logger}),
		logger:         logger,
		messageService: &recordingMessageService{},
	}
	cfg := native.RunConfig{
		Model:    &sdk.Model{ID: "continuation-model", Provider: rejectingSpawnProvider{}, Type: sdk.ModelTypeChat},
		Messages: []sdk.Message{sdk.UserMessage("continue after the decision")},
		Identity: native.SessionContext{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID},
	}
	eventCh := make(chan WSStreamEvent, 64)
	lifecycle := &continuationLifecycleResult{}

	err := service.runNativeDecisionContinuation(context.Background(), ChatRequest{
		BotID: lifecycleTestBotID, ChatID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
	}, cfg, "model-1", lifecycle, eventCh)
	close(eventCh)
	if err != nil {
		t.Fatalf("runNativeDecisionContinuation() error = %v, want the failure delivered in the stream", err)
	}

	var errorEvents []native.StreamEvent
	for raw := range eventCh {
		if strings.Contains(string(raw), "SECRET") {
			t.Fatalf("forwarded event leaked the cause: %s", raw)
		}
		var event native.StreamEvent
		if err := json.Unmarshal(raw, &event); err != nil {
			t.Fatalf("decode forwarded event %s: %v", raw, err)
		}
		if event.Type == native.EventError {
			errorEvents = append(errorEvents, event)
		}
	}
	definition, _ := apperror.Lookup(apperror.CodeAgentProviderAuthFailed)
	if len(errorEvents) != 1 || errorEvents[0].Code != string(apperror.CodeAgentProviderAuthFailed) || errorEvents[0].Error != definition.Detail {
		t.Fatalf("forwarded error events = %#v, want one with the provider's code and detail", errorEvents)
	}
	if got := apperror.CodeOf(lifecycle.cause); got != apperror.CodeAgentProviderAuthFailed {
		t.Fatalf("lifecycle cause code = %q, want %q", got, apperror.CodeAgentProviderAuthFailed)
	}

	logs.mu.Lock()
	defer logs.mu.Unlock()
	logged := false
	for _, record := range logs.records {
		if record.Message != "decision continuation stream error" {
			continue
		}
		record.Attrs(func(attr slog.Attr) bool {
			if attr.Key == "error" && strings.Contains(attr.Value.String(), "SECRET key") {
				logged = true
			}
			return true
		})
	}
	if !logged {
		t.Fatal("the continuation's stream failure was not logged with its cause")
	}
}
