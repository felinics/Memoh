package client

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	acp "github.com/coder/acp-go-sdk"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

// answeringUserInput submits every question with the answers it holds.
type answeringUserInput struct {
	answers []any
	created userinput.CreatePendingInput
}

func (f *answeringUserInput) CreatePending(_ context.Context, input userinput.CreatePendingInput) (userinput.Request, error) {
	f.created = input
	return userinput.Request{ID: "input-1", Status: userinput.StatusPending, ToolCallID: input.ToolCallID}, nil
}

func (*answeringUserInput) Cancel(_ context.Context, input userinput.CancelInput) (userinput.Request, error) {
	return userinput.Request{ID: input.RequestID, Status: userinput.StatusCanceled}, nil
}

func (f *answeringUserInput) WaitForRegisteredResponse(context.Context, string) (userinput.Request, error) {
	return userinput.Request{ID: "input-1", Status: userinput.StatusSubmitted, Result: map[string]any{"answers": f.answers}}, nil
}

func (*answeringUserInput) RegisterWaiter(string) func() { return func() {} }

func TestAgentQuestionRequestIsAnsweredThroughMemohsQuestionCard(t *testing.T) {
	t.Parallel()

	userInput := &answeringUserInput{answers: []any{
		map[string]any{"question_id": "q1", "selected": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}},
	}}
	codec := &UserQuestionCodec{
		Method: "_agent/ask",
		Decode: func(json.RawMessage) (string, []userinput.ChoiceQuestion, error) {
			return "call-1", []userinput.ChoiceQuestion{{Text: "Which?", MultiSelect: true, Options: []userinput.ChoiceOption{{Label: "A"}, {Label: "B"}}}}, nil
		},
		Encode: func(_ []userinput.ChoiceQuestion, answers []string) any { return answers },
	}
	callbacks := &clientCallbacks{
		userInput:     userInput,
		userQuestions: codec,
		baseSession:   ToolSessionContext{BotID: "bot-1", SessionID: "session-1", RunID: "run-1", CanRequestUserInput: true},
		events:        &toolEventEmitter{},
	}
	callbacks.setPromptState(newEventCollector(), nil, callbacks.baseSession)
	conn := &clientConnection{sessions: map[acp.SessionId]*clientCallbacks{"s1": callbacks}}

	got, reqErr := conn.handle(context.Background(), "_agent/ask", json.RawMessage(`{"sessionId":"s1"}`))
	if reqErr != nil || !reflect.DeepEqual(got, []string{"A, B"}) {
		t.Fatalf("answer = %#v, %v; want the picks joined", got, reqErr)
	}
	if userInput.created.ToolCallID != "call-1" || userInput.created.SessionID != "session-1" {
		t.Fatalf("question card = %+v", userInput.created)
	}
	if _, reqErr := conn.handle(context.Background(), "_other/ask", json.RawMessage(`{"sessionId":"s1"}`)); reqErr == nil {
		t.Fatal("a method the session has no codec for must stay unknown")
	}
}
