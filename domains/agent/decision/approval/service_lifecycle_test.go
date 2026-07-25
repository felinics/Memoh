package approval

import (
	"context"
	"errors"
	"log/slog"
	"testing"
)

const (
	testBotID      = "11111111-1111-1111-1111-111111111111"
	testSessionID  = "22222222-2222-2222-2222-222222222222"
	testApprovalID = "33333333-3333-3333-3333-333333333333"
)

type testPersistence struct {
	createInput CreateRecordInput
	createRow   Record
	getRow      Record
	cancelInput CancelSessionInput
	cancelRows  []Record
	decisionErr error
	fenceErr    error
}

func (*testPersistence) ChannelIdentityExists(context.Context, string) (bool, error) {
	return false, nil
}

func (p *testPersistence) Create(_ context.Context, in CreateRecordInput) (Record, error) {
	p.createInput = in
	return p.createRow, nil
}
func (p *testPersistence) Get(context.Context, string) (Record, error) { return p.getRow, nil }
func (*testPersistence) GetPendingBySessionShortID(context.Context, string, string, int) (Record, error) {
	return Record{}, ErrNotFound
}

func (*testPersistence) GetPendingByReplyMessage(context.Context, string, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (*testPersistence) GetLatestPendingBySession(context.Context, string, string) (Record, error) {
	return Record{}, ErrNotFound
}

func (p *testPersistence) Approve(context.Context, DecisionRecordInput) (Record, error) {
	return Record{}, p.decisionErr
}

func (p *testPersistence) Reject(context.Context, DecisionRecordInput) (Record, error) {
	return Record{}, p.decisionErr
}

func (p *testPersistence) CancelPendingBySession(_ context.Context, in CancelSessionInput) ([]Record, error) {
	p.cancelInput = in
	return p.cancelRows, nil
}

func (*testPersistence) UpdatePrompt(context.Context, UpdatePromptInput) (Record, error) {
	return Record{}, ErrNotFound
}

func (*testPersistence) ListPendingBySession(context.Context, string, string) ([]Record, error) {
	return nil, nil
}

func (*testPersistence) ListBySession(context.Context, string, string) ([]Record, error) {
	return nil, nil
}

func (*testPersistence) ListBySessionToolCalls(context.Context, string, string, []string) ([]Record, error) {
	return nil, nil
}

func (p *testPersistence) InApprovalCreateTransaction(_ context.Context, _, _ string, fn func(Store) error) error {
	return fn(p)
}

func (p *testPersistence) InApprovalFenceTransaction(_ context.Context, _, _ string, fn func(Store) error) error {
	if p.fenceErr != nil {
		return p.fenceErr
	}
	return fn(p)
}

func TestCreatePendingStoresOperationAndOriginalToolName(t *testing.T) {
	persistence := &testPersistence{createRow: Record{
		ID: testApprovalID, BotID: testBotID, SessionID: testSessionID,
		ToolCallID: "call-1", ToolName: "apply_patch", Operation: OperationWrite,
		ToolInput: []byte(`{"patch":"*** Begin Patch\n*** End Patch"}`), ShortID: 1, Status: StatusPending,
	}}
	svc := NewService(slog.New(slog.DiscardHandler), persistence, nil)
	req, err := svc.CreatePending(t.Context(), CreatePendingInput{
		BotID: testBotID, SessionID: testSessionID, ToolCallID: "call-1",
		ToolName: "apply_patch", ToolInput: map[string]any{"patch": "*** Begin Patch\n*** End Patch"},
	})
	if err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if persistence.createInput.ToolName != "apply_patch" || persistence.createInput.Operation != OperationWrite {
		t.Fatalf("create input = %#v", persistence.createInput)
	}
	if req.ToolName != "apply_patch" || req.Operation != OperationWrite {
		t.Fatalf("request = %#v", req)
	}
}

func TestCancelPendingForSession(t *testing.T) {
	persistence := &testPersistence{cancelRows: []Record{{
		ID: testApprovalID, BotID: testBotID, SessionID: testSessionID, Status: StatusCancelled,
	}}}
	svc := NewService(slog.New(slog.DiscardHandler), persistence, nil)
	cancelled, err := svc.CancelPendingForSession(t.Context(), testBotID, testSessionID, "")
	if err != nil {
		t.Fatalf("CancelPendingForSession() error = %v", err)
	}
	if len(cancelled) != 1 || cancelled[0].Status != StatusCancelled {
		t.Fatalf("cancelled = %#v", cancelled)
	}
	if persistence.cancelInput.Reason == "" {
		t.Fatal("empty reason was not defaulted")
	}
	if _, err := svc.CancelPendingForSession(t.Context(), testBotID, "not-a-uuid", "r"); err == nil {
		t.Fatal("malformed session id accepted")
	}
}

func TestDecisionAlreadyResolvedReturnsRaceError(t *testing.T) {
	for _, status := range []string{StatusApproved, StatusRejected} {
		t.Run(status, func(t *testing.T) {
			persistence := &testPersistence{
				getRow:      Record{ID: testApprovalID, BotID: testBotID, SessionID: testSessionID, Status: status},
				decisionErr: ErrNotFound,
			}
			svc := NewService(nil, persistence, nil)
			var err error
			if status == StatusApproved {
				_, err = svc.Approve(t.Context(), testApprovalID, "", "")
			} else {
				_, err = svc.Reject(t.Context(), testApprovalID, "", "")
			}
			if !errors.Is(err, ErrAlreadyDecided) {
				t.Fatalf("decision error = %v", err)
			}
		})
	}
}

func TestCreatePendingRejectsReusedTerminalRequest(t *testing.T) {
	persistence := &testPersistence{createRow: Record{
		ID: testApprovalID, BotID: testBotID, SessionID: testSessionID,
		ToolCallID: "call-1", ToolName: "exec", Status: StatusApproved,
	}}
	svc := NewService(nil, persistence, nil)
	_, err := svc.CreatePending(t.Context(), CreatePendingInput{
		BotID: testBotID, SessionID: testSessionID, ToolCallID: "call-1",
		ToolName: "exec", ToolInput: map[string]any{"command": "true"},
	})
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("CreatePending() error = %v", err)
	}
}

func TestCanRespondRequiresPendingLiveWaiter(t *testing.T) {
	svc := NewService(nil, nil, nil)
	req := Request{ID: "approval-1", Status: StatusPending}
	if svc.CanRespond(req) {
		t.Fatal("approval without waiter is answerable")
	}
	release := svc.RegisterWaiter(req.ID)
	if !svc.CanRespond(req) {
		t.Fatal("approval with waiter is not answerable")
	}
	release()
	if svc.CanRespond(req) {
		t.Fatal("approval remained answerable")
	}
}
