package application

import (
	"context"
	"errors"
	"fmt"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

func TestExternalSubagentUnconfirmedTerminationBlocksNextRun(t *testing.T) {
	for _, failurePoint := range []string{"none", "before_execution", "after_execution"} {
		t.Run(fmt.Sprintf("metadata_failure=%s", failurePoint), func(t *testing.T) {
			messages := &recordingMessageService{}
			lifecycles := &recordingContextLifecycleStore{}
			s := newACPLifecycleService(t, &recordingACPPrompter{}, messages, lifecycles)
			s.SetSubagentBackends(&subagentCatalogStub{enabled: true})
			manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: sessionledger.New(), Fence: queueTestFence{}})
			t.Cleanup(func() { _ = manager.CloseContext(context.Background()) })
			s.SetSessionRuntime(manager)
			meta := map[string]any{"runtime_owner_account_id": "user-1", "project_path": "/data/work"}
			s.sessionService = &fakeBackgroundSessionService{getFn: func(_ context.Context, id string) (session.Thread, error) {
				if id == "parent" {
					return session.Thread{ID: id, BotID: lifecycleTestBotID, Type: session.TypeChat}, nil
				}
				return session.Thread{ID: id, BotID: lifecycleTestBotID, Type: session.TypeSubagent, SessionMode: session.TypeSubagent, RuntimeType: "codex", BotAgentID: "backend", ParentThreadID: "parent", CreatedByUserID: "user-1", RuntimeMetadata: meta}, nil
			}, mergeRuntimeMetadataFn: func(writeCtx context.Context, id, _ string, delta map[string]any) (session.Thread, error) {
				if _, bounded := writeCtx.Deadline(); !bounded {
					t.Error("runtime metadata persistence has no deadline")
				}
				if failurePoint == "before_execution" || (failurePoint == "after_execution" && delta["codex_thread_id"] != nil) {
					return session.Thread{}, errors.New("metadata write failed")
				}
				for key, value := range delta {
					meta[key] = value
				}
				return session.Thread{ID: id}, nil
			}}
			handle := sessionruntime.RunHandle{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, RunID: lifecycleTestRunID, FencingToken: 1}
			ctx, cancel := context.WithCancel(withSubagentRunHandle(context.Background(), handle))
			defer cancel()
			calls := 0
			s.SetExternalRuntimes(noticeTestDriver{kind: "codex", prompt: func(context.Context, external.PromptInput) (external.PromptResult, error) {
				calls++
				cancel()
				return external.PromptResult{TerminationUnconfirmed: true, Output: []sdk.Message{sdk.AssistantMessage("partial evidence")}, RuntimeMetadata: map[string]any{"codex_thread_id": "child-native"}}, external.ErrTerminationUnconfirmed
			}})
			input := tools.ExternalSubagentRun{Parent: tools.SessionContext{BotID: lifecycleTestBotID, SessionID: "parent", UserID: "user-1"}, SessionID: lifecycleTestSessionID, RuntimeType: "codex", BotAgentID: "backend", Query: "work", Admission: tools.SubagentAdmission{RunID: handle.RunID, TurnID: handle.RunID}}
			result, err := s.RunExternalSubagent(ctx, input)
			if failurePoint == "before_execution" {
				if apperror.CodeOf(err) != apperror.CodeSessionHistoryInconsistent || calls != 0 {
					t.Fatalf("unsafe start err=%v calls=%d", err, calls)
				}
				return
			}
			if apperror.CodeOf(err) != apperror.CodeExternalRuntimeTerminationUnconfirmed {
				t.Fatalf("err=%v", err)
			}
			if result == nil || result.Text != "partial evidence" || !result.TerminalResolved || result.TerminalOutcome != tools.SpawnAttemptFailure || !result.ExecutionUnconfirmed {
				t.Fatalf("result=%+v", result)
			}
			if failurePoint != "after_execution" && (meta["codex_thread_id"] != "child-native" || meta["external_termination_unconfirmed"] != handle.RunID) {
				t.Fatalf("metadata=%v", meta)
			}
			requireACPLifecycle(t, lifecycles, handle.RunID, contextLifecycleStatusFailedProvider)
			handle.RunID = "55555555-5555-4555-8555-555555555555"
			input.Admission.RunID = handle.RunID
			_, err = s.RunExternalSubagent(withSubagentRunHandle(context.Background(), handle), input)
			if apperror.CodeOf(err) != apperror.CodeExternalRuntimeTerminationUnconfirmed || calls != 1 {
				t.Fatalf("re-entry err=%v calls=%d", err, calls)
			}
			_, _, _, err = s.runtimeControlTarget(context.Background(), RuntimeControlRequest{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, ActorID: "user-1"})
			if apperror.CodeOf(err) != apperror.CodeExternalRuntimeTerminationUnconfirmed {
				t.Fatalf("control bypassed gate: %v", err)
			}
			_, err = s.PrepareExternalFork(context.Background(), lifecycleTestBotID, lifecycleTestSessionID, "")
			if apperror.CodeOf(err) != apperror.CodeExternalRuntimeTerminationUnconfirmed {
				t.Fatalf("fork bypassed gate: %v", err)
			}
		})
	}
}
