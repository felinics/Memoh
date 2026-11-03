package channel

import (
	"context"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// connectionHandlers starts a connection for each config and returns the
// handler the manager gave the adapter for it, with mw running inside.
func connectionHandlers(t *testing.T, mw Middleware, cfgs ...ChannelConfig) []InboundHandler {
	t.Helper()
	adapter := &fakeAdapter{channelType: ChannelType("test")}
	m := NewManager(slog.New(slog.DiscardHandler), NewRegistry(), &fakeConfigStore{}, &fakeInboundProcessor{})
	m.RegisterAdapter(adapter)
	m.Use(mw)
	for _, cfg := range cfgs {
		if err := m.EnsureConnection(context.Background(), cfg); err != nil {
			t.Fatal(err)
		}
	}
	adapter.mu.Lock()
	defer adapter.mu.Unlock()
	return append([]InboundHandler(nil), adapter.handlers...)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestConnectionInboundCapsMessagesPerConfig(t *testing.T) {
	busy := ChannelConfig{ID: "cfg-busy", BotID: "bot-1", ChannelType: ChannelType("test"), UpdatedAt: time.Now()}
	other := ChannelConfig{ID: "cfg-other", BotID: "bot-2", ChannelType: ChannelType("test"), UpdatedAt: time.Now()}
	gate := make(chan struct{})
	var inFlight, peak atomic.Int32
	handlers := connectionHandlers(t, func(InboundHandler) InboundHandler {
		return func(_ context.Context, cfg ChannelConfig, _ InboundMessage) error {
			if cfg.ID == other.ID {
				return nil
			}
			n := inFlight.Add(1)
			defer inFlight.Add(-1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			<-gate
			return nil
		}
	}, busy, other)

	var done sync.WaitGroup
	for range inboundAdmissionLimit + 4 {
		done.Add(1)
		go func() {
			defer done.Done()
			_ = handlers[0](context.Background(), busy, InboundMessage{Channel: ChannelType("test")})
		}()
	}
	waitFor(t, "the cap to fill", func() bool { return inFlight.Load() == inboundAdmissionLimit })
	time.Sleep(50 * time.Millisecond) // let any message past the cap show up
	otherDone := make(chan struct{})
	go func() {
		defer close(otherDone)
		_ = handlers[1](context.Background(), other, InboundMessage{Channel: ChannelType("test")})
	}()
	select {
	case <-otherDone:
	case <-time.After(5 * time.Second):
		t.Error("another config waited on cfg-busy's slots")
	}
	close(gate)
	<-otherDone
	done.Wait()
	if p := peak.Load(); p != inboundAdmissionLimit {
		t.Fatalf("peak messages in the processor = %d, want %d", p, inboundAdmissionLimit)
	}
}

func TestReleaseInboundSlotLetsTheNextMessageIn(t *testing.T) {
	cfg := ChannelConfig{ID: "cfg-1", BotID: "bot-1", ChannelType: ChannelType("test"), UpdatedAt: time.Now()}
	gate := make(chan struct{})
	var started atomic.Int32
	handlers := connectionHandlers(t, func(InboundHandler) InboundHandler {
		return func(ctx context.Context, _ ChannelConfig, _ InboundMessage) error {
			ReleaseInboundSlot(ctx)
			ReleaseInboundSlot(ctx) // releasing twice must be harmless
			started.Add(1)
			<-gate // a turn that lasts
			return nil
		}
	}, cfg)

	const messages = 2*inboundAdmissionLimit + 1
	var done sync.WaitGroup
	for range messages {
		done.Add(1)
		go func() {
			defer done.Done()
			_ = handlers[0](context.Background(), cfg, InboundMessage{Channel: ChannelType("test")})
		}()
	}
	waitFor(t, "every message to start its turn", func() bool { return started.Load() == messages })
	close(gate)
	done.Wait()
}
