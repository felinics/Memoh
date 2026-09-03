package sessionruntime

import (
	"context"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

func TestPublishQueueUserTurnsKeepsAppliedInputsOrderedAndIdempotent(t *testing.T) {
	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-user-turns")
	const rootTurnID = "turn-root"
	handle, err := manager.StartRunWithAdmissionBuilderHandle(
		context.Background(), testBotID, testSessionID, testRunID,
		func(_ context.Context, _ RunHandle) (RunAdmissionView, error) {
			return RunAdmissionView{RequestUserTurn: &chatview.UITurn{
				TurnID: rootTurnID, Role: "user", Text: "original", Timestamp: time.Now(),
			}}, nil
		},
		make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}

	root := chatview.UITurn{
		TurnID: rootTurnID, Role: "user", Text: "original", ID: "persisted-root", Timestamp: time.Now(),
	}
	steerOne := chatview.UITurn{
		TurnID: "turn-steer-1", Role: "user", Text: "first steer", ID: "persisted-steer-1", Timestamp: time.Now(),
	}
	steerTwo := chatview.UITurn{
		TurnID: "turn-steer-2", Role: "user", Text: "second steer", ID: "persisted-steer-2", Timestamp: time.Now(),
	}
	if err := manager.PublishQueueUserTurns(context.Background(), handle, QueueUserTurnUpdate{PersistedTurns: []chatview.UITurn{root, steerOne}}); err != nil {
		t.Fatalf("publish first persisted turns: %v", err)
	}
	if err := manager.PublishQueueUserTurns(context.Background(), handle, QueueUserTurnUpdate{PersistedTurns: []chatview.UITurn{steerOne, steerTwo}}); err != nil {
		t.Fatalf("publish overlapping persisted turns: %v", err)
	}

	snapshot, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.CurrentRunView == nil {
		t.Fatal("current run view is missing")
	}
	got := snapshot.CurrentRunView.UserTurns
	if len(got) != 3 {
		t.Fatalf("user turns = %#v, want original plus two steers", got)
	}
	for i, want := range []string{"original", "first steer", "second steer"} {
		if got[i].Text != want {
			t.Fatalf("user turn %d = %q, want %q", i, got[i].Text, want)
		}
	}
	if got[0].ID != "persisted-root" {
		t.Fatalf("root user turn was not upgraded to persisted identity: %#v", got[0])
	}
}

func TestPublishQueueUserTurnsLocatesClaimAndAppliesItWithoutDuplicateProjection(t *testing.T) {
	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-queue-turns")
	handle, err := manager.StartRunWithAdmissionBuilderHandle(
		context.Background(), testBotID, testSessionID, testRunID,
		func(_ context.Context, _ RunHandle) (RunAdmissionView, error) {
			return RunAdmissionView{RequestUserTurn: &chatview.UITurn{
				TurnID: "turn-root", Role: "user", Text: "original", Timestamp: time.Now(),
			}}, nil
		},
		make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{Type: native.EventTextStart}); err != nil {
		t.Fatalf("start assistant output: %v", err)
	}
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventTextDelta, Delta: "before steer",
	}); err != nil {
		t.Fatalf("publish assistant output: %v", err)
	}
	subscription, err := manager.Subscribe(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("subscribe runtime: %v", err)
	}
	defer subscription.Close()
	select {
	case event := <-subscription.C:
		if event.Type != EventRuntimeSnapshot {
			t.Fatalf("initial runtime event = %q, want snapshot", event.Type)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for initial runtime snapshot")
	}
	claimedAt := time.Now().UTC()
	if err := manager.PublishQueueUserTurns(context.Background(), handle, QueueUserTurnUpdate{
		ClaimedSteerItemID: "steer-item-1", ClaimedSteerText: "change direction", ClaimedSteerTimestamp: claimedAt,
	}); err != nil {
		t.Fatalf("publish claimed steer: %v", err)
	}
	select {
	case event := <-subscription.C:
		if event.Type != EventRuntimeDelta || event.Delta == nil || len(event.Delta.SteerTurnUpserts) != 1 {
			t.Fatalf("claimed steer event = %#v, want one runtime delta upsert", event)
		}
		if got := event.Delta.SteerTurnUpserts[0]; got.ItemID != "steer-item-1" || got.Status != "claimed" {
			t.Fatalf("claimed steer delta = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for claimed steer runtime delta")
	}

	claimed, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("claimed snapshot: %v", err)
	}
	if got := claimed.CurrentRunView.SteerTurns; len(got) != 1 || got[0].Status != "claimed" || got[0].AfterMessageID < 0 {
		t.Fatalf("claimed steer turns = %#v", got)
	}

	durable := chatview.UITurn{
		TurnID: "turn-steer-1", Role: "user", Text: "change direction", ID: "message-steer-1", Timestamp: time.Now().UTC(),
	}
	if err := manager.PublishQueueUserTurns(context.Background(), handle, QueueUserTurnUpdate{
		PersistedTurns: []chatview.UITurn{durable}, AppliedSteerItemID: "steer-item-1", AppliedSteerTurn: &durable,
	}); err != nil {
		t.Fatalf("publish applied steer: %v", err)
	}
	applied, err := manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("applied snapshot: %v", err)
	}
	if got := applied.CurrentRunView.SteerTurns; len(got) != 1 || got[0].Status != "applied" || got[0].TurnID != durable.TurnID {
		t.Fatalf("applied steer turns = %#v", got)
	}
	if got := applied.CurrentRunView.UserTurns; len(got) != 2 || got[1].TurnID != durable.TurnID {
		t.Fatalf("durable user turns = %#v", got)
	}
}

