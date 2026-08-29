package application

import (
	"context"
	"errors"
	"testing"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
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

			if err := tt.continueRun(fixture.service, lifecycle); err != nil {
				t.Fatalf("continuation error = %v, want nil", err)
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
