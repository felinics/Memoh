package application

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

func TestRuntimeDecisionTerminalWithoutRecoverableSnapshotPersistsMinimalLifecycle(t *testing.T) {
	tests := []struct {
		name              string
		events            []native.StreamEvent
		wantLifecycle     string
		wantRuntimeStatus string
	}{
		{
			name:              "completed",
			events:            []native.StreamEvent{{Type: native.EventAgentEnd}},
			wantLifecycle:     contextLifecycleStatusCompleted,
			wantRuntimeStatus: sessionruntime.RunStatusCompleted,
		},
		{
			name: "provider failure",
			events: []native.StreamEvent{
				{Type: native.EventError, Error: "private provider detail"},
				{Type: native.EventAgentAbort},
			},
			wantLifecycle:     contextLifecycleStatusFailedProvider,
			wantRuntimeStatus: sessionruntime.RunStatusErrored,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, handle := newWaitingDecisionRuntime(t)
			lifecycles := &recordingContextLifecycleStore{}
			service := &Service{decisionRuntime: manager, contextLifecycles: lifecycles}

			service.continueRuntimeDecision(context.Background(), sessionruntime.Command{
				BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
				RunID: lifecycleTestRunID, Generation: handle.Generation,
			}, func(_ context.Context, _ *continuationLifecycleResult, events chan<- WSStreamEvent) error {
				events <- runtimeDecisionEvent(t, native.StreamEvent{Type: native.EventAgentStart})
				for _, event := range tt.events {
					events <- runtimeDecisionEvent(t, event)
				}
				return nil
			})

			if len(lifecycles.creates) != 1 || lifecycles.creates[0].Status != tt.wantLifecycle {
				t.Fatalf("lifecycle creates = %#v, want one %s row", lifecycles.creates, tt.wantLifecycle)
			}
			var snapshot contextfrag.LifecycleSnapshot
			if err := json.Unmarshal(lifecycles.creates[0].Snapshot, &snapshot); err != nil {
				t.Fatalf("decode minimal lifecycle: %v", err)
			}
			if snapshot.Version != 1 || snapshot.Counts != (contextfrag.ManifestCounts{}) {
				t.Fatalf("lifecycle snapshot = %#v, want minimal version 1 snapshot", snapshot)
			}
			if bytes.Contains(lifecycles.creates[0].Snapshot, []byte("private provider detail")) {
				t.Fatalf("lifecycle snapshot leaked private provider detail: %s", lifecycles.creates[0].Snapshot)
			}
			runtimeSnapshot, err := manager.Snapshot(context.Background(), lifecycleTestBotID, lifecycleTestSessionID)
			if err != nil {
				t.Fatal(err)
			}
			if runtimeSnapshot.CurrentRunView == nil || runtimeSnapshot.CurrentRunView.Status != tt.wantRuntimeStatus {
				t.Fatalf("runtime terminal = %#v, want %s", runtimeSnapshot.CurrentRunView, tt.wantRuntimeStatus)
			}
		})
	}
}

func TestStaleDecisionContinuationDoesNotRecoverLifecycleFromAssistantMetadata(t *testing.T) {
	manager, handle := newWaitingDecisionRuntime(t)
	snapshot, ok := lifecycleTestRunConfig().ContextLifecycle.Snapshot()
	if !ok {
		t.Fatal("test lifecycle snapshot is unavailable")
	}
	metadata, err := json.Marshal(map[string]any{
		contextfrag.MetadataContextLifecycleKey: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	lifecycles := &recordingContextLifecycleStore{metadata: metadata}
	service := &Service{decisionRuntime: manager, contextLifecycles: lifecycles}

	service.continueRuntimeDecision(context.Background(), sessionruntime.Command{
		BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
		RunID: lifecycleTestRunID, Generation: handle.Generation + "-stale",
	}, func(context.Context, *continuationLifecycleResult, chan<- WSStreamEvent) error {
		t.Fatal("stale continuation unexpectedly ran")
		return nil
	})

	if len(lifecycles.creates) != 0 {
		t.Fatalf("stale continuation created lifecycle rows from assistant metadata: %#v", lifecycles.creates)
	}
}
