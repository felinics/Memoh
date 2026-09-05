package handlers

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestLifecycleTurnsFromRunRowsCarryTurnID(t *testing.T) {
	t.Parallel()

	turnID := pgtype.UUID{Bytes: [16]byte{4, 2}, Valid: true}
	rows := []sqlc.ListRecentContextLifecyclesBySessionRow{{
		RunID:     pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		Status:    "completed",
		TurnID:    turnID,
		CreatedAt: pgtype.Timestamptz{Time: time.Unix(100, 0).UTC(), Valid: true},
		Snapshot:  lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{Version: 1}),
	}, {
		RunID:     pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		Status:    "completed",
		CreatedAt: pgtype.Timestamptz{Time: time.Unix(99, 0).UTC(), Valid: true},
		Snapshot:  lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{Version: 1}),
	}}
	turns, err := lifecycleTurnsFromRunRows(rows, 2)
	if err != nil {
		t.Fatalf("lifecycleTurnsFromRunRows: %v", err)
	}
	if turns[0].TurnID != turnID.String() {
		t.Fatalf("turn id = %q, want %q", turns[0].TurnID, turnID.String())
	}
	if turns[1].TurnID != "" {
		t.Fatalf("run without a ledger row must carry no turn id: %q", turns[1].TurnID)
	}
}
