package email

import (
	"context"
	"log/slog"
	"testing"

	emailport "github.com/memohai/memoh/domains/channel/internal/port/email"
)

type lifecycleChatTriggerer struct {
	started chan struct{}
	stopped chan struct{}
}

func (t *lifecycleChatTriggerer) TriggerBotChat(ctx context.Context, _, _ string) error {
	close(t.started)
	<-ctx.Done()
	close(t.stopped)
	return ctx.Err()
}

func TestTriggerShutdownWaitsForActiveTurns(t *testing.T) {
	bindings := &managerBindingStore{record: emailport.BindingRecord{
		BotID:           "bot-1",
		EmailProviderID: "provider-1",
		CanRead:         true,
	}}
	service := NewService(slog.New(slog.DiscardHandler), nil, bindings, NewRegistry())
	chat := &lifecycleChatTriggerer{
		started: make(chan struct{}),
		stopped: make(chan struct{}),
	}
	trigger := NewTrigger(slog.New(slog.DiscardHandler), service, chat)
	if err := trigger.Start(t.Context()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	if err := trigger.HandleInbound(requestCtx, "provider-1", InboundEmail{}); err != nil {
		t.Fatalf("HandleInbound() error = %v", err)
	}
	<-chat.started
	cancelRequest()

	select {
	case <-chat.stopped:
		t.Fatal("request cancellation stopped the application-owned email turn")
	default:
	}

	if err := trigger.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case <-chat.stopped:
	default:
		t.Fatal("Shutdown returned before the active email turn stopped")
	}
}
