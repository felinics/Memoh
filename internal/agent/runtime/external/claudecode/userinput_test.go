package claudecode

import (
	"context"
	"testing"
	"time"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

type testUserInputService struct {
	pending  userinput.CreatePendingInput
	answer   userinput.Request
	waiting  chan struct{}
	canceled chan struct{}
}

func (s *testUserInputService) CreatePending(_ context.Context, input userinput.CreatePendingInput) (userinput.Request, error) {
	if err := userinput.ValidateAskUserInput(input.Input); err != nil {
		return userinput.Request{}, err
	}
	s.pending = input
	return userinput.Request{ID: "elicitation", ToolCallID: input.ToolCallID, ToolName: input.ToolName, Status: userinput.StatusPending}, nil
}
func (*testUserInputService) RegisterWaiter(string) func() { return func() {} }
func (s *testUserInputService) WaitForRegisteredResponse(ctx context.Context, _ string) (userinput.Request, error) {
	if s.waiting != nil {
		close(s.waiting)
		<-ctx.Done()
		return userinput.Request{}, ctx.Err()
	}
	return s.answer, nil
}

func (s *testUserInputService) Cancel(context.Context, userinput.CancelInput) (userinput.Request, error) {
	if s.canceled != nil {
		close(s.canceled)
	}
	return userinput.Request{Status: userinput.StatusCanceled}, nil
}

func TestClaudeQuestionsReturnAnswersAndCancelWithoutUser(t *testing.T) {
	r := newTestRunner(&recordingSink{})
	defer r.close()
	svc := &testUserInputService{
		canceled: make(chan struct{}),
		answer: userinput.Request{Status: userinput.StatusSubmitted, Result: map[string]any{"answers": []any{
			map[string]any{"question_id": "q1", "selected": []any{map[string]any{"id": "a", "label": "A"}, map[string]any{"id": "b", "label": "B"}}},
			map[string]any{"question_id": "q2", "text": "custom answer"},
		}}},
	}
	r.userInput = svc
	r.input.CanRequestUserInput = true
	payload := &controlRequestPayload{ToolName: "AskUserQuestion", ToolUseID: "ask", Input: map[string]any{"questions": []any{
		map[string]any{"question": "Choose", "multiSelect": true, "options": []any{map[string]any{"label": "A", "description": "first"}, map[string]any{"label": "B", "description": "second"}}},
		map[string]any{"question": "Explain"},
	}}}
	updated, ok := r.collectAnswers(context.Background(), "req", payload)
	if !ok {
		t.Fatal("submitted questions were rejected")
	}
	answers := updated["answers"].(map[string]string)
	if answers["Choose"] != "A, B" || answers["Explain"] != "custom answer" {
		t.Fatalf("answers: %v", answers)
	}
	if _, exists := payload.Input["answers"]; exists {
		t.Fatal("mutated the runtime input")
	}
	r.input.CanRequestUserInput = false
	if _, ok := r.collectAnswers(context.Background(), "req2", payload); ok {
		t.Fatal("non-interactive question was accepted")
	}
	select {
	case <-svc.canceled:
	default:
		t.Fatal("non-interactive question did not cancel")
	}
}

func TestClaudeElicitationFormAndURL(t *testing.T) {
	for _, mode := range []string{"form", "url"} {
		t.Run(mode, func(t *testing.T) {
			r := newTestRunner(&recordingSink{})
			defer r.close()
			r.input.CanRequestUserInput = true
			r.input.ThreadID, r.input.RunID = "thread", "run"
			r.input.CurrentPlatform, r.input.ReplyTarget = "web", "target"
			svc := &testUserInputService{answer: userinput.Request{Status: userinput.StatusSubmitted, Result: map[string]any{"answers": []any{map[string]any{
				"question_id": "q1", "text": "answer", "selected": []any{map[string]any{"id": "q1.o1", "label": "已完成"}},
			}}}}}
			r.userInput = svc
			request := elicitationRequest{Mode: mode, Message: "Enter a value", URL: "https://example.com/confirm", RequestedSchema: map[string]any{
				"type": "object", "properties": map[string]any{"value": map[string]any{"type": "string"}}, "required": []any{"value"},
			}}
			response := r.elicit(context.Background(), "request", request)
			if response["action"] != "accept" {
				t.Fatalf("response: %v", response)
			}
			if mode == "form" && response["content"].(map[string]any)["value"] != "answer" {
				t.Fatalf("content: %v", response)
			}
			if mode == "url" && response["content"] != nil {
				t.Fatalf("URL returned form content: %v", response)
			}
			if svc.pending.SessionID != "thread" || svc.pending.ProviderMetadata["run_id"] != "run" || svc.pending.ReplyTarget != "target" {
				t.Fatalf("ownership: %+v", svc.pending)
			}
		})
	}
}

func TestClaudeElicitationCancellation(t *testing.T) {
	r, p := controlTestRunner()
	defer r.close()
	r.input.CanRequestUserInput = true
	svc := &testUserInputService{waiting: make(chan struct{}), canceled: make(chan struct{})}
	r.userInput = svc
	feed(t, r, `{"type":"control_request","request_id":"request","request":{"subtype":"elicitation","mode":"url","url":"https://example.com"}}`)
	select {
	case <-svc.waiting:
	case <-time.After(time.Second):
		t.Fatal("elicitation was not routed to the user")
	}
	feed(t, r, `{"type":"control_cancel_request","request_id":"request"}`)
	select {
	case <-svc.canceled:
	case <-time.After(time.Second):
		t.Fatal("elicitation was not canceled")
	}
	select {
	case line := <-p.writes:
		t.Fatalf("answered a canceled native request: %s", line)
	default:
	}
}
