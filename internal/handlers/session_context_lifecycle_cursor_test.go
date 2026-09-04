package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestContextLifecycleCursorRoundTrip(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 3, 10, 0, 0, 123456000, time.UTC)
	runID := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	cursor := encodeContextLifecycleCursor(at, runID)
	decoded, ok := decodeContextLifecycleCursor(cursor)
	if !ok || !decoded.createdAt.Equal(at) || decoded.runID != runID {
		t.Fatalf("decoded = %#v (ok=%v)", decoded, ok)
	}
	if _, ok := decodeContextLifecycleCursor("garbage"); ok {
		t.Fatalf("garbage decoded")
	}
	if _, ok := decodeContextLifecycleCursor(""); ok {
		t.Fatalf("empty cursor decoded")
	}
}

func cursorTestRows(t *testing.T) []sqlc.ListRecentContextLifecyclesBySessionRow {
	t.Helper()
	rows := make([]sqlc.ListRecentContextLifecyclesBySessionRow, 0, 3)
	for i := byte(1); i <= 3; i++ {
		rows = append(rows, sqlc.ListRecentContextLifecyclesBySessionRow{
			RunID:     pgtype.UUID{Bytes: [16]byte{i}, Valid: true},
			Status:    "completed",
			CreatedAt: pgtype.Timestamptz{Time: time.Unix(int64(100-i), 0).UTC(), Valid: true},
			Snapshot:  lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{Version: 1}),
		})
	}
	return rows
}

func TestLoadContextLifecycleTurnsUsesTheCursorQueryAndReportsNext(t *testing.T) {
	t.Parallel()

	rows := cursorTestRows(t)
	queries := &contextLifecycleQueryStub{lifecycleRows: rows}
	before := &contextLifecycleCursor{createdAt: time.Unix(200, 0).UTC(), runID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}

	load, err := loadContextLifecycleTurns(context.Background(), queries, pgtype.UUID{Bytes: [16]byte{9}, Valid: true}, 2, before)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(queries.lifecycleParams) != 0 || len(queries.lifecycleBeforeParams) != 1 {
		t.Fatalf("query calls = plain %d, cursor %d; want the cursor query only", len(queries.lifecycleParams), len(queries.lifecycleBeforeParams))
	}
	params := queries.lifecycleBeforeParams[0]
	if !params.BeforeCreatedAt.Valid || !params.BeforeCreatedAt.Time.Equal(before.createdAt) || params.BeforeRunID != before.runID {
		t.Fatalf("cursor params = %#v", params)
	}
	if !load.HasMore || load.NextCursor != encodeContextLifecycleCursor(rows[1].CreatedAt.Time, rows[1].RunID) {
		t.Fatalf("has_more=%v next=%q", load.HasMore, load.NextCursor)
	}

	queries = &contextLifecycleQueryStub{lifecycleRows: rows[:1]}
	load, err = loadContextLifecycleTurns(context.Background(), queries, pgtype.UUID{Bytes: [16]byte{9}, Valid: true}, 2, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(queries.lifecycleParams) != 1 || len(queries.lifecycleBeforeParams) != 0 {
		t.Fatalf("query calls = plain %d, cursor %d; want the plain query only", len(queries.lifecycleParams), len(queries.lifecycleBeforeParams))
	}
	if load.HasMore || load.NextCursor != "" {
		t.Fatalf("complete page must not carry a cursor: has_more=%v next=%q", load.HasMore, load.NextCursor)
	}
}

func TestLoadContextLifecycleTurnsLegacyIgnoresCursor(t *testing.T) {
	t.Parallel()

	queries := &contextLifecycleQueryStub{legacyRows: []sqlc.ListRecentAssistantMessagesBySessionRow{
		legacyLifecycleRow(t, pgtype.UUID{Bytes: [16]byte{4}, Valid: true}, time.Unix(50, 0).UTC(), &contextfrag.LifecycleSnapshot{Version: 1}),
	}}
	before := &contextLifecycleCursor{createdAt: time.Unix(200, 0).UTC(), runID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}
	load, err := loadContextLifecycleTurns(context.Background(), queries, pgtype.UUID{Bytes: [16]byte{9}, Valid: true}, 2, before)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !load.LegacySource || load.NextCursor != "" {
		t.Fatalf("legacy load = %#v", load)
	}
}
