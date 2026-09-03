package application

import (
	"context"
	"errors"
	"sync"
	"testing"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type unavailableContinuationMessageService struct {
	*recordingMessageService
	err   error
	calls int
}

func (s *unavailableContinuationMessageService) Persist(
	context.Context,
	messagepkg.PersistInput,
) (messagepkg.Message, error) {
	s.calls++
	return messagepkg.Message{}, s.err
}

func (s *unavailableContinuationMessageService) PersistRound(
	context.Context,
	[]messagepkg.PersistInput,
	messagepkg.RoundPersistenceOptions,
) ([]messagepkg.Message, bool, error) {
	s.calls++
	return nil, true, s.err
}

type continuationRunConfigCapture struct {
	mu     sync.Mutex
	calls  int
	budget int
	runID  string
}

func (c *continuationRunConfigCapture) apply(
	_ context.Context,
	cfg native.RunConfig,
) (native.RunConfig, error) {
	c.mu.Lock()
	c.calls++
	c.budget = cfg.ContextBudgetMaxTokens
	c.runID = cfg.RunID
	c.mu.Unlock()
	return cfg.RefreshContextFrag(), nil
}

func (c *continuationRunConfigCapture) snapshot() (calls, budget int, runID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls, c.budget, c.runID
}

func TestRuntimeDecisionContinuationsPropagateResolvedBudgetAndAdmittedRunIDToAgentStream(t *testing.T) {
	tests := []struct {
		name        string
		continueRun func(*Service) error
	}{
		{
			name: "user input",
			continueRun: func(service *Service) error {
				return service.continueUserInputSession(
					context.Background(),
					userinput.Request{
						ID: "user-input", BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
						ToolCallID: "ask-user-call", ToolName: "ask_user", SourcePlatform: "web",
					},
					UserInputResponseInput{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID},
					lifecycleTestRunID,
					sessionruntime.RunHandle{},
					&continuationLifecycleResult{},
					nil,
				)
			},
		},
		{
			name: "tool approval",
			continueRun: func(service *Service) error {
				return service.continueToolApprovalSession(
					context.Background(),
					toolapproval.Request{
						ID: "tool-approval", BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
						ToolCallID: "approved-tool-call", ToolName: "container_exec", SourcePlatform: "web",
					},
					ToolApprovalResponseInput{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID},
					lifecycleTestRunID,
					sessionruntime.RunHandle{},
					&continuationLifecycleResult{},
					nil,
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
			capture := &continuationRunConfigCapture{}
			fixture.service.agent = native.New(native.Deps{
				Logger:             fixture.service.logger,
				ContextViewApplier: capture.apply,
			})

			if err := tt.continueRun(fixture.service); err != nil {
				t.Fatalf("continuation error = %v", err)
			}
			calls, budget, runID := capture.snapshot()
			if calls == 0 {
				t.Fatal("ContextView applier was not reached through agent.Stream")
			}
			if budget != 128000 {
				t.Fatalf("ContextBudgetMaxTokens seen by agent.Stream = %d, want 128000", budget)
			}
			if runID != lifecycleTestRunID {
				t.Fatalf("RunID seen by agent.Stream = %q, want admitted run ID %q", runID, lifecycleTestRunID)
			}
		})
	}
}

func TestRuntimeOwnedDecisionContinuationsRetainLifecycleWithoutAssistantMetadata(t *testing.T) {
	tests := []struct {
		name        string
		continueRun func(*Service, *continuationLifecycleResult) error
	}{
		{
			name: "user input",
			continueRun: func(service *Service, lifecycle *continuationLifecycleResult) error {
				return service.continueUserInputSession(
					context.Background(),
					userinput.Request{
						ID: "user-input", BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
						ToolCallID: "ask-user-call", ToolName: "ask_user", SourcePlatform: "web",
					},
					UserInputResponseInput{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID},
					lifecycleTestRunID,
					sessionruntime.RunHandle{},
					lifecycle,
					nil,
				)
			},
		},
		{
			name: "tool approval",
			continueRun: func(service *Service, lifecycle *continuationLifecycleResult) error {
				return service.continueToolApprovalSession(
					context.Background(),
					toolapproval.Request{
						ID: "tool-approval", BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
						ToolCallID: "approved-tool-call", ToolName: "container_exec", SourcePlatform: "web",
					},
					ToolApprovalResponseInput{BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID},
					lifecycleTestRunID,
					sessionruntime.RunHandle{},
					lifecycle,
					nil,
				)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
			messages := &unavailableContinuationMessageService{
				recordingMessageService: &recordingMessageService{},
				err:                     errors.New("assistant message store unavailable"),
			}
			fixture.service.messageService = messages
			lifecycle := &continuationLifecycleResult{}

			if err := tt.continueRun(fixture.service, lifecycle); !errors.Is(err, messages.err) {
				t.Fatalf("continuation error = %v, want assistant persistence failure", err)
			}
			if lifecycle.snapshot == nil {
				t.Fatal("runtime-owned continuation lost its in-memory lifecycle snapshot")
			}
			if messages.calls == 0 {
				t.Fatal("test did not exercise unavailable assistant persistence")
			}
			if creates := fixture.lifecycles.creates(); len(creates) != 0 {
				t.Fatalf("inner continuation lifecycle writes = %d, want 0", len(creates))
			}

			fixture.service.persistRuntimeDecisionLifecycle(
				context.Background(),
				sessionruntime.Command{
					RunID: lifecycleTestRunID, BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
				},
				lifecycle,
				errors.New("runtime publication failed after assistant persistence"),
			)

			assertDirectLifecycle(
				t,
				fixture.lifecycles,
				lifecycleTestRunID,
				contextLifecycleStatusFailedProvider,
				"",
			)
		})
	}
}
