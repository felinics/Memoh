package claudecode

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
)

func (t *turnRunner) answerQuestions(ctx context.Context, requestID string, payload *controlRequestPayload) {
	input, ok := t.collectAnswers(ctx, requestID, payload)
	var line []byte
	var err error
	if ok {
		line, err = permissionAllowResponse(requestID, input, payload.ToolUseID)
	} else {
		line, err = permissionDenyResponse(requestID, "User questions were not answered.")
	}
	if err != nil || ctx.Err() != nil {
		return
	}
	_ = t.writeLine(line)
}

func (t *turnRunner) collectAnswers(ctx context.Context, requestID string, payload *controlRequestPayload) (map[string]any, bool) {
	if t.userInput == nil {
		return nil, false
	}
	raw, err := json.Marshal(payload.Input)
	if err != nil {
		return nil, false
	}
	var input struct {
		Questions []userinput.ChoiceQuestion `json:"questions"`
	}
	if json.Unmarshal(raw, &input) != nil || len(input.Questions) == 0 || len(input.Questions) > userinput.MaxQuestionsPerRequest {
		return nil, false
	}
	request, err := t.requestUserInput(ctx, firstNonEmpty(payload.ToolUseID, requestID), userinput.ChoiceQuestionsInput(input.Questions), map[string]any{"source": "claude_request_user_input", "request_id": requestID})
	if err != nil || request.Status != userinput.StatusSubmitted {
		return nil, false
	}
	picked, ok := userinput.ChoiceAnswers(request.Result, len(input.Questions))
	if !ok {
		return nil, false
	}
	answers := make(map[string]string, len(input.Questions))
	for i, question := range input.Questions {
		answers[question.Text] = picked[i]
	}
	updated := make(map[string]any, len(payload.Input)+1)
	for key, value := range payload.Input {
		updated[key] = value
	}
	updated["answers"] = answers
	return updated, true
}

// Both native questions and MCP elicitation use the same turn-owned waiter;
// only their request/answer mapping differs.
func (t *turnRunner) requestUserInput(ctx context.Context, callID string, input any, metadata map[string]any) (userinput.Request, error) {
	metadata["run_id"] = t.input.RunID
	expiresAt := time.Now().Add(userinput.DefaultWaitTimeout + time.Minute)
	flow, err := userinput.RunFlow(ctx, t.userInput, userinput.FlowRequest{
		Input: userinput.CreatePendingInput{
			BotID: t.input.BotID, SessionID: t.input.ThreadID, RouteID: t.input.RouteID,
			ChannelIdentityID: t.input.ChannelIdentityID, RequestedByChannelIdentityID: t.input.ChannelIdentityID,
			ToolCallID: callID, ToolName: userinput.ToolNameAskUser, Input: input, ProviderMetadata: metadata,
			SourcePlatform: t.input.CurrentPlatform, ReplyTarget: t.input.ReplyTarget, ConversationType: t.input.ConversationType, ExpiresAt: &expiresAt,
		},
		ActorChannelIdentityID: t.input.ChannelIdentityID, Interactive: t.input.CanRequestUserInput,
		WaitTimeout: userinput.DefaultWaitTimeout, Emit: t.emitUserInputRequest,
		NonInteractiveReason: "claude requested user input without an interactive stream",
		UndeliveredReason:    "claude user input was not delivered", TimeoutReason: "claude user input timed out", AbortReason: "claude user input aborted",
	})
	if err != nil && ctx.Err() == nil {
		t.logger.ErrorContext(ctx, "claude user input failed", slog.Any("error", err))
	}
	return flow.Request, err
}

func (t *turnRunner) emitUserInputRequest(req userinput.Request) bool {
	t.emit(event.StreamEvent{Type: event.UserInputRequest, ToolCallID: req.ToolCallID, ToolName: req.ToolName, Input: req.Input, UserInputID: req.ID, ShortID: req.ShortID, Status: firstNonEmpty(req.Status, userinput.StatusPending), Metadata: userinput.DeferredMetadata(req)})
	return true
}
