package claudecode

import (
	"context"
	"encoding/json"
	"log/slog"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

type elicitationRequest struct {
	Mode            string         `json:"mode"`
	Message         string         `json:"message"`
	ServerName      string         `json:"mcp_server_name"`
	URL             string         `json:"url"`
	ElicitationID   string         `json:"elicitation_id"`
	RequestedSchema map[string]any `json:"requested_schema"`
}

func (t *turnRunner) handleElicitation(ctx context.Context, id string, raw json.RawMessage) {
	var request elicitationRequest
	response := map[string]any{"action": "decline"}
	if json.Unmarshal(raw, &request) == nil {
		response = t.elicit(ctx, id, request)
	}
	if ctx.Err() != nil {
		return
	}
	line, err := controlSuccessResponse(id, response)
	if err == nil {
		_ = t.writeLine(line)
	}
}

func (t *turnRunner) elicit(ctx context.Context, id string, request elicitationRequest) map[string]any {
	decline := map[string]any{"action": "decline"}
	canceled := map[string]any{"action": "cancel"}
	if t.userInput == nil || !t.input.CanRequestUserInput {
		return decline
	}
	var input any
	var mapping userinput.ElicitationFormMapping
	var err error
	switch request.Mode {
	case "", "form":
		input, mapping, err = userinput.ElicitationFormInput(request.Message, request.RequestedSchema)
		if err != nil {
			t.logger.WarnContext(ctx, "claude elicitation form is unsupported", slog.Any("error", err))
			return decline
		}
	case "url":
		input, err = userinput.ElicitationURLInput(request.Message, request.URL)
		if err != nil {
			return decline
		}
	default:
		return decline
	}
	response, err := t.requestUserInput(ctx, "claude-elicitation-"+id, input, map[string]any{
		"source": "claude_mcp_elicitation", "request_id": id, "server_name": request.ServerName, "elicitation_id": request.ElicitationID,
	})
	if err != nil {
		return canceled
	}
	if response.Status != userinput.StatusSubmitted {
		if reason, _ := response.Result["reason"].(string); reason == "user_canceled" {
			return decline
		}
		return canceled
	}
	if request.Mode == "url" {
		answers := userinput.AnswersFromResult(response.Result)
		if len(answers) == 1 && !answers[0].Skipped && len(answers[0].Selected) == 1 && answers[0].Selected[0].ID == "q1.o1" {
			return map[string]any{"action": "accept"}
		}
		return decline
	}
	content, err := mapping.Content(response)
	if err != nil {
		return canceled
	}
	return map[string]any{"action": "accept", "content": content}
}
