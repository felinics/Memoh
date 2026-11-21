package codex

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/agent/runtime/external/agentproc"
)

// The chat page polls the goal every few seconds. With no app-server running
// it must answer "no goal" without starting Codex, even for a session that
// already has a thread. The driver here has no workspace source: any attempt
// to start an app-server fails the read.
func TestGoalDoesNotStartCodex(t *testing.T) {
	procs := agentproc.New(agentproc.Options{ReconcileEvery: time.Hour})
	t.Cleanup(func() { procs.Close(context.Background()) })
	d := &Driver{procs: procs, logger: slog.Default()}

	goal, err := d.Goal(t.Context(), external.PromptInput{
		BotID: "bot-1", BotAgentID: "agent-1", ThreadID: "session-1",
		RuntimeMetadata: map[string]any{metadataThreadIDKey: "codex-thread-1"},
	})
	if err != nil || goal != nil {
		t.Fatalf("Goal = %v, %v; want no goal and no attempt to start Codex", goal, err)
	}
	if procs.Peek(serverKey("bot-1", "agent-1")) != nil {
		t.Fatal("reading the goal started an app-server")
	}
}
