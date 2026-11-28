package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type fakeForkQueries struct {
	dbstore.Queries
	sourceSession sqlc.BotSession
	forkRow       sqlc.ForkSessionFromAssistantTurnRow
	forkErr       error
	checkpointRow sqlc.BotHistoryCheckpoint
	checkpointErr error
	summaryRows   []sqlc.ListHistoryCheckpointSummariesRow
	copySumErr    error
}

func (f *fakeForkQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	return fn(f)
}

func (f *fakeForkQueries) GetSessionByID(context.Context, pgtype.UUID) (sqlc.BotSession, error) {
	return f.sourceSession, nil
}

func (*fakeForkQueries) LockBotForSessionWrite(_ context.Context, id pgtype.UUID) (pgtype.UUID, error) {
	return id, nil
}

func (*fakeForkQueries) LockSessionForFork(context.Context, pgtype.UUID) error {
	return nil
}

func (f *fakeForkQueries) ForkSessionFromAssistantTurn(context.Context, sqlc.ForkSessionFromAssistantTurnParams) (sqlc.ForkSessionFromAssistantTurnRow, error) {
	return f.forkRow, f.forkErr
}

func (f *fakeForkQueries) GetHistoryCheckpointKeptThroughTurn(context.Context, sqlc.GetHistoryCheckpointKeptThroughTurnParams) (sqlc.BotHistoryCheckpoint, error) {
	return f.checkpointRow, f.checkpointErr
}

func (f *fakeForkQueries) ListHistoryCheckpointSummaries(context.Context, sqlc.ListHistoryCheckpointSummariesParams) ([]sqlc.ListHistoryCheckpointSummariesRow, error) {
	return f.summaryRows, nil
}

func (f *fakeForkQueries) CopyCompactionSummary(context.Context, sqlc.CopyCompactionSummaryParams) (pgtype.UUID, error) {
	return pgtype.UUID{}, f.copySumErr
}

func TestForkFromAssistantTurnRuntimeOverrideRequiredIsInternalError(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-4111-8111-111111111111"
	sessionID := "22222222-2222-4222-8222-222222222222"
	turnID := "33333333-3333-4333-8333-333333333333"
	queries := &fakeForkQueries{
		sourceSession: sqlc.BotSession{
			ID:    mustPGUUID(sessionID),
			BotID: mustPGUUID(botID),
			Type:  TypeChat,
		},
	}
	svc := NewService(nil, queries, nil)

	_, err := svc.ForkFromAssistantTurn(context.Background(), ForkFromAssistantInput{
		BotID:               botID,
		ThreadID:            sessionID,
		TurnID:              turnID,
		RuntimeForkRequired: true,
	})
	if err == nil {
		t.Fatal("ForkFromAssistantTurn() succeeded, want internal error")
	}
	if errors.Is(err, ErrForkSourceNotChat) || errors.Is(err, ErrForkSourceNotReply) {
		t.Fatalf("ForkFromAssistantTurn() error = %v, must not masquerade as client fork sentinel", err)
	}
}

func TestForkFromAssistantTurnDistinguishesForkNoRowsFromCheckpointNoRows(t *testing.T) {
	t.Parallel()

	botID := "11111111-1111-4111-8111-111111111111"
	sessionID := "22222222-2222-4222-8222-222222222222"
	forkID := "44444444-4444-4444-8444-444444444444"
	turnID := "33333333-3333-4333-8333-333333333333"
	checkpointID := "55555555-5555-4555-8555-555555555555"
	summaryID := "66666666-6666-4666-8666-666666666666"

	source := sqlc.BotSession{
		ID:    mustPGUUID(sessionID),
		BotID: mustPGUUID(botID),
		Type:  TypeChat,
	}

	t.Run("ForkSessionFromAssistantTurn ErrNoRows maps to ErrForkSourceNotReply with cause", func(t *testing.T) {
		t.Parallel()
		svc := NewService(nil, &fakeForkQueries{
			sourceSession: source,
			forkErr:       pgx.ErrNoRows,
		}, nil)

		_, err := svc.ForkFromAssistantTurn(context.Background(), ForkFromAssistantInput{
			BotID:    botID,
			ThreadID: sessionID,
			TurnID:   turnID,
		})
		if !errors.Is(err, ErrForkSourceNotReply) || !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("ForkFromAssistantTurn() error = %v, want ErrForkSourceNotReply wrapping pgx.ErrNoRows", err)
		}
	})

	t.Run("checkpoint CopyToFork ErrNoRows stays internal error", func(t *testing.T) {
		t.Parallel()
		svc := NewService(nil, &fakeForkQueries{
			sourceSession: source,
			forkRow: sqlc.ForkSessionFromAssistantTurnRow{
				ID:                  mustPGUUID(forkID),
				ThroughTurnPosition: 1,
			},
			checkpointRow: sqlc.BotHistoryCheckpoint{
				ID:        mustPGUUID(checkpointID),
				SessionID: mustPGUUID(sessionID),
				Items:     []byte(`[{"summary":"` + summaryID + `"}]`),
			},
			summaryRows: []sqlc.ListHistoryCheckpointSummariesRow{
				{ID: mustPGUUID(summaryID), Summary: "summary text"},
			},
			copySumErr: pgx.ErrNoRows,
		}, nil)

		_, err := svc.ForkFromAssistantTurn(context.Background(), ForkFromAssistantInput{
			BotID:    botID,
			ThreadID: sessionID,
			TurnID:   turnID,
		})
		if err == nil {
			t.Fatal("ForkFromAssistantTurn() succeeded, want checkpoint copy error")
		}
		if errors.Is(err, ErrForkSourceNotReply) {
			t.Fatalf("checkpoint CopyToFork ErrNoRows must not map to ErrForkSourceNotReply: %v", err)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("ForkFromAssistantTurn() lost pgx.ErrNoRows cause: %v", err)
		}
	})
}
