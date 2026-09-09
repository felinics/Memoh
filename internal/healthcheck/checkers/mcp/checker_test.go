package mcpchecker

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/mcp"
)

type fakeConnectionLister struct {
	items []mcp.Connection
	err   error
}

func (f *fakeConnectionLister) ListActiveByBot(_ context.Context, _ string) ([]mcp.Connection, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

type fakeToolLister struct {
	items []mcp.ToolDescriptor
	err   error
}

func (f *fakeToolLister) ListTools(_ context.Context, _ mcp.ToolSessionContext) ([]mcp.ToolDescriptor, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.items, nil
}

func newTestLogger() *slog.Logger {
	return slog.New(slog.DiscardHandler)
}

func TestCheckerListChecks(t *testing.T) {
	t.Parallel()

	checker := NewChecker(
		newTestLogger(),
		&fakeConnectionLister{
			items: []mcp.Connection{
				{ID: "conn-1", Name: "Hello World", Type: "http"},
				{ID: "conn-2", Name: "NoTools", Type: "sse"},
			},
		},
		&fakeToolLister{
			items: []mcp.ToolDescriptor{
				{Name: "hello_world_ping"},
				{Name: "hello_world_echo"},
			},
		},
	)

	items := checker.ListChecks(context.Background(), "bot-1")
	if len(items) != 2 {
		t.Fatalf("expected 2 checks, got %d", len(items))
	}
	if items[0].ID != "mcp.connection.conn-1" && items[1].ID != "mcp.connection.conn-1" {
		t.Fatalf("expected check id for conn-1")
	}

	var healthyFound bool
	var noToolsFound bool
	for _, item := range items {
		if item.Subtitle == "Hello World" {
			healthyFound = true
			if item.Status != "ok" {
				t.Fatalf("expected ok status for Hello World, got %s", item.Status)
			}
		}
		if item.Subtitle == "NoTools" {
			noToolsFound = true
			if item.Status != "warn" {
				t.Fatalf("expected warn status for NoTools, got %s", item.Status)
			}
		}
	}
	if !healthyFound || !noToolsFound {
		t.Fatalf("expected both connection checks")
	}
}

func TestCheckerListChecksToolListError(t *testing.T) {
	t.Parallel()

	checker := NewChecker(
		newTestLogger(),
		&fakeConnectionLister{
			items: []mcp.Connection{
				{ID: "conn-1", Name: "ErrConn", Type: "http"},
			},
		},
		&fakeToolLister{err: errors.New("gateway down")},
	)

	items := checker.ListChecks(context.Background(), "bot-1")
	if len(items) != 1 {
		t.Fatalf("expected 1 check, got %d", len(items))
	}
	if items[0].Status != "error" {
		t.Fatalf("expected error status, got %s", items[0].Status)
	}
	if items[0].Detail == "" {
		t.Fatalf("expected non-empty detail")
	}
}