// TestPublishQueueUserTurnsAnchorsClaimedSteerAfterCommittedStepOutput pins
// the fix for a steer rendering above assistant output that preceded it. The
// commit barrier runs on the model loop while agent events are consumed on
// another goroutine; a claimed steer that names the committed step must wait
// until that step's step_end marker has been consumed before it reads the
// projection to compute its anchor.
func TestPublishQueueUserTurnsAnchorsClaimedSteerAfterCommittedStepOutput(t *testing.T) {
	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-steer-anchor")
	handle, err := manager.StartRunWithAdmissionBuilderHandle(
		context.Background(), testBotID, testSessionID, testRunID,
		func(_ context.Context, _ RunHandle) (RunAdmissionView, error) {
			return RunAdmissionView{RequestUserTurn: &chatview.UITurn{
				TurnID: "turn-root", Role: "user", Text: "original", Timestamp: time.Now(),
			}}, nil
		},
		make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	ctx := context.Background()
	// Step 0 output that the consumer has already applied.
	for _, ev := range []native.StreamEvent{
		{Type: native.EventTextStart},
		{Type: native.EventTextDelta, Delta: "early"},
		{Type: native.EventTextEnd},
	} {
		if _, err := manager.HandleAgentEvent(ctx, handle, ev); err != nil {
			t.Fatalf("publish %s: %v", ev.Type, err)
		}
	}

	// The commit for step 0 publishes the claimed steer now, but the tail of
	// step 0 (a tool block plus the step_end marker) is still in flight.
	stepZero := 0
	published := make(chan error, 1)
	go func() {
		published <- manager.PublishQueueUserTurns(ctx, handle, QueueUserTurnUpdate{
			ClaimedSteerItemID: "steer-1", ClaimedSteerText: "change course",
			ClaimedSteerTimestamp: time.Now().UTC(), AfterStepIndex: &stepZero,
		})
	}()
	select {
	case err := <-published:
		t.Fatalf("steer published before step 0 was consumed: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	// Late tail of step 0 arrives and is consumed.
	for _, ev := range []native.StreamEvent{
		{Type: native.EventToolCallStart, ToolCallID: "call-1", ToolName: "exec"},
		{Type: native.EventToolCallEnd, ToolCallID: "call-1", ToolName: "exec", Result: "ok"},
		{Type: native.EventStepEnd, StepNumber: 0},
	} {
		if _, err := manager.HandleAgentEvent(ctx, handle, ev); err != nil {
			t.Fatalf("publish late %s: %v", ev.Type, err)
		}
	}
	select {
	case err := <-published:
		if err != nil {
			t.Fatalf("publish claimed steer: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("steer publish did not resume after step_end was consumed")
	}

	snapshot, err := manager.Snapshot(ctx, testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	run := snapshot.CurrentRunView
	if run == nil || len(run.SteerTurns) != 1 {
		t.Fatalf("steer turns = %#v", run)
	}
	maxID := -1
	for _, message := range run.Messages {
		if message.ID > maxID {
			maxID = message.ID
		}
	}
	if got := run.SteerTurns[0].AfterMessageID; got != maxID {
		t.Fatalf("steer anchored after message %d, want %d (the last message of the committed step)", got, maxID)
	}
	if len(run.Messages) < 2 {
		t.Fatalf("expected text and tool blocks before the steer, got %#v", run.Messages)
	}
}

func TestPublishQueueUserTurnsWithoutStepIndexDoesNotWait(t *testing.T) {
	manager := testRuntimeManager(t, NewMemoryBackend(), "owner-steer-nowait")
	handle, err := manager.StartRunWithAdmissionBuilderHandle(
		context.Background(), testBotID, testSessionID, testRunID,
		func(_ context.Context, _ RunHandle) (RunAdmissionView, error) {
			return RunAdmissionView{RequestUserTurn: &chatview.UITurn{
				TurnID: "turn-root", Role: "user", Text: "original", Timestamp: time.Now(),
			}}, nil
		},
		make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1),
	)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		done <- manager.PublishQueueUserTurns(context.Background(), handle, QueueUserTurnUpdate{
			ClaimedSteerItemID: "steer-1", ClaimedSteerText: "recovered", ClaimedSteerTimestamp: time.Now().UTC(),
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("publish without step index: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("publish without AfterStepIndex must not block")
	}
}
