package channel

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type recordingSendToolStream struct {
	mu             sync.Mutex
	events         []StreamEvent
	closeErr       error
	finalPushError error
	closeCalls     int
}

func (s *recordingSendToolStream) Push(_ context.Context, event StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	if event.Type == StreamEventFinal && s.finalPushError != nil {
		return s.finalPushError
	}
	return nil
}

func (s *recordingSendToolStream) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCalls++
	return s.closeErr
}

func TestSendToolStreamFinalizeCommitsExistingPreviewWithoutDuplicateFallback(t *testing.T) {
	t.Parallel()
	coordinator := NewSendToolStreamCoordinator()
	key := SendToolStreamKey{BotID: "bot-1", Platform: ChannelTypeTelegram, Target: "chat-1", ToolCallID: "call-1"}
	stream := &recordingSendToolStream{closeErr: errors.New("close failed after commit")}
	if !coordinator.Attach(key, stream) {
		t.Fatal("preview was not attached")
	}
	if err := coordinator.PushDelta(context.Background(), key, "hel"); err != nil {
		t.Fatal(err)
	}
	handled, err := coordinator.Finalize(context.Background(), key, Message{Text: "hello"})
	if err != nil || !handled {
		t.Fatalf("Finalize() handled=%v err=%v", handled, err)
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	finals := 0
	for _, event := range stream.events {
		if event.Type == StreamEventFinal {
			finals++
			if event.Final == nil || event.Final.Message.Text != "hello" {
				t.Fatalf("final event = %#v", event)
			}
		}
	}
	if finals != 1 {
		t.Fatalf("final events = %d, events=%#v", finals, stream.events)
	}
}

func TestSendToolStreamAbortIsSilentAndDoesNotCreateGhostEntry(t *testing.T) {
	t.Parallel()
	coordinator := NewSendToolStreamCoordinator()
	key := SendToolStreamKey{BotID: "bot-1", Platform: ChannelTypeTelegram, Target: "chat-1", ToolCallID: "call-1"}

	coordinator.Abort(context.Background(), key)
	if len(coordinator.entries) != 0 {
		t.Fatalf("abort created an entry: %#v", coordinator.entries)
	}

	stream := &recordingSendToolStream{}
	if !coordinator.Attach(key, stream) {
		t.Fatal("preview was not attached")
	}
	if err := coordinator.PushDelta(context.Background(), key, "partial"); err != nil {
		t.Fatal(err)
	}
	coordinator.Abort(context.Background(), key)

	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.closeCalls != 1 {
		t.Fatalf("close calls = %d, want 1", stream.closeCalls)
	}
	for _, event := range stream.events {
		if event.Type == StreamEventError {
			t.Fatalf("abort emitted a permanent error event: %#v", stream.events)
		}
	}
}

func TestSendToolStreamFinalPushErrorNeverRequestsOneShotFallback(t *testing.T) {
	t.Parallel()
	coordinator := NewSendToolStreamCoordinator()
	key := SendToolStreamKey{BotID: "bot-1", Platform: ChannelTypeTelegram, Target: "chat-1", ToolCallID: "call-1"}
	stream := &recordingSendToolStream{finalPushError: errors.New("uncertain commit")}
	if !coordinator.Attach(key, stream) {
		t.Fatal("preview was not attached")
	}
	handled, err := coordinator.Finalize(context.Background(), key, Message{Text: "hello"})
	if !handled || err == nil {
		t.Fatalf("Finalize() handled=%v err=%v, want owned error without fallback", handled, err)
	}
}

func TestInternalSendMetadataIsRemovedBeforeTelegramDelivery(t *testing.T) {
	t.Parallel()
	message := Message{Metadata: map[string]any{
		InternalSendToolCallIDMetadataKey: "call-1",
		"visible":                         "keep",
	}}
	if got := takeInternalSendToolCallID(&message); got != "call-1" {
		t.Fatalf("tool call ID = %q", got)
	}
	if _, leaked := message.Metadata[InternalSendToolCallIDMetadataKey]; leaked || message.Metadata["visible"] != "keep" {
		t.Fatalf("sanitized metadata = %#v", message.Metadata)
	}
}
