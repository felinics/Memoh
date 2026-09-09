package application

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/uuid"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/runtimefence"
)

func subagentRunContext(botID, sessionID string, token int64) context.Context {
	ctx := runtimefence.WithContext(context.Background(), runtimefence.Fence{
		BotID: botID, SessionID: sessionID, Token: token,
	})
	return withSubagentRunHandle(ctx, sessionruntime.RunHandle{
		BotID:        botID,
		SessionID:    sessionID,
		RunID:        uuid.NewString(),
		TurnID:       uuid.NewString(),
		Generation:   "gen-1",
		FencingToken: token,
	})
}

func TestSubagentStepCommitRequiresHandleAndFence(t *testing.T) {
	botID, sessionID := uuid.NewString(), uuid.NewString()
	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{messageService: store}

	if commit, interrupt := service.SubagentStepCommit(context.Background(), botID, sessionID, "model", "req-msg-1", nil, nil); commit != nil || interrupt != nil {
		t.Fatal("commit enabled without an admitted run handle")
	}
	noFence := withSubagentRunHandle(context.Background(), sessionruntime.RunHandle{
		BotID: botID, SessionID: sessionID, RunID: uuid.NewString(), Generation: "g", FencingToken: 7,
	})
	if commit, interrupt := service.SubagentStepCommit(noFence, botID, sessionID, "model", "req-msg-1", nil, nil); commit != nil || interrupt != nil {
		t.Fatal("commit enabled without a runtime fence")
	}
	unfenced := runtimefence.WithContext(context.Background(), runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 7})
	unfenced = withSubagentRunHandle(unfenced, sessionruntime.RunHandle{
		BotID: botID, SessionID: sessionID, RunID: uuid.NewString(), Generation: "g",
	})
	if commit, interrupt := service.SubagentStepCommit(unfenced, botID, sessionID, "model", "req-msg-1", nil, nil); commit != nil || interrupt != nil {
		t.Fatal("commit enabled with a zero fencing token")
	}
	// A failed pre-run user message write leaves no request row. Committing
	// steps then would report the run persisted and lose the task prompt for
	// good, so incremental persistence must decline and let the terminal path
	// write the whole run, user message included.
	if commit, interrupt := service.SubagentStepCommit(subagentRunContext(botID, sessionID, 7), botID, sessionID, "model", "  ", nil, nil); commit != nil || interrupt != nil {
		t.Fatal("commit enabled without a persisted request row")
	}
	if commit, interrupt := service.SubagentStepCommit(subagentRunContext(botID, sessionID, 7), botID, sessionID, "model", "req-msg-1", nil, nil); commit == nil || interrupt == nil {
		t.Fatal("commit not enabled for an admitted fenced subagent run")
	}
	bare := &Service{messageService: &recordingMessageService{}}
	if commit, interrupt := bare.SubagentStepCommit(subagentRunContext(botID, sessionID, 7), botID, sessionID, "model", "req-msg-1", nil, nil); commit != nil || interrupt != nil {
		t.Fatal("commit enabled on a message service without step persistence")
	}
}

