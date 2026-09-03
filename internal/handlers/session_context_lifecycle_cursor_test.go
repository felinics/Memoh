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
	runID := pgtype.UUID{Bytes: [16]byte{7, 1}, Valid: true}
	cursor := encodeContextLifecycleCursor(at, runID)
	decoded, ok := decodeContextLifecycleCursor(cursor)
	if !ok || !decoded.createdAt.Equal(at) || decoded.runID != runID {
		t.Fatalf("decoded = %#v (ok=%v) from %q", decoded, ok, cursor)
	}
	for _, bad := range []string{"", "garbage", "bm90LWEtY3Vyc29y"} {
		if _, ok := decodeContextLifecycleCursor(bad); ok {
			t.Fatalf("cursor %q decoded", bad)
		}
	}
}

func TestLoadContextLifecycleTurnsAppliesCursorAndReportsNext(t *testing.T) {
	t.Parallel()

	rows := make([]sqlc.ListRecentContextLifecyclesBySessionRow, 0, 3)
	for i := byte(1); i <= 3; i++ {
		rows = append(rows, sqlc.ListRecentContextLifecyclesBySessionRow{
			RunID:     pgtype.UUID{Bytes: [16]byte{i}, Valid: true},
			Status:    "completed",
			CreatedAt: pgtype.Timestamptz{Time: time.Unix(int64(100-i), 0).UTC(), Valid: true},
			Snapshot:  lifecycleSnapshotJSON(t, contextfrag.LifecycleSnapshot{Version: 1}),
		})
	}
	queries := &contextLifecycleQueryStub{lifecycleRows: rows}
	before := &contextLifecycleCursor{createdAt: time.Unix(200, 0).UTC(), runID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}

	load, err := loadContextLifecycleTurns(context.Background(), queries, pgtype.UUID{Bytes: [16]byte{9}, Valid: true}, 2, before)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	params := queries.lifecycleParams[0]
	if !params.BeforeCreatedAt.Valid || !params.BeforeCreatedAt.Time.Equal(before.createdAt) || params.BeforeRunID != before.runID {
		t.Fatalf("query params = %#v, want the cursor bound", params)
	}
	if !load.HasMore || len(load.Turns) != 2 {
		t.Fatalf("load = %#v", load)
	}
	want := encodeContextLifecycleCursor(load.Turns[1].CreatedAt, rows[1].RunID)
	if load.NextCursor != want {
		t.Fatalf("next cursor = %q, want %q", load.NextCursor, want)
	}

	queries = &contextLifecycleQueryStub{lifecycleRows: rows[:1]}
	load, err = loadContextLifecycleTurns(context.Background(), queries, pgtype.UUID{Bytes: [16]byte{9}, Valid: true}, 2, nil)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if load.HasMore || load.NextCursor != "" || queries.lifecycleParams[0].BeforeCreatedAt.Valid {
		t.Fatalf("unbounded load = %#v params %#v", load, queries.lifecycleParams[0])
	}
}
