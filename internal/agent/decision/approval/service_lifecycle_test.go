package approval

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type lifecycleQueries struct {
	dbstore.Queries
	mu         sync.Mutex
	createRow  sqlc.ToolApprovalRequest
	createArg  sqlc.CreateToolApprovalRequestParams
	createErr  error
	getRow     sqlc.ToolApprovalRequest
	cancelArg  sqlc.CancelPendingToolApprovalsBySessionParams
	cancelCall int
}

func (q *lifecycleQueries) CreateToolApprovalRequest(_ context.Context, arg sqlc.CreateToolApprovalRequestParams) (sqlc.ToolApprovalRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.createArg = arg
	if q.createErr != nil {
		return sqlc.ToolApprovalRequest{}, q.createErr
	}
	return q.createRow, nil
}

func TestCreatePendingStoresOperationAndOriginalToolName(t *testing.T) {
	t.Parallel()

	queries := &lifecycleQueries{createRow: sqlc.ToolApprovalRequest{
		ID:         mustTestUUID("33333333-3333-3333-3333-333333333333"),
		BotID:      mustTestUUID("11111111-1111-1111-1111-111111111111"),
		SessionID:  mustTestUUID("22222222-2222-2222-2222-222222222222"),
		ToolCallID: "call-1",
		ToolName:   "apply_patch",
		Operation:  OperationWrite,
		ToolInput:  []byte(`{"patch":"*** Begin Patch\n*** End Patch"}`),
		ShortID:    1,
		Status:     StatusPending,
	}}
	svc := NewService(slog.New(slog.DiscardHandler), queries, nil)

	req, err := svc.CreatePending(context.Background(), CreatePendingInput{
		BotID:      "11111111-1111-1111-1111-111111111111",
		SessionID:  "22222222-2222-2222-2222-222222222222",
		ToolCallID: "call-1",
		ToolName:   "apply_patch",
		ToolInput:  map[string]any{"patch": "*** Begin Patch\n*** End Patch"},
	})
	if err != nil {
		t.Fatalf("CreatePending() error = %v", err)
	}
	if queries.createArg.ToolName != "apply_patch" || queries.createArg.Operation != OperationWrite {
		t.Fatalf("create args tool=%q operation=%q, want apply_patch/write", queries.createArg.ToolName, queries.createArg.Operation)
	}
	if req.ToolName != "apply_patch" || req.Operation != OperationWrite {
		t.Fatalf("request tool=%q operation=%q, want apply_patch/write", req.ToolName, req.Operation)
	}
}

func (q *lifecycleQueries) GetToolApprovalRequest(_ context.Context, _ pgtype.UUID) (sqlc.ToolApprovalRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.getRow, nil
}

func (*lifecycleQueries) ApproveToolApprovalRequest(context.Context, sqlc.ApproveToolApprovalRequestParams) (sqlc.ToolApprovalRequest, error) {
	return sqlc.ToolApprovalRequest{}, pgx.ErrNoRows
}

func (*lifecycleQueries) RejectToolApprovalRequest(context.Context, sqlc.RejectToolApprovalRequestParams) (sqlc.ToolApprovalRequest, error) {
	return sqlc.ToolApprovalRequest{}, pgx.ErrNoRows
}

func (q *lifecycleQueries) CancelPendingToolApprovalsBySession(_ context.Context, arg sqlc.CancelPendingToolApprovalsBySessionParams) ([]sqlc.ToolApprovalRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.cancelArg = arg
	q.cancelCall++
	return []sqlc.ToolApprovalRequest{{
		ID:             mustTestUUID("33333333-3333-3333-3333-333333333333"),
		BotID:          arg.BotID,
		SessionID:      arg.SessionID,
		ToolCallID:     "call-1",
		ToolName:       "exec",
		ToolInput:      []byte(`{"command":"true"}`),
		ShortID:        1,
		Status:         StatusCancelled,
		DecisionReason: arg.Reason,
	}}, nil
}

func TestCancelPendingForSession(t *testing.T) {
	t.Parallel()

	queries := &lifecycleQueries{}
	svc := NewService(slog.New(slog.DiscardHandler), queries, nil)

	botID := "11111111-1111-1111-1111-111111111111"
	sessionID := "22222222-2222-2222-2222-222222222222"
	cancelled, err := svc.CancelPendingForSession(context.Background(), botID, sessionID, "")
	if err != nil {
		t.Fatalf("CancelPendingForSession() error = %v", err)
	}
	if len(cancelled) != 1 || cancelled[0].Status != StatusCancelled {
		t.Fatalf("cancelled = %#v, want one cancelled request", cancelled)
	}
	if queries.cancelCall != 1 {
		t.Fatalf("cancel query calls = %d, want 1", queries.cancelCall)
	}
	if queries.cancelArg.Reason == "" {
		t.Fatal("empty reason was not defaulted")
	}

	if _, err := svc.CancelPendingForSession(context.Background(), botID, "not-a-uuid", "r"); err == nil {
		t.Fatal("CancelPendingForSession accepted a malformed session id")
	}
}

