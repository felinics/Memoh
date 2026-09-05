//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/runtimefence"
	"github.com/felinics/memoh/internal/team"
)

func TestUserInputFencedChannelInteractionPostgres(t *testing.T) {
	ctx := context.Background()
	basePool := freshMigratedDB(t)
	cfg := basePool.Config()
	cfg.ConnConfig.RuntimeParams["memoh.team_id"] = team.DefaultTeamID
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	botID, sessionID, runID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	seedLifecycleReconciliationTeam(t, ctx, pool, team.DefaultTeamID, "ask-user-owner", botID, sessionID)
	if _, err := pool.Exec(ctx, `UPDATE bot_sessions SET runtime_fencing_token = 7 WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO session_runs (run_id, bot_id, session_id, invocation_id, turn_id, turn_position, state, input_json, input_fingerprint, fencing_token) VALUES ($1,$2,$3,'ask-user-test',gen_random_uuid(),1,'waiting_decision','{}','ask-user-test',7)`, runID, botID, sessionID); err != nil {
		t.Fatal(err)
	}
	svc := userinput.NewService(nil, postgresstore.NewQueriesWithPool(pool, sqlc.New(pool)))
	ownerCtx := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 7})
	req, err := svc.CreatePending(ownerCtx, userinput.CreatePendingInput{
		BotID: botID, SessionID: sessionID, ToolCallID: "ask-user-test",
		Input: map[string]any{"questions": []any{
			map[string]any{"text": "Plan?", "kind": "single_select", "options": []any{map[string]any{"label": "A"}, map[string]any{"label": "B"}}},
			map[string]any{"text": "End time?", "kind": "single_select", "allow_custom": true, "options": []any{map[string]any{"label": "September 15 00:00"}, map[string]any{"label": "September 15 23:59:59"}}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	clicked, err := svc.AdvanceInteraction(ctx, userinput.AdvanceInteractionInput{BotID: botID, RequestID: req.ID, Op: userinput.InteractionOp{Kind: userinput.OpSelectOption, OptionIndex: 0}})
	if err != nil || !clicked.Handled || clicked.Request.Interaction.QuestionIndex != 1 {
		t.Fatalf("button: %#v, %v", clicked, err)
	}
	typed, err := svc.AdvanceText(ctx, userinput.AdvanceTextInput{BotID: botID, SessionID: sessionID, ExplicitID: req.ID, Text: "0点"})
	if err != nil || !typed.Handled || !typed.Request.Interaction.Completed {
		t.Fatalf("text: %#v, %v", typed, err)
	}
	if len(typed.Request.Interaction.Answers) != 2 || typed.Request.Interaction.Answers[1].CustomText != "0点" {
		t.Fatalf("answers: %#v", typed.Request.Interaction.Answers)
	}
	input := userinput.SubmitInput{RequestID: req.ID, Answers: typed.Request.Interaction.Answers}
	if _, err := svc.Submit(ctx, input); err == nil {
		t.Fatal("unfenced channel committed a runtime decision")
	}
	staleCtx := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID, SessionID: sessionID, Token: 6})
	if _, err := svc.Submit(staleCtx, input); !errors.Is(err, userinput.ErrAlreadyDecided) {
		t.Fatalf("stale owner: %v", err)
	}
	pending, err := svc.Get(ctx, req.ID)
	if err != nil || pending.Status != userinput.StatusPending {
		t.Fatalf("rejected write changed request: %#v, %v", pending, err)
	}
	accepted, err := svc.Submit(ownerCtx, input)
	if err != nil || accepted.Status != userinput.StatusSubmitted {
		t.Fatalf("owner submit: %#v, %v", accepted, err)
	}
	replay, err := svc.AdvanceInteraction(ctx, userinput.AdvanceInteractionInput{BotID: botID, RequestID: req.ID, Op: userinput.InteractionOp{Kind: userinput.OpSubmit}})
	if err != nil || replay.Handled {
		t.Fatalf("submitted card is still editable: %#v, %v", replay, err)
	}
}
