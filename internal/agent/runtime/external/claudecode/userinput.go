package claudecode

import (
	"context"
	"encoding/json"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/external"
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
	request, err := t.requestUserInput(ctx, external.FirstNonEmpty(payload.ToolUseID, requestID), userinput.ChoiceQuestionsInput(input.Questions), map[string]any{"source": userinput.ProviderSourceClaudeUserInput, "request_id": requestID})
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

// Both native questions and MCP elicitation go through the shared question
// flow; only their request/answer mapping differs.
func (t *turnRunner) requestUserInput(ctx context.Context, callID string, input any, metadata map[string]any) (userinput.Request, error) {
	metadata["run_id"] = t.input.RunID
	return external.AskUser(ctx, t.userInput, t.input, external.Question{ToolCallID: callID, Input: input, ProviderMetadata: metadata}, t.emit, t.logger)
}
