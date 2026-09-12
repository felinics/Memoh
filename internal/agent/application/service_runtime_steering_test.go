package application

import (
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func TestExternalSteerClaimsOnlyCurrentRunAndAppliesAfterConfirmation(t *testing.T) {
	service, handle := newDeferredSteerTestService(t)
	ctx := t.Context()
	source := service.runtimeSteering(ChatRequest{RunHandle: handle})
	if err := source.Enable(ctx); err != nil {
		t.Fatal(err)
	}
	first, err := service.EnqueueSteer(ctx, handle.BotID, handle.SessionID, "one", []byte(`{"text":"adjust"}`))
	if err != nil {
		t.Fatal(err)
	}
	item, ok, err := source.Next(ctx)
	if err != nil || !ok || item.ID != string(first.ID) || item.Text != "adjust" {
		t.Fatalf("claim: %+v %v", item, err)
	}
	replay, replayed, err := source.Next(ctx)
	if err != nil || !replayed || replay.ID != item.ID {
		t.Fatal("claim advanced before Codex confirmation")
	}
	if _, err := service.sessionManager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventStepEnd, StepNumber: 0}); err != nil {
		t.Fatal(err)
	}
	if err := source.Accepted(ctx, item.ID, 0); err != nil {
		t.Fatal(err)
	}
	queue, err := service.ListSessionQueues(ctx, handle.BotID, handle.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(queue.Steer) != 0 {
		t.Fatal("confirmed input remained pending")
	}
	// Round persistence replaces the provisional identity, preserving its
	// existing anchor instead of adding a second user bubble at the end.
	service.logger = slog.Default()
	service.publishRuntimeSteerHistory(ctx, ChatRequest{RunHandle: handle}, []string{item.ID}, []messagepkg.Message{
		{ID: "original", TurnID: "original-turn", Role: "user", Content: []byte(`[{"type":"text","text":"adjust"}]`)},
		{ID: "steer", TurnID: "steer-turn", Role: "user", Content: []byte(`[{"type":"text","text":"adjust"}]`)},
	})
	snapshot, err := service.sessionManager.Snapshot(ctx, handle.BotID, handle.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if turns := snapshot.CurrentRunView.SteerTurns; len(turns) != 1 || turns[0].TurnID != "steer-turn" || turns[0].Status != "applied" {
		t.Fatalf("durable steer identity: %+v", turns)
	}
	if err := source.Close(ctx); err != nil {
		t.Fatal(err)
	}
}
