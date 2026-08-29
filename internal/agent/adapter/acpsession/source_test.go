package acpsession

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/workdir"
)

type fakeThreadGetter struct {
	item thread.Thread
}

func (f fakeThreadGetter) Get(context.Context, string) (thread.Thread, error) {
	return f.item, nil
}

type fakeWorkdirResolver struct {
	resolved workdir.Resolved
}

func (f fakeWorkdirResolver) ResolveForSession(context.Context, string, string) (workdir.Resolved, error) {
	return f.resolved, nil
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

func TestSourceProjectsBoundWorkdirTarget(t *testing.T) {
	t.Parallel()

	item := thread.Thread{
		BotID:       "bot-1",
		Type:        sessionmode.ACPAgent,
		RuntimeType: thread.RuntimeACPAgent,
		WorkdirID:   "workdir-1",
	}
	got, err := newSource(fakeThreadGetter{item: item}, fakeWorkdirResolver{resolved: workdir.Resolved{
		WorkdirID: "workdir-1",
		TargetID:  "computer-1",
		Kind:      "remote",
		WorkDir:   "/Users/alice/project",
	}}).Get(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.WorkspaceTargetID != "computer-1" || got.WorkspaceTargetKind != "remote" || got.WorkdirPath != "/Users/alice/project" {
		t.Fatalf("workdir descriptor = %#v", got)
	}
}
