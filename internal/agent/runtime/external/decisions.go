package external

import (
	"context"
	"log/slog"
	"time"

	"github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
)

// ApprovalCard is the stream event that shows an approval to the user.
func ApprovalCard(req approval.Request) event.StreamEvent {
	return event.StreamEvent{
		Type:       event.ToolApprovalRequest,
		ToolCallID: req.ToolCallID,
		ToolName:   req.ToolName,
		Input:      req.ToolInput,
		ApprovalID: req.ID,
		ShortID:    req.ShortID,
		Status:     approval.NormalizedStatus(req.Status),
		Metadata:   map[string]any{"approval": approval.RequestMetadata(req)},
	}
}

// UserInputCard is the stream event that puts a question to the user.
func UserInputCard(req userinput.Request) event.StreamEvent {
	return event.StreamEvent{
		Type:        event.UserInputRequest,
		ToolCallID:  req.ToolCallID,
		ToolName:    req.ToolName,
		Input:       req.Input,
		UserInputID: req.ID,
		ShortID:     req.ShortID,
		Status:      FirstNonEmpty(req.Status, userinput.StatusPending),
		Metadata:    userinput.DeferredMetadata(req),
	}
}

// ApprovalCall is a permission question a runtime asks about one tool call.
type ApprovalCall struct {
	ToolCallID string
	ToolName   string
	Input      any
	Options    []approval.PermissionOption
}

// DecideApproval forwards a runtime's own permission question to the user.
// The runtime's permission mode is the only policy layer for its native
// tools; Memoh's rules govern Memoh's tools, which the gateway enforces. A
// failure fails closed, as cancelled.
func DecideApproval(ctx context.Context, service approval.FlowService, waiter func(string) func(), input PromptInput, call ApprovalCall, emit func(event.StreamEvent), logger *slog.Logger) approval.FlowResult {
	if service == nil {
		return approval.FlowResult{Status: approval.StatusRejected, DecisionReason: "approval service unavailable"}
	}
	result, err := approval.RunRuntimeFlow(ctx, service, approval.FlowRequest{
		Input: approval.CreatePendingInput{
			BotID:                        input.BotID,
			SessionID:                    input.ThreadID,
			RouteID:                      input.RouteID,
			ChannelIdentityID:            input.ChannelIdentityID,
			RequestedByChannelIdentityID: input.ChannelIdentityID,
			ToolCallID:                   call.ToolCallID,
			ToolName:                     call.ToolName,
			ToolInput:                    call.Input,
			Options:                      call.Options,
			SourcePlatform:               input.CurrentPlatform,
			ReplyTarget:                  input.ReplyTarget,
			ConversationType:             input.ConversationType,
		},
		Interactive:    input.CanRequestUserInput,
		RegisterWaiter: waiter,
		Emit: func(req approval.Request) bool {
			emit(ApprovalCard(req))
			return true
		},
	})
	if err != nil {
		if ctx.Err() != nil {
			return approval.FlowResult{Status: approval.StatusCancelled, DecisionReason: "the turn ended before a decision arrived"}
		}
		logger.ErrorContext(ctx, "approval flow failed", slog.String("session_id", input.ThreadID), slog.Any("error", err))
		return approval.FlowResult{Status: approval.StatusCancelled, DecisionReason: "approval flow failed"}
	}
	return result
}

// Question is one question a runtime puts to the user.
type Question struct {
	ToolCallID string
	// Input is the ask_user card's input.
	Input any
	// ProviderMetadata says which runtime request the answer goes back to.
	ProviderMetadata map[string]any
}

// AskUser puts a runtime's question to the user and waits for the answer.
func AskUser(ctx context.Context, service userinput.FlowService, input PromptInput, question Question, emit func(event.StreamEvent), logger *slog.Logger) (userinput.Request, error) {
	expiresAt := time.Now().Add(userinput.DefaultWaitTimeout + time.Minute)
	flow, err := userinput.RunFlow(ctx, service, userinput.FlowRequest{
		Input: userinput.CreatePendingInput{
			BotID:                        input.BotID,
			SessionID:                    input.ThreadID,
			RouteID:                      input.RouteID,
			ChannelIdentityID:            input.ChannelIdentityID,
			RequestedByChannelIdentityID: input.ChannelIdentityID,
			ToolCallID:                   question.ToolCallID,
			ToolName:                     userinput.ToolNameAskUser,
			Input:                        question.Input,
			ProviderMetadata:             question.ProviderMetadata,
			SourcePlatform:               input.CurrentPlatform,
			ReplyTarget:                  input.ReplyTarget,
			ConversationType:             input.ConversationType,
			ExpiresAt:                    &expiresAt,
		},
		ActorChannelIdentityID: input.ChannelIdentityID,
		Interactive:            input.CanRequestUserInput,
		WaitTimeout:            userinput.DefaultWaitTimeout,
		Emit: func(req userinput.Request) bool {
			emit(UserInputCard(req))
			return true
		},
		NonInteractiveReason: "the agent asked a question without an interactive stream",
		UndeliveredReason:    "the agent's question was not delivered to the interactive stream",
		TimeoutReason:        "the agent's question timed out",
		AbortReason:          "the agent's question was aborted",
	})
	if err != nil && ctx.Err() == nil {
		logger.ErrorContext(ctx, "user input flow failed", slog.String("session_id", input.ThreadID), slog.Any("error", err))
	}
	return flow.Request, err
}
