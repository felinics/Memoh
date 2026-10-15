package application

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/testutil/sessionledger"
)

func TestSubagentControlsRecheckPendingAcrossTerminalTransition(t *testing.T) {
	for _, operation := range []string{"snapshot", "wait", "stop", "external-control", "generation", "churn"} {
		t.Run(operation, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			botID, childID := uuid.NewString(), uuid.NewString()
			manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: sessionledger.New(), Fence: queueTestFence{}})
			t.Cleanup(func() { _ = manager.CloseContext(context.Background()) })
			admission, err := manager.Admit(ctx, sessionruntime.AdmitInput{BotID: botID, SessionID: childID, InvocationID: "child-turn", Payload: []byte(`{"query":"work"}`), Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			child := thread.Thread{ID: childID, BotID: botID, ParentThreadID: "parent", SessionMode: thread.TypeSubagent, RuntimeType: "codex", BotAgentID: "backend", RuntimeMetadata: map[string]any{"runtime_owner_account_id": "caller"}}
			reads := 0
			successorRunID := ""
			staleRead := 1
			if operation == "wait" {
				staleRead = 2
			}
			svc := &Service{logger: slog.New(slog.DiscardHandler), botPermissions: allowWorkspaceExecForBot(botID, "caller"), sessionService: &fakeBackgroundSessionService{getFn: func(ctx context.Context, id string) (thread.Thread, error) {
				if id == "parent" {
					return thread.Thread{ID: id, BotID: botID, SessionMode: thread.TypeChat}, nil
				}
				reads++
				if reads == staleRead {
					stale := child
					stale.RuntimeMetadata = map[string]any{"runtime_owner_account_id": "caller", "external_execution_run": admission.RunID}
					if _, err := manager.FinishRun(ctx, admission.Handle, sessionruntime.RunStatusCompleted); err != nil {
						return thread.Thread{}, err
					}
					if operation == "generation" {
						successor, err := manager.Admit(ctx, sessionruntime.AdmitInput{BotID: botID, SessionID: childID, InvocationID: "successor-turn", Payload: []byte(`{"query":"followup"}`), Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
							return sessionruntime.RunAdmissionView{}, nil
						}}})
						if err != nil {
							return thread.Thread{}, err
						}
						successorRunID = successor.RunID
						child.RuntimeMetadata["external_execution_run"] = successor.RunID
					}
					return stale, nil
				}
				if operation == "churn" {
					child.RuntimeMetadata = map[string]any{"external_execution_run": uuid.NewString()}
				}
				return child, nil
			}}}
			svc.SetSessionRuntime(manager)
			svc.SetSubagentBackends(&subagentCatalogStub{enabled: true})
			svc.SetExternalRuntimes(noticeTestDriver{kind: "codex"})
			parent := tools.SessionContext{BotID: botID, SessionID: "parent", UserID: "caller"}
			switch operation {
			case "generation":
				state, err := svc.SnapshotSubagent(ctx, parent, childID)
				if err != nil || state.RunID != successorRunID || state.Status != sessionruntime.RunStatusRunning || !state.Active || state.Code != "" {
					t.Fatalf("changed execution generation became unknown: state=%+v err=%v", state, err)
				}
			case "churn":
				_, err := svc.SnapshotSubagent(ctx, parent, childID)
				if apperror.CodeOf(err) != apperror.CodeSessionBusy || reads != 4 {
					t.Fatalf("changing marker retries were not bounded: reads=%d err=%v", reads, err)
				}
			case "snapshot":
				state, err := svc.SnapshotSubagent(ctx, parent, childID)
				if err != nil || state.Status != sessionruntime.RunStatusCompleted || state.Active || state.Code != "" {
					t.Fatalf("normal terminal transition became unknown: state=%+v err=%v", state, err)
				}
			case "wait":
				if err := svc.WaitSubagentIdle(ctx, parent, childID); err != nil {
					t.Fatalf("normal terminal transition rejected queued work: %v", err)
				}
			case "stop":
				result, err := svc.ControlSubagent(ctx, tools.SubagentControlInput{Parent: parent, SessionID: childID, RunID: admission.RunID, Action: "stop"})
				if err != nil || result.Status == "execution_unknown" || result.Accepted {
					t.Fatalf("normal terminal transition changed stop result: result=%+v err=%v", result, err)
				}
			case "external-control":
				stale, err := svc.sessionService.Get(ctx, childID)
				if err != nil {
					t.Fatal(err)
				}
				if err := svc.externalSubagentExecutionGate(ctx, stale); err != nil {
					t.Fatalf("normal terminal transition blocked external control: %v", err)
				}
			}
		})
	}
}
