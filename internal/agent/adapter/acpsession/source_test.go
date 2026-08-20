package acpsession

import (
	"context"
	"testing"

	"github.com/memohai/memoh/internal/agent/sessionmode"
	"github.com/memohai/memoh/internal/chat/thread"
)

type fakeThreadGetter struct {
	item thread.Thread
}

func (f fakeThreadGetter) Get(context.Context, string) (thread.Thread, error) {
	return f.item, nil
}

func TestSourceProjectsThreadDescriptor(t *testing.T) {
	t.Parallel()

	item := thread.Thread{
		BotID:           "bot-1",
		Type:            sessionmode.ACPAgent,
		RuntimeType:     thread.RuntimeACPAgent,
		Metadata:        map[string]any{"acp_agent_id": "codex"},
		RuntimeMetadata: map[string]any{"project_path": "/workspace"},
	}
	got, err := (&Source{threads: fakeThreadGetter{item: item}}).Get(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.BotID != "bot-1" || got.SessionType != sessionmode.ACPAgent || !got.IsACP {
		t.Fatalf("unexpected descriptor: %#v", got)
	}
	if got.Metadata["acp_agent_id"] != "codex" || got.RuntimeMetadata["project_path"] != "/workspace" {
		t.Fatalf("metadata not preserved: %#v", got)
	}
}
