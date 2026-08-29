package application

import (
	"context"
	"testing"

	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/schedule"
)

func TestTriggerScheduleACPPersistsCompletedLifecycle(t *testing.T) {
	pool := &recordingACPPrompter{result: acpclient.PromptResult{Text: "done", StopReason: "end_turn"}}
	messages := &recordingMessageService{}
	lifecycles := &recordingContextLifecycleStore{}
	service := newACPLifecycleService(t, pool, messages, lifecycles)

	result, err := service.triggerScheduleACP(
		context.Background(),
		lifecycleTestBotID,
		schedule.TriggerPayload{
			SessionID:       lifecycleTestSessionID,
			Command:         "run scheduled task",
			OwnerUserID:     "user-1",
			ACPModelID:      "test-model",
			ReasoningEffort: "medium",
		},
		"",
		lifecycleTestRunID,
		ACPSessionExecutionInfo{
			AgentID:               "codex",
			ProjectPath:           "/data/app",
			RuntimeOwnerAccountID: "user-1",
		},
	)
	if err != nil {
		t.Fatalf("triggerScheduleACP() error = %v", err)
	}
	if result.Status != "ok" || result.Text != "done" {
		t.Fatalf("triggerScheduleACP() result = %#v, want completed output", result)
	}
	if pool.input.RunID != lifecycleTestRunID || pool.input.SessionID != lifecycleTestSessionID {
		t.Fatalf("ACP prompt identity = (run %q, session %q), want (%q, %q)", pool.input.RunID, pool.input.SessionID, lifecycleTestRunID, lifecycleTestSessionID)
	}
	row, snapshot := requireACPLifecycle(t, lifecycles, lifecycleTestRunID, contextLifecycleStatusCompleted)
	if row.ErrorCode.Valid {
		t.Fatalf("completed lifecycle error code = %#v, want none", row.ErrorCode)
	}
	if snapshot.AssistantMessageID != "message-id" {
		t.Fatalf("assistant message ID = %q, want message-id", snapshot.AssistantMessageID)
	}
}
