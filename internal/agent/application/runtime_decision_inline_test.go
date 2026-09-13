package application

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/bots"
	session "github.com/felinics/memoh/internal/chat/thread"
	sessiontest "github.com/felinics/memoh/internal/testutil/sessionruntime"
)

func TestInlineRuntimeDecisionClosesOutputWithoutFinishingProducer(t *testing.T) {
	for _, runtimeType := range []string{session.RuntimeClaudeCode, session.RuntimeCodex, session.RuntimeACPAgent} {
		for _, canceled := range []bool{false, true} {
			t.Run(runtimeType+map[bool]string{false: "/submit", true: "/cancel"}[canceled], func(t *testing.T) {
				ctx := context.Background()
				backend := sessionruntime.NewMemoryBackend()
				manager := sessiontest.New(backend, sessionruntime.Options{OwnerID: "inline-test", StateTTL: time.Minute})
				t.Cleanup(func() { _ = manager.Close() })
				if err := manager.Start(ctx); err != nil {
					t.Fatal(err)
				}
				handle, err := sessiontest.Start(ctx, manager, lifecycleTestBotID, lifecycleTestSessionID, lifecycleTestRunID,
					make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1))
				if err != nil {
					t.Fatal(err)
				}
				manager.MarkInlineDecisionRun(handle.BotID, handle.SessionID, handle.RunID)
				request := userinput.Request{
					ID: "44444444-4444-4444-8444-444444444444", BotID: handle.BotID,
					SessionID: handle.SessionID, ToolCallID: "question", ToolName: "AskUserQuestion", Status: userinput.StatusPending,
				}
				if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{
					Type:        native.EventUserInputRequest,
					UserInputID: request.ID, ToolCallID: request.ToolCallID, Status: request.Status,
				}); err != nil {
					t.Fatal(err)
				}
				resolved := request
				resolved.Status = userinput.StatusSubmitted
				if canceled {
					resolved.Status = userinput.StatusCanceled
				}
				inputs := &fakeUserInputService{target: request, resolved: resolved}
				var logs bytes.Buffer
				service := &Service{
					decisionRuntime: manager, userInput: inputs, logger: slog.New(slog.NewTextHandler(&logs, nil)),
					botPermissions: &fakeBotPermissionChecker{values: map[string]bool{handle.BotID + ":" + testACPUserInputOwnerID + ":" + bots.PermissionWorkspaceExec: true}},
					sessionService: &fakeBackgroundSessionService{getFn: func(context.Context, string) (session.Thread, error) {
						return session.Thread{
							ID: handle.SessionID, BotID: handle.BotID, RuntimeType: runtimeType,
							RuntimeMetadata: map[string]any{"runtime_owner_account_id": testACPUserInputOwnerID},
						}, nil
					}},
				}
				payload, err := json.Marshal(UserInputResponseInput{
					ActorUserID: testACPUserInputOwnerID, Canceled: canceled,
					Answers: []userinput.QuestionAnswer{{QuestionID: "q", Text: "answer"}},
				})
				if err != nil {
					t.Fatal(err)
				}
				command := sessionruntime.Command{
					ID: "inline-answer", Type: sessionruntime.CommandUserInputResponse,
					BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID, Generation: handle.Generation,
					TargetID: request.ID, Payload: payload, StreamOutput: true,
				}
				if err := service.handleRuntimeDecisionCommand(ctx, command); err != nil {
					t.Fatal(err)
				}
				page, err := backend.ReadDecisionOutput(ctx, sessionruntime.DecisionOutputRef{BotID: handle.BotID, CommandID: command.ID}, 0)
				if err != nil || !page.Done || page.Length != 0 {
					t.Fatalf("inline ACK must close immediately without agent events: %+v, %v", page, err)
				}
				if inputs.submitCalls+inputs.cancelCalls != 1 {
					t.Fatal("answer was not committed exactly once")
				}
				// A later native output is still accepted: the ACK must not end the run.
				if _, err := manager.HandleAgentEvent(ctx, handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "after answer"}); err != nil {
					t.Fatal(err)
				}
				snapshot, err := manager.Snapshot(ctx, handle.BotID, handle.SessionID)
				if err != nil || snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != sessionruntime.RunStatusRunning || len(snapshot.CurrentRunView.Messages) == 0 {
					t.Fatalf("original producer stopped after answer: %+v, %v", snapshot.CurrentRunView, err)
				}
				if err := manager.FinishRun(ctx, handle, sessionruntime.RunStatusCompleted, ""); err != nil {
					t.Fatal(err)
				}
				if logs.Len() != 0 {
					t.Fatalf("successful inline response logged a failure: %s", logs.String())
				}
			})
		}
	}
}
