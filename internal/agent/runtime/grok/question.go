package grok

import (
	"encoding/json"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
)

// userQuestions answers Grok's ask_user_question tool, which asks the client
// with its own request rather than ACP elicitation. "cancelled" tells the
// model the user declined to answer and it should go on without one.
var userQuestions = &acpclient.UserQuestionCodec{
	Method: "_x.ai/ask_user_question",
	Decode: func(params json.RawMessage) (string, []userinput.ChoiceQuestion, error) {
		var request struct {
			ToolCallID string                     `json:"toolCallId"`
			Questions  []userinput.ChoiceQuestion `json:"questions"`
		}
		err := json.Unmarshal(params, &request)
		return request.ToolCallID, request.Questions, err
	},
	Encode: func(questions []userinput.ChoiceQuestion, answers []string) any {
		if answers == nil {
			return map[string]any{"outcome": "cancelled"}
		}
		byQuestion := make(map[string]string, len(questions))
		for i, question := range questions {
			byQuestion[question.Text] = answers[i]
		}
		return map[string]any{"outcome": "accepted", "answers": byQuestion}
	},
}
