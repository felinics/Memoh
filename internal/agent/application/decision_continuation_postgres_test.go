package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/decision"
	toolapproval "github.com/felinics/memoh/internal/agent/decision/approval"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestPostgresNativeDecisionAcceptanceAndShutdownRecovery(t *testing.T) {
	for _, scenario := range []string{"approved", "rejected", "answer", "aborted", "inline"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool := openTurnAdmissionPostgres(t, ctx)
			botID, sessionID := createTurnAdmissionFixture(t, ctx, pool)
			q := sqlc.New(pool)
			store := postgresstore.NewQueriesWithPool(pool, q)
			runs := ledger.NewPostgres(q, pool)
			manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: runs, Fence: runtimefence.NewActivator(store)})
			t.Cleanup(func() { _ = manager.CloseContext(context.Background()) })
			admission, err := manager.Admit(ctx, sessionruntime.AdmitInput{BotID: botID, SessionID: sessionID, InvocationID: "native-decision", Payload: []byte(`{"resume":{"version":1,"chat_id":"chat","query":"approved work"}}`), Execution: sessionruntime.Execution{Admission: func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
				return sessionruntime.RunAdmissionView{}, nil
			}}})
			if err != nil {
				t.Fatal(err)
			}
			ownerCtx := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: admission.Handle.FencingToken})
			approvals := toolapproval.NewService(nil, store, nil)
			approval, err := approvals.CreatePending(ownerCtx, toolapproval.CreatePendingInput{BotID: botID, SessionID: sessionID, ToolCallID: "long-tool", ToolName: "exec", ToolInput: map[string]any{"command": "long side effect"}})
			if err != nil {
				t.Fatal(err)
			}
			inputs := userinput.NewService(nil, store)
			input, err := inputs.CreatePending(ownerCtx, userinput.CreatePendingInput{BotID: botID, SessionID: sessionID, ToolCallID: "ask", Input: map[string]any{"questions": []any{map[string]any{"text": "Proceed?", "kind": "text"}}}})
			if err != nil {
				t.Fatal(err)
			}
			_, _, _ = runs.SetWaitingDecision(ctx, admission.RunID, admission.Handle.FencingToken)
			if scenario == "aborted" {
				_, _, _ = runs.RequestAbort(ctx, admission.RunID)
			}
			responseCtx := ownerCtx
			if scenario != "inline" {
				responseCtx = decision.WithNativeContinuation(responseCtx)
			}
			switch scenario {
			case "rejected":
				_, err = approvals.Reject(responseCtx, approval.ID, "", "no")
			case "answer":
				_, err = inputs.Submit(responseCtx, userinput.SubmitInput{RequestID: input.ID, Answers: []userinput.QuestionAnswer{{QuestionID: input.UIPayload.Questions[0].ID, Text: "accepted-answer"}}})
			default:
				_, err = approvals.Approve(responseCtx, approval.ID, "", "yes")
			}
			if scenario == "aborted" {
				if !errors.Is(err, runtimefence.ErrStale) {
					t.Fatalf("abort accepted a continuation: %v", err)
				}
				unchanged, _ := approvals.Get(ctx, approval.ID)
				if unchanged.Status != toolapproval.StatusPending {
					t.Fatal("answer committed despite failed run transition")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			run, _ := runs.Get(ctx, admission.RunID)
			if scenario == "inline" {
				if run.State != ledger.StateWaitingDecision {
					t.Fatal("inline waiter resumed before its producer")
				}
				return
			}
			if run.State != ledger.StateRunning {
				t.Fatal("accepted native answer was left waiting")
			}
			if scenario == "approved" {
				// Another decision can commit before the first tool returns.
				// Both accepted facts must survive; the checkpoint is a map,
				// not a last-writer-wins slot.
				if _, err := inputs.Submit(responseCtx, userinput.SubmitInput{RequestID: input.ID, Answers: []userinput.QuestionAnswer{{QuestionID: input.UIPayload.Questions[0].ID, Text: "sibling-answer"}}}); err != nil {
					t.Fatal(err)
				}
				count, err := store.MarkSessionRunDecisionExecuting(ownerCtx, sqlc.MarkSessionRunDecisionExecutingParams{BotID: db.ParseUUIDOrEmpty(botID), SessionID: db.ParseUUIDOrEmpty(sessionID), RunID: db.ParseUUIDOrEmpty(admission.RunID), FencingToken: admission.Handle.FencingToken, DecisionID: approval.ID})
				if err != nil || count != 1 {
					t.Fatalf("execution checkpoint=%d err=%v", count, err)
				}
				// The execution claim is once-only, even while the same owner
				// and response identity remain valid.
				count, err = store.MarkSessionRunDecisionExecuting(ownerCtx, sqlc.MarkSessionRunDecisionExecutingParams{BotID: db.ParseUUIDOrEmpty(botID), SessionID: db.ParseUUIDOrEmpty(sessionID), RunID: db.ParseUUIDOrEmpty(admission.RunID), FencingToken: admission.Handle.FencingToken, DecisionID: approval.ID})
				if err != nil || count != 0 {
					t.Fatalf("approved execution claimed twice: %d %v", count, err)
				}
			}
			// The tool has not returned and no agent_start has been emitted.
			if err := manager.InterruptForShutdown(ctx); err != nil {
				t.Fatal(err)
			}
			row, err := q.GetSessionRun(ctx, db.ParseUUIDOrEmpty(admission.RunID))
			if err != nil || row.State != "lost" || row.ErrorCode.String != sessionruntime.RunErrorInterrupted {
				t.Fatalf("accepted continuation lost recovery semantics: %+v %v", row, err)
			}
			var saved struct {
				Checkpoints map[string]decision.ContinuationCheckpoint `json:"decision_continuations"`
			}
			if err := json.Unmarshal(row.InputJson, &saved); err != nil {
				t.Fatal(err)
			}
			svc := &Service{queries: store}
			instruction, err := svc.interruptedDecisionContext(ctx, row, saved.Checkpoints)
			if err != nil || instruction == "" || !strings.Contains(instruction, "Do not automatically replay") {
				t.Fatalf("decision recovery context=%q err=%v", instruction, err)
			}
			if scenario == "answer" && !strings.Contains(instruction, "accepted-answer") {
				t.Fatal("accepted user answer missing from recovery")
			}
			if scenario == "approved" && (len(saved.Checkpoints) != 2 || !strings.Contains(instruction, "sibling-answer")) {
				t.Fatal("a sibling accepted decision was lost")
			}
			// A different run must never inherit this accepted authority.
			row.RunID = db.ParseUUIDOrEmpty("00000000-0000-0000-0000-000000000001")
			if _, err := svc.interruptedDecisionContext(ctx, row, saved.Checkpoints); !errors.Is(err, errResumeUnrecoverable) {
				t.Fatalf("cross-run checkpoint accepted: %v", err)
			}
		})
	}
}
