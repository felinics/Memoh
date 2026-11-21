package native

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/hooks"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// laterModelCallHookService answers the first BeforeModelCall with allow and
// every later one with the configured outcome.
type laterModelCallHookService struct {
	*mockExecContainerService
	later execBehavior
	calls atomic.Int32
}

func (s *laterModelCallHookService) Exec(stream pb.ContainerService_ExecServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	b := execBehavior{stdout: `{"decision":"allow"}`}
	if s.calls.Add(1) > 1 {
		b = s.later
	}
	if b.stdout != "" {
		if err := stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_STDOUT, Data: []byte(b.stdout)}); err != nil {
			return err
		}
	}
	return stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT, ExitCode: b.exitCode})
}

func TestBeforeModelCallHookGovernsEveryModelCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		onError       string
		later         execBehavior
		wantCalls     int32
		wantDenied    bool
		wantRunFailed bool
	}{
		{
			name:       "deny stops the call",
			onError:    hooks.OnErrorFail,
			later:      execBehavior{stdout: `{"decision":"deny","reason":"no further provider requests"}`},
			wantCalls:  1,
			wantDenied: true,
		},
		{
			name:          "failing hook with on_error fail stops the call",
			onError:       hooks.OnErrorFail,
			later:         execBehavior{exitCode: 3},
			wantCalls:     1,
			wantRunFailed: true,
		},
		{
			name:       "failing hook with on_error block denies the call",
			onError:    hooks.OnErrorBlock,
			later:      execBehavior{exitCode: 3},
			wantCalls:  1,
			wantDenied: true,
		},
		{
			name:      "failing hook with on_error ignore lets the call proceed",
			onError:   hooks.OnErrorIgnore,
			later:     execBehavior{exitCode: 3},
			wantCalls: 2,
		},
	}
	for _, tc := range tests {
		for _, mode := range []string{"generate", "stream"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				t.Parallel()

				svc := &laterModelCallHookService{mockExecContainerService: newMockExecContainerService(), later: tc.later}
				svc.written[hooks.DefaultConfigPath] = fmt.Appendf(nil, `{
					"version": 1,
					"enabled": true,
					"hooks": [{
						"name": "later model call",
						"event": "BeforeModelCall",
						"actions": [{"type": "command", "command": "later-model-call", "on_error": %q}]
					}]
				}`, tc.onError)
				bridgeProvider, cleanup := setupExecTestInfra(t, svc)
				t.Cleanup(cleanup)

				model := &atomicMockProvider{handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
					if call == 1 {
						return sdk.ModelResult{
							FinishReason: sdk.FinishReasonToolCalls,
							ToolCalls:    []sdk.ToolCall{{ToolCallID: "call-1", ToolName: "lookup", Input: toolexec.ArgumentsFromValue(map[string]any{})}},
						}, nil
					}
					return sdk.ModelResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
				}}
				a := New(Deps{BridgeProvider: bridgeProvider, HookService: hooks.NewService(nil, bridgeProvider)})
				a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
					Name: "lookup",
					Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
						return toolexec.OutputFromValue("result"), nil
					},
				}}}})
				cfg := RunConfig{
					Model:            &sdk.Model{ID: "hook-model", Provider: model},
					Messages:         []sdk.Message{sdk.UserMessage("start")},
					SupportsToolCall: true,
					Identity:         SessionContext{BotID: "bot-1", SessionID: "session-1"},
				}

				var runErr error
				var failure *StreamEvent
				if mode == "stream" {
					for event := range a.Stream(context.Background(), cfg) {
						if event.Type == EventError {
							failure = &event
						}
					}
					if failure != nil {
						runErr = failure.Cause
					}
				} else {
					_, runErr = a.Generate(context.Background(), cfg)
				}

				if got := model.calls.Load(); got != tc.wantCalls {
					t.Fatalf("provider calls = %d, want %d (hook calls %d)", got, tc.wantCalls, svc.calls.Load())
				}
				if got := svc.calls.Load(); got != 2 {
					t.Fatalf("hook calls = %d, want 2", got)
				}
				if !tc.wantDenied && !tc.wantRunFailed {
					if runErr != nil {
						t.Fatalf("run error = %v, want none", runErr)
					}
					return
				}
				if runErr == nil {
					t.Fatal("run ended without an error")
				}
				if got := errors.Is(runErr, hooks.ErrDenied); got != tc.wantDenied {
					t.Fatalf("errors.Is(%v, ErrDenied) = %v, want %v", runErr, got, tc.wantDenied)
				}
				if failure != nil && failure.Code != codeRunFailed {
					t.Fatalf("stream failure code = %q, want %q", failure.Code, codeRunFailed)
				}
			})
		}
	}
}