func TestSubagentStepCommitPersistsStepsInOrder(t *testing.T) {
	botID, sessionID := uuid.NewString(), uuid.NewString()
	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{messageService: store}
	ctx := subagentRunContext(botID, sessionID, 9)
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{
		View:   contextfrag.ViewRunConfigPreProvider,
		Counts: contextfrag.ManifestCounts{Fragments: 3, Messages: 2},
	})
	persistedSteps := 0
	commit, interrupt := service.SubagentStepCommit(ctx, botID, sessionID, "model-uuid", "req-msg-1", holder, func() { persistedSteps++ })
	if commit == nil || interrupt == nil {
		t.Fatal("step persistence callbacks not enabled")
	}

	if err := commit(ctx, 0, &sdk.StepResult{Messages: []sdk.Message{
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "task"}}},
		sdk.AssistantMessage("step one"),
	}}); err != nil {
		t.Fatalf("commit step 0: %v", err)
	}
	if err := commit(ctx, 1, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage("step two")}}); err != nil {
		t.Fatalf("commit step 1: %v", err)
	}
	if err := commit(ctx, 3, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage("skipped ahead")}}); err == nil {
		t.Fatal("out-of-order step index was accepted")
	}

	if len(store.steps) != 2 {
		t.Fatalf("persisted %d steps, want 2", len(store.steps))
	}
	first := store.steps[0]
	if len(first.Messages) != 1 {
		t.Fatalf("step 0 persisted %d messages, want the assistant one only", len(first.Messages))
	}
	msg := first.Messages[0]
	if msg.Role != string(sdk.MessageRoleAssistant) || msg.BotID != botID || msg.SessionID != sessionID {
		t.Fatalf("unexpected persisted message: %+v", msg)
	}
	if msg.ModelID != "model-uuid" {
		t.Fatalf("model id = %q, want model-uuid", msg.ModelID)
	}
	if msg.RunID == "" || first.RunID != msg.RunID {
		t.Fatalf("run ids diverge: step %q message %q", first.RunID, msg.RunID)
	}
	if msg.TurnRequestMessageID != "req-msg-1" {
		t.Fatalf("step row does not bind to the task's request message: %q", msg.TurnRequestMessageID)
	}
	var decoded sdk.Message
	if err := json.Unmarshal(msg.Content, &decoded); err != nil {
		t.Fatalf("persisted content is not a marshalled sdk message: %v", err)
	}
	if persistedSteps != 2 {
		t.Fatalf("onPersisted fired %d times, want 2", persistedSteps)
	}
	firstLifecycle, ok := store.steps[0].Messages[0].Metadata[contextfrag.MetadataContextLifecycleKey].(contextfrag.LifecycleSnapshot)
	if !ok || firstLifecycle.Counts.Fragments != 3 || firstLifecycle.AssistantMessageID != "" {
		t.Fatalf("first step lifecycle metadata = %#v, want pre-persist snapshot", store.steps[0].Messages[0].Metadata)
	}
	secondLifecycle, ok := store.steps[1].Messages[0].Metadata[contextfrag.MetadataContextLifecycleKey].(contextfrag.LifecycleSnapshot)
	if !ok || secondLifecycle.AssistantMessageID != "committed" {
		t.Fatalf("second step lifecycle metadata = %#v, want prior assistant association", store.steps[1].Messages[0].Metadata)
	}
	associated, ok := holder.Snapshot()
	if !ok || associated.AssistantMessageID != "committed" {
		t.Fatalf("shared lifecycle snapshot = %#v, set = %v", associated, ok)
	}

	interrupted := sdk.Message{
		Role:    sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "partial inference"}},
	}
	if err := interrupt(ctx, 2, &sdk.StepResult{Messages: []sdk.Message{interrupted}}); err != nil {
		t.Fatalf("persist interrupted step: %v", err)
	}
	if len(store.steps) != 3 || !store.steps[2].Interrupted {
		t.Fatalf("interrupted step = %#v, want durable interrupted checkpoint", store.steps)
	}
	if got := store.steps[2].Messages[0].Metadata[messagepkg.AgentStepInterruptedMetadataKey]; got != true {
		t.Fatalf("interrupted metadata = %#v, want marker", store.steps[2].Messages[0].Metadata)
	}
	if _, ok := store.steps[2].Messages[0].Metadata[contextfrag.MetadataContextLifecycleKey].(contextfrag.LifecycleSnapshot); !ok {
		t.Fatalf("interrupted lifecycle metadata = %#v, want snapshot", store.steps[2].Messages[0].Metadata)
	}
	if persistedSteps != 3 {
		t.Fatalf("interrupted checkpoint did not report durable output: %d", persistedSteps)
	}
}

func TestSubagentStepCommitSkipsEmptyStepWithoutPersisting(t *testing.T) {
	botID, sessionID := uuid.NewString(), uuid.NewString()
	store := &recordingStepPersister{recordingMessageService: &recordingMessageService{}}
	service := &Service{messageService: store}
	ctx := subagentRunContext(botID, sessionID, 3)
	persistedSteps := 0
	commit, _ := service.SubagentStepCommit(ctx, botID, sessionID, "model", "req-msg-1", nil, func() { persistedSteps++ })

	if err := commit(ctx, 0, &sdk.StepResult{Messages: []sdk.Message{
		{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: "only user"}}},
	}}); err != nil {
		t.Fatalf("commit user-only step: %v", err)
	}
	if len(store.steps) != 0 || persistedSteps != 0 {
		t.Fatalf("user-only step persisted: steps=%d fired=%d", len(store.steps), persistedSteps)
	}
	// The empty step still advanced the cursor: the next step index is 1.
	if err := commit(ctx, 1, &sdk.StepResult{Messages: []sdk.Message{sdk.AssistantMessage("real output")}}); err != nil {
		t.Fatalf("commit step 1 after empty step: %v", err)
	}
	if len(store.steps) != 1 || persistedSteps != 1 {
		t.Fatalf("follow-up step not persisted: steps=%d fired=%d", len(store.steps), persistedSteps)
	}
}

func TestSubagentRunObserverRequiresRuntimeAndHandle(t *testing.T) {
	service := &Service{}
	if got := service.SubagentRunObserver(subagentRunContext(uuid.NewString(), uuid.NewString(), 5)); got != nil {
		t.Fatal("observer enabled without a session runtime")
	}
	withRuntime := &Service{decisionRuntime: &sessionruntime.Manager{}}
	if got := withRuntime.SubagentRunObserver(context.Background()); got != nil {
		t.Fatal("observer enabled without an admitted run handle")
	}
}
