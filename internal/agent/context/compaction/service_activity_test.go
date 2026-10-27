package compaction

import (
	"context"
	"log/slog"
	"testing"
	"time"

	messageevent "github.com/felinics/memoh/internal/chat/event"
)

func TestCompactionActivityLifecycle(t *testing.T) {
	t.Parallel()
	started, release := make(chan struct{}), make(chan struct{})
	svc := NewService(slog.New(slog.DiscardHandler), &fakeQueries{listStarted: started, listRelease: release})
	hub := messageevent.NewHub()
	svc.SetEventPublisher(hub)
	cfg := TriggerConfig{BotID: "00000000-0000-0000-0000-00000000b715", SessionID: "00000000-0000-0000-0000-00000000e715", HistoryBudgetTokens: 1000, SummaryWindowTokens: 128000}
	sub, cancel := hub.Subscribe(cfg.BotID, 8)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := svc.Compact(context.Background(), cfg)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		close(release)
		t.Fatal("compaction did not start")
	}
	ids := svc.ActiveSessions(cfg.BotID)
	other := svc.ActiveSessions("other-bot")
	close(release)
	<-done
	if len(ids) != 1 || ids[0] != cfg.SessionID || len(other) != 0 {
		t.Fatalf("incorrect active sessions: %v, other bot: %v", ids, other)
	}
	if ids := svc.ActiveSessions(cfg.BotID); len(ids) != 0 {
		t.Fatalf("finished compaction still active: %v", ids)
	}
	for range 2 {
		select {
		case ev := <-sub.Events:
			if ev.Type != messageevent.EventTypeCompactionChanged {
				t.Fatalf("unexpected event: %v", ev)
			}
		default:
			t.Fatal("missing start/end notification")
		}
	}
}

func TestCompactionActivityClearsAfterPanic(t *testing.T) {
	t.Parallel()
	svc := NewService(slog.New(slog.DiscardHandler), &fakeQueries{listPanic: true, listStarted: make(chan struct{})})
	hub := messageevent.NewHub()
	svc.SetEventPublisher(hub)
	cfg := TriggerConfig{BotID: "00000000-0000-0000-0000-00000000b715", SessionID: "00000000-0000-0000-0000-00000000e715", HistoryBudgetTokens: 1000, SummaryWindowTokens: 128000}
	sub, cancel := hub.Subscribe(cfg.BotID, 8)
	defer cancel()
	func() {
		defer func() {
			if recover() == nil {
				t.Error("expected injected panic")
			}
		}()
		_, _ = svc.Compact(context.Background(), cfg)
	}()
	if len(svc.ActiveSessions(cfg.BotID)) != 0 || len(sub.Events) != 2 {
		t.Fatal("panic must clear activity and notify subscribers")
	}
}
