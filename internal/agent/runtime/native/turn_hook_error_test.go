package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
	"github.com/felinics/memoh/internal/hooks"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

// turnHookRecorder runs every hook command as a success with no decision and
// keeps the request each command read on stdin.
type turnHookRecorder struct {
	*mockExecContainerService
	requests chan hooks.Request
}

func (s *turnHookRecorder) Exec(stream pb.ContainerService_ExecServer) error {
	if _, err := stream.Recv(); err != nil {
		return err
	}
	var stdin []byte
	for {
		input, err := stream.Recv()
		if err != nil {
			break
		}
		stdin = append(stdin, input.GetStdinData()...)
	}
	var req hooks.Request
	if err := json.Unmarshal(stdin, &req); err == nil {
		s.requests <- req
	}
	return stream.Send(&pb.ExecOutput{Stream: pb.ExecOutput_EXIT})
}

func newTurnErrorHook(t *testing.T) (Deps, <-chan hooks.Request) {
	t.Helper()
	svc := &turnHookRecorder{mockExecContainerService: newMockExecContainerService(), requests: make(chan hooks.Request, 4)}
	svc.written[hooks.DefaultConfigPath] = []byte(`{
		"version": 1,
		"enabled": true,
		"hooks": [{
			"name": "turn error",
			"event": "TurnError",
			"actions": [{"type": "command", "command": "record-turn-error"}]
		}]
	}`)
	provider, cleanup := setupExecTestInfra(t, svc)
	t.Cleanup(cleanup)
	return Deps{BridgeProvider: provider, HookService: hooks.NewService(nil, provider)}, svc.requests
}

func receiveTurnHook(t *testing.T, requests <-chan hooks.Request) hooks.Request {
	t.Helper()
	select {
	case req := <-requests:
		return req
	case <-time.After(5 * time.Second):
		t.Fatal("turn error hook did not run")
		return hooks.Request{}
	}
}

// A turn that context preparation failed hands the turn hook the failure's
// catalog code; the internal text of the cause stays out of the hook.
func TestContextFailureHandsTheTurnHookItsCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"protected overflow", fmt.Errorf("%w: private window", contextfrag.ErrProtectedContextOverflow), "context.protected_overflow"},
		{"budget unsatisfied", fmt.Errorf("%w: private window", contextfrag.ErrBudgetUnsatisfied), "context.budget_unsatisfied"},
		{"preparation failure", errors.New("private preparation failure"), "runtime_run_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps, requests := newTurnErrorHook(t)
			deps.ContextViewApplier = budgetErrorApplier(tt.err)
			for range New(deps).Stream(context.Background(), RunConfig{
				Model:    &sdk.Model{ID: "hook-stream", Provider: &preflightCountingProvider{}, Type: sdk.ModelTypeChat},
				Identity: SessionContext{BotID: "bot-1"},
			}) {
			}
			req := receiveTurnHook(t, requests)
			if req.Event != hooks.EventTurnError || req.Error != tt.want || req.Turn["error"] != tt.want || strings.Contains(req.Error, "private") {
				t.Fatalf("turn hook request = %#v, want error %q", req, tt.want)
			}
		})
	}
}

// The non-streaming loop hands the turn hook the code of a context sentinel
// in the same way.
func TestGenerateContextFailureHandsTheTurnHookItsCode(t *testing.T) {
	t.Parallel()
	deps, requests := newTurnErrorHook(t)
	deps.ContextViewApplier = budgetErrorApplier(fmt.Errorf("%w: private window", contextfrag.ErrBudgetUnsatisfied))
	if _, err := New(deps).Generate(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "hook-generate", Provider: &preflightCountingProvider{}, Type: sdk.ModelTypeChat},
		Identity: SessionContext{BotID: "bot-1"},
	}); err == nil {
		t.Fatal("Generate() error = nil, want the budget failure")
	}
	if req := receiveTurnHook(t, requests); req.Error != "context.budget_unsatisfied" {
		t.Fatalf("turn hook error = %q, want context.budget_unsatisfied", req.Error)
	}
}

// A turn that failed on the runtime's own work hands the turn hook
// runtime_run_failed; the internal text of the failure stays out of the hook.
func TestLocalFailureHandsTheTurnHookItsCode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		cfg  func() RunConfig
	}{
		{"stream start", func() RunConfig {
			return RunConfig{Model: &sdk.Model{ID: "private-model"}, Identity: SessionContext{BotID: "bot-1"}}
		}},
		{"step commit", func() RunConfig {
			var invocations atomic.Int32
			return RunConfig{
				Model:            &sdk.Model{ID: "hook-commit", Provider: agentStreamTestProvider(streamScript(&invocations, scriptText("answer")))},
				Messages:         []sdk.Message{sdk.UserMessage("task")},
				ContextMutations: contextfrag.NewMutationLedger(),
				Identity:         SessionContext{BotID: "bot-1"},
				OnStepCommitted: func(context.Context, int, *step.Record) (StepDirective, error) {
					return StepDirective{}, errors.New("private commit failure")
				},
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			deps, requests := newTurnErrorHook(t)
			for range New(deps).Stream(context.Background(), tt.cfg()) {
			}
			req := receiveTurnHook(t, requests)
			if req.Event != hooks.EventTurnError || req.Error != codeRunFailed || req.Turn["error"] != codeRunFailed || strings.Contains(req.Error, "private") {
				t.Fatalf("turn hook request = %#v, want error %q", req, codeRunFailed)
			}
		})
	}
}

// The non-streaming loop hands the turn hook runtime_run_failed for its own
// failures in the same way.
func TestGenerateLocalFailureHandsTheTurnHookItsCode(t *testing.T) {
	t.Parallel()
	deps, requests := newTurnErrorHook(t)
	if _, err := New(deps).Generate(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "private-model"},
		Identity: SessionContext{BotID: "bot-1"},
	}); err == nil {
		t.Fatal("Generate() error = nil, want the missing provider")
	}
	if req := receiveTurnHook(t, requests); req.Error != codeRunFailed {
		t.Fatalf("turn hook error = %q, want %s", req.Error, codeRunFailed)
	}
}
