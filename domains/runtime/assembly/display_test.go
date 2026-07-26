package assembly

import (
	"context"
	"testing"
)

type stubWorkspace struct{}

func (stubWorkspace) BotDisplayEnabled(context.Context, string) bool { return false }
func (stubWorkspace) DisplaySocketPath(string) string                { return "" }

func TestNewDisplayRequiresWorkspace(t *testing.T) {
	if _, _, err := NewDisplay(DisplayDeps{}); err == nil {
		t.Fatal("expected workspace required error")
	}
}

func TestNewDisplayReturnsServiceAndCleanup(t *testing.T) {
	svc, cleanup, err := NewDisplay(DisplayDeps{Workspace: stubWorkspace{}})
	if err != nil {
		t.Fatalf("NewDisplay: %v", err)
	}
	if svc == nil {
		t.Fatal("expected service")
	}
	if cleanup == nil {
		t.Fatal("expected cleanup")
	}
	if err := cleanup(t.Context()); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
}
