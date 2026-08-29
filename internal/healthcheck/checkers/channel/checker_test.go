package channelchecker

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel"
)

type fakeConnectionObserver struct {
	items []channel.ConnectionStatus
}

func (f *fakeConnectionObserver) ConnectionStatusesByBot(_ string) []channel.ConnectionStatus {
	return f.items
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestCheckerListChecks(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	checker := NewChecker(newTestLogger(), &fakeConnectionObserver{
		items: []channel.ConnectionStatus{
			{
				ConfigID:    "cfg-1",
				BotID:       "bot-1",
				ChannelType: channel.ChannelType("telegram"),
				Running:     true,
				UpdatedAt:   now,
			},
			{
				ConfigID:    "cfg-2",
				BotID:       "bot-1",
				ChannelType: channel.ChannelType("feishu"),
				Running:     false,
				LastError:   "connect timeout",
				UpdatedAt:   now,
			},
		},
	})

	items := checker.ListChecks(context.Background(), "bot-1")
	if len(items) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(items))
	}

	var okFound bool
	var errFound bool
	for _, item := range items {
		if item.ID == "channel.connection.cfg-1" {
			okFound = true
			if item.Status != "ok" {
				t.Fatalf("expected ok for cfg-1, got %s", item.Status)
			}
		}
		if item.ID == "channel.connection.cfg-2" {
			errFound = true
			if item.Status != "error" {
				t.Fatalf("expected error for cfg-2, got %s", item.Status)
			}
			if item.Detail != "connect timeout" {
				t.Fatalf("unexpected detail: %s", item.Detail)
			}
		}
	}
	if !okFound || !errFound {
		t.Fatalf("expected checks for both configs")
	}
}

func TestCheckerNilObserver(t *testing.T) {
	t.Parallel()

	checker := NewChecker(newTestLogger(), nil)
	items := checker.ListChecks(context.Background(), "bot-1")
	if len(items) != 1 {
		t.Fatalf("expected service warning check, got %d", len(items))
	}
	if items[0].Status != "warn" {
		t.Fatalf("expected warn status, got %s", items[0].Status)
	}
}
