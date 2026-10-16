package application

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

// Exercise real PostgreSQL decisions, Redis expiry, a fresh Manager's reaper,
// and the application's response commit path. No production provider is used.
//
//nolint:contextcheck // Continuation contexts are intentionally owned by the recovered run, not the command deadline.
func TestPostgresRedisDecisionSurvivesGracefulShutdown(t *testing.T) {
	redisURL := os.Getenv("MEMOH_TEST_REDIS_URL")
	if redisURL == "" {
		if os.Getenv("MEMOH_TEST_DISTRIBUTED_REQUIRED") == "1" {
			t.Fatal("MEMOH_TEST_REDIS_URL required")
		}
		t.Skip("set MEMOH_TEST_REDIS_URL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool := openTurnAdmissionPostgres(t, ctx)
	botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
	q := dbsqlc.New(pool)
	store := postgresstore.NewQueriesWithPool(pool, q)
	runs := ledger.NewPostgres(q, pool)
	inputs := userinput.NewService(nil, store)
	service := &Service{messageService: messagepkg.NewService(nil, store), queries: store, userInput: inputs, sessionService: sessionpkg.NewService(nil, store, nil)}
	prefix := "shutdown-test:" + uuid.NewString()
	newManager := func(owner string) *sessionruntime.Manager {
		backend, err := sessionruntime.NewRedisBackend(ctx, sessionruntime.RedisOptions{URL: redisURL, KeyPrefix: prefix, StateTTL: time.Minute})
		if err != nil {
			t.Fatal(err)
		}
		manager := sessionruntime.NewManager(backend, sessionruntime.Options{OwnerID: owner, OwnerLeaseTTL: 600 * time.Millisecond, Ledger: runs, Fence: runtimefence.NewActivator(store)})
		manager.SetDecisionStore(service)
		return manager
	}
	old := newManager("old")
	admission, err := old.Admit(ctx, sessionruntime.AdmitInput{BotID: botID, SessionID: sessionID, InvocationID: "original", Payload: []byte(`{"resume":{"version":1}}`), Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
		return sessionruntime.RunAdmissionView{}, nil
	}}})
	if err != nil {
		t.Fatal(err)
	}
	requestMessageID := uuid.NewString()
	if _, err := pool.Exec(ctx, `INSERT INTO bot_history_messages (id,bot_id,session_id,role,content,turn_id,turn_position,turn_message_seq,turn_visible) VALUES ($1,$2,$3,'user','{}',$4,1,1,true)`, requestMessageID, botID, sessionID, admission.Handle.TurnID); err != nil {
		t.Fatal(err)
	}
	ownerCtx := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: admission.Handle.FencingToken})
	request, err := inputs.CreatePending(ownerCtx, userinput.CreatePendingInput{BotID: botID, SessionID: sessionID, ToolCallID: "ask", ToolName: userinput.ToolNameAskUser, Input: map[string]any{"questions": []any{map[string]any{"text": "Proceed?", "kind": userinput.QuestionKindText}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.HandleAgentEvent(ctx, admission.Handle, native.StreamEvent{Type: native.EventUserInputRequest, UserInputID: request.ID, ToolCallID: request.ToolCallID, Status: "pending", Input: request.Input}); err != nil {
		t.Fatal(err)
	}
	if _, err := old.FinishRun(ctx, admission.Handle, ""); err != nil {
		t.Fatal(err)
	}
	if err := old.InterruptForShutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := old.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	parked, err := runs.Get(ctx, admission.RunID)
	if err != nil || parked.State != ledger.StateWaitingDecision || parked.FencingToken <= admission.Handle.FencingToken {
		t.Fatalf("parked=%+v err=%v", parked, err)
	}
	next := newManager("new")
	defer func() { _ = next.CloseContext(context.Background()) }()
	var executions atomic.Int32
	next.SetCommandHandler(func(commandCtx context.Context, command sessionruntime.Command) error {
		runCtx, done, handle, err := next.DecisionContinuationContext(command)
		if err != nil {
			return err
		}
		defer done()
		if err := next.WaitDecisionContinuationReady(runCtx, command); err != nil {
			return err
		}
		var input UserInputResponseInput
		if err := json.Unmarshal(command.Payload, &input); err != nil {
			return err
		}
		requestID, err := service.continuationTurnRequestMessageID(runCtx, sessionID, handle)
		if err != nil {
			return err
		}
		if requestID != requestMessageID {
			return fmt.Errorf("continuation request id = %q, want %q", requestID, requestMessageID)
		}
		// Commit through the same application service used by the routed handler.
		if _, err := service.CommitUserInputResponse(commandCtx, input); err != nil {
			return err
		}
		executions.Add(1)
		if _, err := next.HandleAgentEvent(runCtx, handle, native.StreamEvent{Type: native.EventAgentStart}); err != nil {
			return err
		}
		_, err = next.FinishRun(context.WithoutCancel(runCtx), handle, sessionruntime.RunStatusCompleted)
		return err
	})
	if err := next.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		run, err := runs.Get(ctx, admission.RunID)
		if err != nil {
			t.Fatal(err)
		}
		if run.OwnerID == "new" && run.FencingToken > parked.FencingToken {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("new owner did not recover waiting decision")
		case <-ticker.C:
		}
	}
	payload, err := json.Marshal(UserInputResponseInput{ControlID: "answer", BotID: botID, ThreadID: sessionID, UserInputID: request.ID, TextAnswer: "yes"})
	if err != nil {
		t.Fatal(err)
	}
	response := sessionruntime.DecisionResponse{ControlID: "answer", Type: sessionruntime.CommandUserInputResponse, DecisionID: request.ID, BotID: botID, SessionID: sessionID, Payload: payload}
	for range 2 {
		result, err := next.RouteDecisionResponse(ctx, response)
		if err != nil || !result.Applied {
			t.Fatalf("response=%+v err=%v", result, err)
		}
	}
	if executions.Load() != 1 {
		t.Fatalf("continuation executed %d times", executions.Load())
	}
	decided, err := inputs.Get(ctx, request.ID)
	if err != nil || decided.Status != userinput.StatusSubmitted {
		t.Fatalf("answer=%+v err=%v", decided, err)
	}
	final, err := runs.Get(ctx, admission.RunID)
	if err != nil || final.State != ledger.StateCompleted {
		t.Fatalf("final=%+v err=%v", final, err)
	}
}
