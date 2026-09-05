package application

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

type finishDecisionQueries struct {
	dbstore.Queries
	row sqlc.UserInputRequest
}

func (*finishDecisionQueries) ListPendingToolApprovalsByRun(context.Context, pgtype.UUID) ([]sqlc.ToolApprovalRequest, error) {
	return nil, nil
}

func (q *finishDecisionQueries) ListPendingUserInputsByRun(_ context.Context, runID pgtype.UUID) ([]sqlc.UserInputRequest, error) {
	if q.row.RunID == runID && q.row.Status == userinput.StatusPending {
		return []sqlc.UserInputRequest{q.row}, nil
	}
	return nil, nil
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
			// The in-memory test owner has no ledger token. The finalizer still
			// receives the admitted owner's fence in production; supply it here
			// to verify the application passes it to the existing Cancel path.
			q := &finishDecisionQueries{row: sqlc.UserInputRequest{
				ID:    db.ParseUUIDOrEmpty("44444444-4444-4444-8444-444444444444"),
				BotID: db.ParseUUIDOrEmpty(handle.BotID), SessionID: db.ParseUUIDOrEmpty(handle.SessionID),
				RunID: db.ParseUUIDOrEmpty(handle.RunID), Status: userinput.StatusPending,
				RuntimeFencingToken: pgtype.Int8{Int64: 7, Valid: true},
			}}
			input := &finishingInputService{q: q, wantFence: 7, t: t}
			service := &Service{queries: q, userInput: input, decisionRuntime: manager, abortRuntime: manager, allowedTeam: "team-1"}
			manager.SetDecisionFinalizer(func(ctx context.Context, h sessionruntime.RunHandle) error {
				h.FencingToken = 7
				return service.finalizeRuntimeDecisions(ctx, h)
			})
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
			if err := service.finalizeRuntimeDecisions(context.Background(), handle); err != nil || input.cancelCalls != 1 {
				t.Fatalf("cleanup replay: %v, calls=%d", err, input.cancelCalls)
			}
			// The runtime must release the slot as well as the decision row.
			if _, err := manager.StartRunHandle(context.Background(), handle.BotID, handle.SessionID,
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
