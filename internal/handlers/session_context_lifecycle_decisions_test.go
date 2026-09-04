package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type contextLifecycleDecisionStub struct {
	run       sqlc.GetContextLifecycleByRunIDRow
	runErr    error
	decisions []byte
	decErr    error
	decCalls  int
}

func (s *contextLifecycleDecisionStub) GetContextLifecycleByRunID(context.Context, pgtype.UUID) (sqlc.GetContextLifecycleByRunIDRow, error) {
	return s.run, s.runErr
}

func (s *contextLifecycleDecisionStub) GetContextLifecycleSelectionDecisionsByRunID(context.Context, pgtype.UUID) ([]byte, error) {
	s.decCalls++
	return s.decisions, s.decErr
}

func TestLoadContextLifecycleDecisionsReturnsTheRunsAudit(t *testing.T) {
	t.Parallel()

	sessionID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	runID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	raw, _ := json.Marshal([]contextfrag.SelectionDecision{
		{ID: "sys-1", Slot: contextfrag.SlotSystem, Decision: contextfrag.DecisionSelected, TokenEstimate: 1_300},
		{ID: "msg-9", Slot: contextfrag.SlotHistory, Decision: contextfrag.DecisionDropped, Reason: "budget", TokenEstimate: 420},
	})
	stub := &contextLifecycleDecisionStub{run: sqlc.GetContextLifecycleByRunIDRow{RunID: runID, SessionID: sessionID}, decisions: raw}

	decisions, err := loadContextLifecycleDecisions(context.Background(), stub, sessionID, runID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(decisions) != 2 || decisions[1].Reason != "budget" || decisions[0].Slot != contextfrag.SlotSystem {
		t.Fatalf("decisions = %#v", decisions)
	}
}

func TestLoadContextLifecycleDecisionsHidesRunsOfOtherSessions(t *testing.T) {
	t.Parallel()

	sessionID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	runID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	stub := &contextLifecycleDecisionStub{run: sqlc.GetContextLifecycleByRunIDRow{RunID: runID, SessionID: pgtype.UUID{Bytes: [16]byte{3}, Valid: true}}}

	if _, err := loadContextLifecycleDecisions(context.Background(), stub, sessionID, runID); !errors.Is(err, errContextLifecycleRunNotFound) {
		t.Fatalf("err = %v, want run-not-found", err)
	}
	if stub.decCalls != 0 {
		t.Fatalf("decisions were read for a foreign run")
	}

	missing := &contextLifecycleDecisionStub{runErr: pgx.ErrNoRows}
	if _, err := loadContextLifecycleDecisions(context.Background(), missing, sessionID, runID); !errors.Is(err, errContextLifecycleRunNotFound) {
		t.Fatalf("err = %v, want run-not-found for a missing run", err)
	}
}

func TestLoadContextLifecycleDecisionsTreatsAnEmptyAuditAsNoDecisions(t *testing.T) {
	t.Parallel()

	sessionID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	runID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	stub := &contextLifecycleDecisionStub{run: sqlc.GetContextLifecycleByRunIDRow{RunID: runID, SessionID: sessionID}, decisions: []byte("null")}

	decisions, err := loadContextLifecycleDecisions(context.Background(), stub, sessionID, runID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if decisions == nil || len(decisions) != 0 {
		t.Fatalf("decisions = %#v, want an empty non-nil slice", decisions)
	}
}
