package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
	sessiontest "github.com/felinics/memoh/internal/testutil/sessionruntime"
)

type finishDecisionQueries struct {
	dbstore.Queries
	row      sqlc.UserInputRequest
	approval sqlc.ToolApprovalRequest
}

func (q *finishDecisionQueries) ListToolApprovalsByRun(_ context.Context, runID pgtype.UUID) ([]sqlc.ToolApprovalRequest, error) {
	if q.approval.RunID == runID {
		return []sqlc.ToolApprovalRequest{q.approval}, nil
	}
	return nil, nil
}

func (q *finishDecisionQueries) ListUserInputsByRun(_ context.Context, runID pgtype.UUID) ([]sqlc.UserInputRequest, error) {
	if q.row.RunID == runID {
		return []sqlc.UserInputRequest{q.row}, nil
	}
	return nil, nil
}

func (f *finishingInputService) Get(context.Context, string) (userinput.Request, error) {
	return userinput.Request{ID: f.q.row.ID.String(), Status: f.q.row.Status}, nil
}

type finishingInputService struct {
	fakeUserInputService
	q         *finishDecisionQueries
	wantFence int64
	t         *testing.T
}

func (f *finishingInputService) Cancel(ctx context.Context, input userinput.CancelInput) (userinput.Request, error) {
	fence, ok := runtimefence.FromContext(ctx)
	if !ok || fence.Token != f.wantFence || fence.BotID != lifecycleTestBotID || fence.SessionID != lifecycleTestSessionID {
		f.t.Fatalf("cancel received wrong fence: %#v", fence)
	}
	f.cancelCalls++
	f.q.row.Status = userinput.StatusCanceled
	return userinput.Request{ID: input.RequestID, Status: userinput.StatusCanceled}, nil
}

func TestWebAndChannelStopCloseParkedInputWithoutContinuing(t *testing.T) {
	for _, surface := range []string{"web", "channel"} {
		t.Run(surface, func(t *testing.T) {
			manager, handle := newWaitingDecisionRuntime(t)
			q := &finishDecisionQueries{row: sqlc.UserInputRequest{
				ID:    db.ParseUUIDOrEmpty("44444444-4444-4444-8444-444444444444"),
				BotID: db.ParseUUIDOrEmpty(handle.BotID), SessionID: db.ParseUUIDOrEmpty(handle.SessionID),
				RunID: db.ParseUUIDOrEmpty(handle.RunID), Status: userinput.StatusPending,
				RuntimeFencingToken: pgtype.Int8{Int64: handle.FencingToken, Valid: true},
			}}
			input := &finishingInputService{q: q, wantFence: handle.FencingToken, t: t}
			service := &Service{queries: q, userInput: input, decisionRuntime: manager, abortRuntime: manager, allowedTeam: "team-1"}
			manager.SetDecisionFinalizer(service.finalizeRuntimeDecisions)
			manager.SetCommandHandler(func(context.Context, sessionruntime.Command) error {
				t.Error("stop must not resume the model")
				return nil
			})
			var stopped bool
			var err error
			if surface == "web" {
				stopped, err = service.AbortRuntimeRun(context.Background(), handle.BotID, handle.SessionID, handle.RunID, "web-stop")
			} else {
				stopped, err = service.StopTurn(context.Background(), turn.StopCommand{TeamID: "team-1", BotID: handle.BotID, ThreadID: handle.SessionID})
			}
			if err != nil || !stopped || input.cancelCalls != 1 || q.row.Status != userinput.StatusCanceled {
				t.Fatalf("stop = %v, %v; cancel calls=%d status=%s", stopped, err, input.cancelCalls, q.row.Status)
			}
			// After ownership is released, replay may not publish through the old handle.
			if err := service.finalizeRuntimeDecisions(context.Background(), handle); !errors.Is(err, sessionruntime.ErrRunOwnershipLost) || input.cancelCalls != 1 {
				t.Fatalf("cleanup replay: %v, calls=%d", err, input.cancelCalls)
			}
			// The runtime must release the slot as well as the decision row.
			if _, err := sessiontest.Start(context.Background(), manager, handle.BotID, handle.SessionID,
				"55555555-5555-4555-8555-555555555555", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
				t.Fatalf("next run blocked: %v", err)
			}
		})
	}
}

