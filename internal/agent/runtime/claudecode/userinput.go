package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
)

type claudeQuestion struct {
	Question    string `json:"question"`
	MultiSelect bool   `json:"multiSelect"`
	Options     []struct {
		Label       string `json:"label"`
		Description string `json:"description"`
	} `json:"options"`
}

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
		Questions []claudeQuestion `json:"questions"`
	}
	if json.Unmarshal(raw, &input) != nil || len(input.Questions) == 0 || len(input.Questions) > userinput.MaxQuestionsPerRequest {
		return nil, false
	}
	questions := make([]any, 0, len(input.Questions))
	for _, question := range input.Questions {
		questions = append(questions, claudeQuestionPayload(question))
	}
	request, err := t.requestUserInput(ctx, firstNonEmpty(payload.ToolUseID, requestID), map[string]any{"questions": questions}, map[string]any{"source": "claude_request_user_input", "request_id": requestID})
	if err != nil || request.Status != userinput.StatusSubmitted {
		return nil, false
	}
	byID := map[string]userinput.UIAnswer{}
	for _, answer := range userinput.AnswersFromResult(request.Result) {
		byID[answer.QuestionID] = answer
	}
	answers := make(map[string]string, len(input.Questions))
	for i, question := range input.Questions {
		answer, ok := byID[fmt.Sprintf("q%d", i+1)]
		if !ok || answer.Skipped {
			return nil, false
		}
		var values []string
		for _, selected := range answer.Selected {
			values = append(values, selected.Label)
		}
		if text := strings.TrimSpace(answer.CustomText); text != "" {
			values = append(values, text)
		}
		if text := strings.TrimSpace(answer.Text); text != "" {
			values = append(values, text)
		}
		if len(values) == 0 {
			return nil, false
		}
		answers[question.Question] = strings.Join(values, ", ")
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

func claudeQuestionPayload(question claudeQuestion) map[string]any {
	out := map[string]any{"text": question.Question, "kind": userinput.QuestionKindText}
	if len(question.Options) < userinput.MinOptionsPerQuestion {
		return out
	}
	options := make([]any, 0, len(question.Options))
	for _, option := range question.Options[:min(len(question.Options), userinput.MaxOptionsPerQuestion)] {
		options = append(options, map[string]any{"label": option.Label, "description": option.Description})
	}
	out["options"] = options
	out["allow_custom"] = true
	out["kind"] = userinput.QuestionKindSingleSelect
	if question.MultiSelect {
		out["kind"] = userinput.QuestionKindMultiSelect
	}
	return out
}

func (t *turnRunner) emitUserInputRequest(req userinput.Request) bool {
	t.emit(event.StreamEvent{Type: event.UserInputRequest, ToolCallID: req.ToolCallID, ToolName: req.ToolName, Input: req.Input, UserInputID: req.ID, ShortID: req.ShortID, Status: firstNonEmpty(req.Status, userinput.StatusPending), Metadata: userinput.DeferredMetadata(req)})
	return true
}
