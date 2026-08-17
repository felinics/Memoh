package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/memohai/memoh/internal/agent/runtime/native"
)

func TestShouldPersistTerminalEventAcceptsProviderTimeoutAbort(t *testing.T) {
	t.Parallel()

	_, idle := withIdleTimeout(context.Background(), time.Hour)
	defer idle.Stop()

	event := native.StreamEvent{
		Type:     native.EventAgentAbort,
		Messages: json.RawMessage("[]"),
	}
	if idle.DidFire() {
		t.Fatal("test watchdog fired unexpectedly")
	}
	if shouldPersistTerminalEvent(event, idle, false, false) {
		t.Fatal("explicit empty abort unexpectedly selected for persistence")
	}
	if !shouldPersistTerminalEvent(event, idle, false, true) {
		t.Fatal("provider-timeout abort was not selected for persistence")
	}
}

func TestPersistPartialResultPersistsMarkerForProviderTimeout(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	resolver := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	persisted := resolver.persistPartialResult(
		context.Background(),
		ChatRequest{BotID: "bot-1", ThreadID: "session-1", Query: "hello"},
		resolvedContext{},
		nil,
		0,
		true,
		false,
	)
	if len(persisted) != 2 {
		t.Fatalf("persisted messages = %d, want user + interrupted marker", len(persisted))
	}
	if len(messages.persisted) != 2 {
		t.Fatalf("message service persisted %d messages, want 2", len(messages.persisted))
	}
	if got := persistedTextContent(t, messages.persisted[1].Content); got != interruptedTurnMarker {
		t.Fatalf("persisted marker = %q, want %q", got, interruptedTurnMarker)
	}
}
