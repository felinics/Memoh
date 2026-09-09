package handlers

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/agent/background"
)

func TestBackgroundTaskSnapshotsUseDescriptionAsSpawnLabel(t *testing.T) {
	mgr := background.New(nil)
	h := &MessageHandler{bgManager: mgr}

	if _, _, err := mgr.StartSpawnTask(context.Background(), "bot1", "sess1", "spawn 2 task(s): alpha | beta"); err != nil {
		t.Fatalf("StartSpawnTask failed: %v", err)
	}

	tasks := h.backgroundTaskSnapshots("bot1", "sess1")
	if len(tasks) != 1 {
		t.Fatalf("expected 1 task snapshot, got %d", len(tasks))
	}
	if tasks[0].Command != "spawn 2 task(s): alpha | beta" {
		t.Errorf("expected spawn description as display label, got %q", tasks[0].Command)
	}
	if tasks[0].Status != "running" {
		t.Errorf("expected running status, got %q", tasks[0].Status)
	}
}
