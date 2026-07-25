package input

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

type fakeFlowService struct {
	created     Request
	createErr   error
	waitResult  Request
	waitErr     error
	waitBlocks  bool
	cancelCalls []string
	waitCalls   int
	waiters     int
}

func (f *fakeFlowService) CreatePending(context.Context, CreatePendingInput) (Request, error) {
	if f.createErr != nil {
		return Request{}, f.createErr
	}
	req := f.created
	if req.ID == "" {
		req.ID = "input-1"
	}
	if req.ToolName == "" {
		req.ToolName = ToolNameAskUser
	}
	if req.Status == "" {
		req.Status = StatusPending
	}
	return req, nil
}

func (f *fakeFlowService) Cancel(_ context.Context, input CancelInput) (Request, error) {
	f.cancelCalls = append(f.cancelCalls, input.RequestID+":"+input.Reason)
	return Request{
		ID:     input.RequestID,
		Status: StatusCanceled,
		Result: map[string]any{"status": StatusCanceled, "reason": input.Reason},
	}, nil
}

func (f *fakeFlowService) WaitForRegisteredResponse(ctx context.Context, _ string) (Request, error) {
	f.waitCalls++
	if f.waitBlocks {
		<-ctx.Done()
		return Request{}, ctx.Err()
	}
	if f.waitErr != nil {
		return Request{}, f.waitErr
	}
	if f.waitResult.ID == "" {
		return Request{ID: "input-1", Status: StatusSubmitted, Result: map[string]any{"ok": true}}, nil
	}
	return f.waitResult, nil
}

func (f *fakeFlowService) RegisterWaiter(string) func() {
	f.waiters++
	return func() {
		f.waiters--
	}
}

func testFlowRequest() FlowRequest {
	return FlowRequest{
		Input: CreatePendingInput{
			BotID:      "bot-1",
			SessionID:  "session-1",
			ToolCallID: "ask-1",
			ToolName:   ToolNameAskUser,
			Input: map[string]any{
				"questions": []any{
					map[string]any{
						"text": "Choose",
						"kind": QuestionKindSingleSelect,
						"options": []any{
							map[string]any{"label": "A"},
							map[string]any{"label": "B"},
						},
					},
				},
			},
		},
		Interactive: true,
	}
}

func TestRunFlowRejectsReusedTerminalRequest(t *testing.T) {
	t.Parallel()

	svc := &fakeFlowService{created: Request{ID: "input-1", Status: StatusSubmitted}}
	_, err := RunFlow(context.Background(), svc, testFlowRequest())
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("RunFlow() error = %v, want ErrAlreadyDecided", err)
	}
	if svc.waitCalls != 0 || len(svc.cancelCalls) != 0 {
		t.Fatalf("flow used terminal request: waitCalls=%d cancelCalls=%v", svc.waitCalls, svc.cancelCalls)
	}
}

func TestRunFlowNonInteractiveCancels(t *testing.T) {
	t.Parallel()

	svc := &fakeFlowService{}
	flow := testFlowRequest()
	flow.Interactive = false
	flow.NonInteractiveReason = "no stream"
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil || result.Request.Status != StatusCanceled {
		t.Fatalf("RunFlow() = %+v, %v; want canceled", result, err)
	}
	if len(svc.cancelCalls) != 1 || !strings.Contains(svc.cancelCalls[0], "no stream") {
		t.Fatalf("cancelCalls = %+v, want one non-interactive cancel", svc.cancelCalls)
	}
}

func TestRunFlowTimeoutCancels(t *testing.T) {
	t.Parallel()

	svc := &fakeFlowService{waitBlocks: true}
	flow := testFlowRequest()
	flow.WaitTimeout = 30 * time.Millisecond
	result, err := RunFlow(context.Background(), svc, flow)
	if err != nil || result.Request.Status != StatusCanceled {
		t.Fatalf("RunFlow() = %+v, %v; want timeout cancel", result, err)
	}
	if len(svc.cancelCalls) != 1 || !strings.Contains(svc.cancelCalls[0], "timed out") {
		t.Fatalf("cancelCalls = %+v, want one timeout cancel", svc.cancelCalls)
	}
}
