package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/runtime/session/ledger"
	"github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type resumeTestFence struct{}

func (resumeTestFence) Activate(context.Context, string, string, int64) error { return nil }

func TestPostgresGracefulShutdownResumeSurvivesNewManager(t *testing.T) {
	ctx := t.Context()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	queries := dbsqlc.New(pool)
	runs := ledger.NewPostgres(queries, pool)
	manager := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: runs, Fence: resumeTestFence{}})
	build := func(context.Context, sessionruntime.RunHandle) (sessionruntime.RunAdmissionView, error) {
		return sessionruntime.RunAdmissionView{}, nil
	}
	first, err := manager.Admit(ctx, sessionruntime.AdmitInput{BotID: botID, SessionID: sessionID, InvocationID: "original", Payload: []byte(`{"query":"work"}`), Execution: sessionruntime.Execution{Admission: build}})
	if err != nil {
		t.Fatal(err)
	}
	resumeJSON := []byte(`{"version":1,"chat_id":"chat","query":"work"}`)
	params := dbsqlc.SaveSessionRunResumeContextParams{RunID: db.ParseUUIDOrEmpty(first.RunID), FencingToken: first.Handle.FencingToken, ResumeContext: resumeJSON}
	stale := params
	stale.FencingToken--
	if n, err := queries.SaveSessionRunResumeContext(ctx, stale); err != nil || n != 0 {
		t.Fatalf("stale writer=%d %v", n, err)
	}
	if n, err := queries.SaveSessionRunResumeContext(ctx, params); err != nil || n != 1 {
		t.Fatalf("save=%d %v", n, err)
	}
	if err := manager.InterruptForShutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.CloseContext(ctx); err != nil {
		t.Fatal(err)
	}
	interrupted, err := runs.Get(ctx, first.RunID)
	if err != nil || interrupted.ErrorCode != sessionruntime.RunErrorInterrupted {
		t.Fatalf("marker=%+v %v", interrupted, err)
	}
	var saved map[string]json.RawMessage
	if err := json.Unmarshal(interrupted.Input, &saved); err != nil || string(saved["query"]) != `"work"` {
		t.Fatal("original input lost")
	}
	candidates, err := queries.ListInterruptedSessionRuns(ctx, pgtype.UUID{Valid: true})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range candidates {
		if row.RunID.String() == first.RunID {
			found = true
		}
	}
	if !found {
		t.Fatal("restart did not discover interrupted run")
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			next := sessionruntime.NewManager(sessionruntime.NewMemoryBackend(), sessionruntime.Options{Ledger: runs, Fence: resumeTestFence{}})
			defer func() { _ = next.CloseContext(context.WithoutCancel(ctx)) }()
			admission, err := next.Admit(ctx, sessionruntime.AdmitInput{BotID: botID, SessionID: sessionID, InvocationID: "resume:" + first.RunID, ResumeRunID: first.RunID, Payload: []byte(`{"query":"continue"}`), Execution: sessionruntime.Execution{Admission: build}})
			if err != nil {
				t.Error(err)
				return
			}
			if admission.Started {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatalf("resume executed %d times", winners.Load())
	}
	candidates, err = queries.ListInterruptedSessionRuns(ctx, db.ParseUUIDOrEmpty(uuid.Nil.String()))
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range candidates {
		if row.RunID.String() == first.RunID {
			t.Fatal("already resumed source reselected")
		}
	}
}

func TestPostgresResumeRejectsCandidateSupersededAfterScan(t *testing.T) {
	ctx := t.Context()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	runs := ledger.NewPostgres(dbsqlc.New(pool), pool)
	oldID, token := createClaimedLedgerRun(t, ctx, pool, botID, sessionID)
	if _, _, err := runs.Finalize(ctx, ledger.FinalizeParams{RunID: oldID, FencingToken: token, State: ledger.StateLost, ErrorCode: sessionruntime.RunErrorInterrupted}); err != nil {
		t.Fatal(err)
	}
	// A user finishes another turn after the recovery worker discovered oldID.
	newerID, newerToken := createClaimedLedgerRun(t, ctx, pool, botID, sessionID)
	if _, err := pool.Exec(ctx, "UPDATE session_runs SET turn_position=2 WHERE run_id=$1", newerID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := runs.Finalize(ctx, ledger.FinalizeParams{RunID: newerID, FencingToken: newerToken, State: ledger.StateCompleted}); err != nil {
		t.Fatal(err)
	}
	_, created, err := runs.Admit(ctx, ledger.AdmitParams{RunID: uuid.NewString(), BotID: botID, SessionID: sessionID, InvocationID: "resume:" + oldID, TurnID: uuid.NewString(), Input: []byte(`{}`), InputFingerprint: "resume", ResumeRunID: oldID})
	if created || !errors.Is(err, ledger.ErrResumeSuperseded) {
		t.Fatalf("stale continuation admitted: %v %v", created, err)
	}
	var count int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM session_runs WHERE session_id=$1", sessionID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}
