package client

import (
	"encoding/json"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/partmeta"
)

func TestTranscriptKeepsSubmittedUserInputAnswers(t *testing.T) {
	t.Parallel()

	recorder := NewTranscriptRecorder()
	recorder.Add(event.StreamEvent{
		Type:       event.ToolCallStart,
		ToolCallID: "call-ask",
		ToolName:   "ask_user",
	})
	recorder.Add(event.StreamEvent{
		Type:        event.UserInputRequest,
		ToolCallID:  "call-ask",
		ToolName:    "ask_user",
		UserInputID: "input-1",
		Status:      userinput.StatusSubmitted,
		Metadata: map[string]any{
			"answers": []userinput.UIAnswer{{
				QuestionID: "q1",
				Question:   "Continue?",
				Selected:   []userinput.UIOption{{ID: "yes", Label: "Yes"}},
			}},
		},
	})

	messages := recorder.Messages("")
	toolCall := messages[0].Content[0].(sdk.ToolCallPart)
	metadata, _ := partmeta.Object(toolCall.ProviderMetadata, partmeta.KeyUserInput)
	var answers []userinput.UIAnswer
	if raw, err := json.Marshal(metadata["answers"]); err != nil || json.Unmarshal(raw, &answers) != nil {
		t.Fatalf("submitted answers did not decode: %#v", metadata["answers"])
	}
	if len(answers) != 1 || answers[0].Selected[0].Label != "Yes" {
		t.Fatalf("submitted answers = %#v", answers)
	}
}