func TestRunDecisionCleanupRejectsSuccessorFence(t *testing.T) {
	q := &finishDecisionQueries{row: sqlc.UserInputRequest{
		ID:    db.ParseUUIDOrEmpty("44444444-4444-4444-8444-444444444444"),
		BotID: db.ParseUUIDOrEmpty(lifecycleTestBotID), SessionID: db.ParseUUIDOrEmpty(lifecycleTestSessionID),
		RunID: db.ParseUUIDOrEmpty(lifecycleTestRunID), Status: userinput.StatusPending,
		RuntimeFencingToken: pgtype.Int8{Int64: 8, Valid: true},
	}}
	input := &fakeUserInputService{}
	s := &Service{queries: q, userInput: input}
	err := s.finalizeRuntimeDecisions(context.Background(), sessionruntime.RunHandle{
		BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, RunID: lifecycleTestRunID, FencingToken: 7,
	})
	if !errors.Is(err, sessionruntime.ErrRunOwnershipLost) || input.cancelCalls != 0 {
		t.Fatalf("successor decision changed: %v, calls=%d", err, input.cancelCalls)
	}
}

func TestParkDoesNotFinalizeDecisions(t *testing.T) {
	manager, handle := newWaitingDecisionRuntime(t)
	manager.SetDecisionFinalizer(func(context.Context, sessionruntime.RunHandle) error {
		t.Error("a parked run must keep its question")
		return nil
	})
	if _, err := manager.HandleAgentEvent(context.Background(), handle, native.StreamEvent{
		Type: native.EventUserInputRequest, UserInputID: "another-pending", Status: "pending",
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.FinishRun(context.Background(), handle, "", ""); err != nil {
		t.Fatal(err)
	}
}

func TestDecisionCleanupFailureKeepsRunUntilRetry(t *testing.T) {
	manager, handle := newWaitingDecisionRuntime(t)
	wantErr := errors.New("decision database unavailable")
	var calls atomic.Int32
	release := make(chan struct{})
	manager.SetDecisionFinalizer(func(ctx context.Context, _ sessionruntime.RunHandle) error {
		if calls.Add(1) == 1 {
			return wantErr
		}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	if stopped, err := manager.Abort(context.Background(), handle.BotID, handle.SessionID, handle.RunID); stopped || !errors.Is(err, wantErr) {
		t.Fatalf("cleanup failure reported success: %v, %v", stopped, err)
	}
	snapshot, err := manager.Snapshot(context.Background(), handle.BotID, handle.SessionID)
	if err != nil || snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status != sessionruntime.RunStatusAborting {
		t.Fatalf("run released before cleanup: %#v, %v", snapshot, err)
	}
	close(release)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snapshot, err = manager.Snapshot(context.Background(), handle.BotID, handle.SessionID)
		if err == nil && (snapshot.CurrentRunView == nil || snapshot.CurrentRunView.Status == sessionruntime.RunStatusAborted) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("retry did not finish the run after cleanup recovered")
}

func (q *finishDecisionQueries) GetToolApprovalRequest(context.Context, pgtype.UUID) (sqlc.ToolApprovalRequest, error) {
	return q.approval, nil
}

func TestInlineFinishReconcilesClosedDecisionsAndRetriesPublication(t *testing.T) {
	for _, failPublish := range []bool{false, true} {
		name := "delivered"
		if failPublish {
			name = "retry_publication"
		}
		t.Run(name, func(t *testing.T) {
			backend := &failNextRuntimeDecisionBackend{MemoryBackend: sessionruntime.NewMemoryBackend(), err: errors.New("projection unavailable")}
			manager, handle := newWaitingDecisionRuntime(t, backend)
			manager.MarkInlineDecisionRun(handle.BotID, handle.SessionID, handle.RunID)
			q := &finishDecisionQueries{row: sqlc.UserInputRequest{
				ID:    db.ParseUUIDOrEmpty("44444444-4444-4444-8444-444444444444"),
				BotID: db.ParseUUIDOrEmpty(handle.BotID), SessionID: db.ParseUUIDOrEmpty(handle.SessionID), RunID: db.ParseUUIDOrEmpty(handle.RunID),
				Status: userinput.StatusSubmitted, RuntimeFencingToken: pgtype.Int8{Int64: handle.FencingToken, Valid: true},
			}, approval: sqlc.ToolApprovalRequest{
				ID:    db.ParseUUIDOrEmpty("66666666-6666-4666-8666-666666666666"),
				BotID: db.ParseUUIDOrEmpty(handle.BotID), SessionID: db.ParseUUIDOrEmpty(handle.SessionID), RunID: db.ParseUUIDOrEmpty(handle.RunID),
				Status: toolapproval.StatusRejected, RuntimeFencingToken: pgtype.Int8{Int64: handle.FencingToken, Valid: true},
				ToolCallID: "approval-call", ToolName: "exec", ToolInput: []byte(`{"command":"pwd"}`),
			}}
			input := &fakeUserInputService{target: userinput.Request{ID: q.row.ID.String(), Status: userinput.StatusSubmitted, ToolCallID: "input-call", ToolName: "ask_user", Result: map[string]any{"answers": []any{map[string]any{"question_id": "q1", "text": "chosen answer"}}}}}
			service := &Service{queries: q, userInput: input, toolApproval: toolapproval.NewService(nil, q, nil), decisionRuntime: manager}
			if _, err := manager.HandleAgentEvent(t.Context(), handle, native.StreamEvent{Type: native.EventToolApprovalRequest, ApprovalID: q.approval.ID.String(), ToolCallID: "approval-call", ToolName: "exec", Status: "pending"}); err != nil {
				t.Fatal(err)
			}
			// Close dropped both decision notifications; the application still emits
			// its failure and abort before calling FinishRun with an empty outcome.
			for _, ev := range []native.StreamEvent{runtimeFailureEvent(apperror.Wrap(apperror.CodeAgentResponseTimeout, errors.New("SECRET"), nil)), {Type: native.EventAbort}} {
				if _, err := manager.HandleAgentEvent(t.Context(), handle, ev); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := manager.Snapshot(t.Context(), handle.BotID, handle.SessionID)
			if before.CurrentRunView.Status != sessionruntime.RunStatusFinishing {
				t.Fatalf("not finishing: %+v", before.CurrentRunView)
			}

			// Once finishing, neither ordinary output nor a new approval may reopen
			// the run. Only authoritative terminal decision snapshots are admitted.
			if _, err := manager.HandleAgentEvent(t.Context(), handle, native.StreamEvent{Type: native.EventTextDelta, Delta: "late output"}); err != nil {
				t.Fatal(err)
			}
			if _, err := manager.HandleAgentEvent(t.Context(), handle, native.StreamEvent{Type: native.EventToolApprovalRequest, ApprovalID: "late-pending", Status: "pending"}); !errors.Is(err, sessionruntime.ErrRunOwnershipLost) {
				t.Fatalf("late pending accepted: %v", err)
			}
			after, _ := manager.Snapshot(t.Context(), handle.BotID, handle.SessionID)
			if after.Seq != before.Seq || after.CurrentRunView.Status != sessionruntime.RunStatusFinishing {
				t.Fatal("late events mutated finishing run")
			}
			var calls atomic.Int32
			manager.SetDecisionFinalizer(func(ctx context.Context, h sessionruntime.RunHandle) error {
				if calls.Add(1) == 1 && failPublish {
					backend.failNext.Store(true)
				}
				return service.finalizeRuntimeDecisions(ctx, h)
			})
			err := manager.FinishRun(t.Context(), handle, "", "")
			if failPublish && !errors.Is(err, backend.err) {
				t.Fatalf("publication failure not propagated: %v", err)
			}
			if !failPublish && err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				snap, err := manager.Snapshot(t.Context(), handle.BotID, handle.SessionID)
				if err != nil {
					t.Fatal(err)
				}
				if snap.CurrentRunView != nil && snap.CurrentRunView.Status == sessionruntime.RunStatusErrored {
					if snap.CurrentRunView.ErrorCode != string(apperror.CodeAgentResponseTimeout) {
						t.Fatalf("lost failure code: %+v", snap.CurrentRunView)
					}
					approvalSeen, inputSeen := false, false
					for _, msg := range snap.CurrentRunView.Messages {
						if msg.Approval != nil && msg.Approval.ApprovalID == q.approval.ID.String() {
							approvalSeen = msg.Approval.Status == toolapproval.StatusRejected && !msg.Approval.CanApprove
						}
						if msg.UserInput != nil && msg.UserInput.UserInputID == q.row.ID.String() {
							inputSeen = msg.UserInput.Status == userinput.StatusSubmitted && !msg.UserInput.CanRespond && len(msg.UserInput.Answers) == 1
						}
					}
					if !approvalSeen || !inputSeen {
						t.Fatalf("terminal decision metadata missing: %+v", snap.CurrentRunView.Messages)
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatalf("finish retry did not converge: %+v", snap.CurrentRunView)
				}
				time.Sleep(10 * time.Millisecond)
			}
			if input.cancelCalls != 0 {
				t.Fatal("already submitted answer was cancelled")
			}
			if failPublish && calls.Load() < 2 {
				t.Fatal("no finish retry")
			}
			if _, err := sessiontest.Start(t.Context(), manager, handle.BotID, handle.SessionID, "77777777-7777-4777-8777-777777777777", make(chan struct{}, 1), func() {}, make(chan turn.InjectMessage, 1)); err != nil {
				t.Fatalf("next run blocked: %v", err)
			}
		})
	}
}

func TestRunDecisionFinishFenceRules(t *testing.T) {
	for _, tt := range []struct {
		name, status string
		fence        int64
		wantLost     bool
	}{
		{"old_terminal", userinput.StatusSubmitted, 6, false},
		{"successor_terminal", userinput.StatusSubmitted, 8, true},
		{"old_pending", userinput.StatusPending, 6, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			q := &finishDecisionQueries{row: sqlc.UserInputRequest{ID: db.ParseUUIDOrEmpty("44444444-4444-4444-8444-444444444444"), BotID: db.ParseUUIDOrEmpty(lifecycleTestBotID), SessionID: db.ParseUUIDOrEmpty(lifecycleTestSessionID), RunID: db.ParseUUIDOrEmpty(lifecycleTestRunID), Status: tt.status, RuntimeFencingToken: pgtype.Int8{Int64: tt.fence, Valid: true}}}
			input := &fakeUserInputService{target: userinput.Request{ID: q.row.ID.String(), Status: tt.status}}
			service := &Service{queries: q, userInput: input}
			err := service.finalizeRuntimeDecisions(t.Context(), sessionruntime.RunHandle{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, RunID: lifecycleTestRunID, FencingToken: 7})
			if errors.Is(err, sessionruntime.ErrRunOwnershipLost) != tt.wantLost || (err != nil && !tt.wantLost) || input.cancelCalls != 0 {
				t.Fatalf("fence result: %v, mutations=%d", err, input.cancelCalls)
			}
		})
	}
}

func TestRuntimeFailureEventKeepsStableCodePrivateCause(t *testing.T) {
	for _, cause := range []error{errors.New("SECRET"), apperror.Wrap(apperror.CodeAgentResponseTimeout, errors.New("SECRET"), nil)} {
		ev := runtimeFailureEvent(cause)
		raw, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if ev.Code == "" || ev.Code != ev.Error || strings.Contains(string(raw), "SECRET") {
			t.Fatalf("invalid public failure: %s", raw)
		}
	}
}

func TestFinishRereadsConcurrentUserDecision(t *testing.T) {
	q := &finishDecisionQueries{row: sqlc.UserInputRequest{ID: db.ParseUUIDOrEmpty("44444444-4444-4444-8444-444444444444"), BotID: db.ParseUUIDOrEmpty(lifecycleTestBotID), SessionID: db.ParseUUIDOrEmpty(lifecycleTestSessionID), RunID: db.ParseUUIDOrEmpty(lifecycleTestRunID), Status: userinput.StatusPending, RuntimeFencingToken: pgtype.Int8{Int64: 7, Valid: true}}}
	input := &fakeUserInputService{cancelErr: userinput.ErrAlreadyDecided, target: userinput.Request{ID: q.row.ID.String(), Status: userinput.StatusSubmitted}}
	service := &Service{queries: q, userInput: input}
	if err := service.finalizeRuntimeDecisions(t.Context(), sessionruntime.RunHandle{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, RunID: lifecycleTestRunID, FencingToken: 7}); err != nil {
		t.Fatal(err)
	}
	if input.cancelCalls != 1 {
		t.Fatal("pending row was not resolved")
	}
	input.target.Status = userinput.StatusPending
	if err := service.finalizeRuntimeDecisions(t.Context(), sessionruntime.RunHandle{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, RunID: lifecycleTestRunID, FencingToken: 7}); err == nil {
		t.Fatal("unresolved decision allowed finish")
	}
}