func TestApproveAlreadyDecidedRequestReturnsRaceError(t *testing.T) {
	t.Parallel()

	approvalID := "33333333-3333-3333-3333-333333333333"
	queries := &lifecycleQueries{getRow: sqlc.ToolApprovalRequest{
		ID:         mustTestUUID(approvalID),
		BotID:      mustTestUUID("11111111-1111-1111-1111-111111111111"),
		SessionID:  mustTestUUID("22222222-2222-2222-2222-222222222222"),
		ToolCallID: "call-1",
		ToolName:   "exec",
		ToolInput:  []byte(`{"command":"true"}`),
		ShortID:    1,
		Status:     StatusApproved,
	}}
	svc := NewService(slog.New(slog.DiscardHandler), queries, nil)

	_, err := svc.Approve(context.Background(), approvalID, "", "")
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("Approve() error = %v, want ErrAlreadyDecided", err)
	}
}

func TestCreatePendingRejectsReusedTerminalRequest(t *testing.T) {
	t.Parallel()

	queries := &lifecycleQueries{createRow: sqlc.ToolApprovalRequest{
		ID:         mustTestUUID("33333333-3333-3333-3333-333333333333"),
		BotID:      mustTestUUID("11111111-1111-1111-1111-111111111111"),
		SessionID:  mustTestUUID("22222222-2222-2222-2222-222222222222"),
		ToolCallID: "call-1",
		ToolName:   "exec",
		ToolInput:  []byte(`{"command":"true"}`),
		ShortID:    1,
		Status:     StatusApproved,
	}}
	svc := NewService(slog.New(slog.DiscardHandler), queries, nil)

	_, err := svc.CreatePending(context.Background(), CreatePendingInput{
		BotID:      "11111111-1111-1111-1111-111111111111",
		SessionID:  "22222222-2222-2222-2222-222222222222",
		ToolCallID: "call-1",
		ToolName:   "exec",
		ToolInput:  map[string]any{"command": "true"},
	})
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("CreatePending() error = %v, want ErrAlreadyDecided", err)
	}
}

func TestCanRespondRequiresPendingLiveWaiter(t *testing.T) {
	t.Parallel()

	svc := NewService(slog.New(slog.DiscardHandler), nil, nil)
	req := Request{ID: "approval-1", Status: StatusPending}
	if svc.CanRespond(req) {
		t.Fatal("pending approval without a live waiter should not be answerable")
	}

	release := svc.RegisterWaiter(req.ID)
	if !svc.CanRespond(req) {
		t.Fatal("pending approval with a live waiter should be answerable")
	}
	release()
	if svc.CanRespond(req) {
		t.Fatal("approval should stop being answerable after waiter release")
	}

	release = svc.RegisterWaiter(req.ID)
	defer release()
	req.Status = StatusApproved
	if svc.CanRespond(req) {
		t.Fatal("terminal approval should not be answerable even with a waiter")
	}
}

func TestRejectAlreadyDecidedRequestReturnsRaceError(t *testing.T) {
	t.Parallel()

	approvalID := "33333333-3333-3333-3333-333333333333"
	queries := &lifecycleQueries{getRow: sqlc.ToolApprovalRequest{
		ID:             mustTestUUID(approvalID),
		BotID:          mustTestUUID("11111111-1111-1111-1111-111111111111"),
		SessionID:      mustTestUUID("22222222-2222-2222-2222-222222222222"),
		ToolCallID:     "call-1",
		ToolName:       "exec",
		ToolInput:      []byte(`{"command":"true"}`),
		ShortID:        1,
		Status:         StatusRejected,
		DecisionReason: "already rejected",
	}}
	svc := NewService(slog.New(slog.DiscardHandler), queries, nil)

	_, err := svc.Reject(context.Background(), approvalID, "", "")
	if !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("Reject() error = %v, want ErrAlreadyDecided", err)
	}
}

func mustTestUUID(value string) pgtype.UUID {
	id, err := db.ParseUUID(value)
	if err != nil {
		panic(err)
	}
	return id
}
