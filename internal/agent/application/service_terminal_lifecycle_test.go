package application

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/runtimefence"
)

func TestContextLifecycleStatusForTerminalRunUsesNarrowedOutcomes(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		state  string
		status string
		ok     bool
	}{
		{state: "completed", status: contextLifecycleStatusCompleted, ok: true},
		{state: "aborted", status: contextLifecycleStatusAborted, ok: true},
		{state: "failed", status: contextLifecycleStatusFailedProvider, ok: true},
		{state: "lost", status: contextLifecycleStatusFailedProvider, ok: true},
		{state: "running"},
		{state: "waiting_decision"},
	} {
		t.Run(tt.state, func(t *testing.T) {
			status, ok := contextLifecycleStatusForTerminalRun(tt.state)
			if status != tt.status || ok != tt.ok {
				t.Fatalf("contextLifecycleStatusForTerminalRun(%q) = (%q, %t), want (%q, %t)", tt.state, status, ok, tt.status, tt.ok)
			}
		})
	}
}

func TestTerminalLifecycleCandidateWaitsForAuthoritativeFence(t *testing.T) {
	store := &recordingContextLifecycleStore{}
	service := &Service{contextLifecycles: store}
	snapshot := lifecycleRichSnapshot(t)
	ctx := lifecycleFencedContext(7)

	if !service.stageContextLifecycleCandidate(
		ctx,
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		&snapshot,
		nil,
		contextLifecycleCandidateAuthoritative,
	) {
		t.Fatal("admitted lifecycle candidate was not staged")
	}
	if len(store.terminalUpserts) != 0 {
		t.Fatalf("candidate wrote before durable terminal: %#v", store.terminalUpserts)
	}

	run := lifecycleTerminalRun(7, "completed", "")
	service.reconcileTerminalContextLifecycle(context.Background(), run)
	if len(store.terminalUpserts) != 1 {
		t.Fatalf("terminal upserts = %d, want 1", len(store.terminalUpserts))
	}
	assertLifecycleSnapshot(t, store.terminalUpserts[0].Snapshot, snapshot)
	if !store.terminalUpserts[0].ReplaceSnapshot {
		t.Fatal("authoritative candidate did not replace the minimal snapshot")
	}
	if !store.terminalUpsertBound {
		t.Fatal("terminal observer write was not bounded")
	}
	if _, ok := service.contextLifecycleCandidateFor(run); ok {
		t.Fatal("terminal candidate remained after successful upsert")
	}

	service.reconcileTerminalContextLifecycle(context.Background(), run)
	if len(store.terminalUpserts) != 2 || store.terminalUpserts[1].ReplaceSnapshot {
		t.Fatalf("duplicate terminal replay = %#v, want idempotent preserved snapshot", store.terminalUpserts)
	}
}

func TestTerminalLifecycleCandidateOwnsErrorCodeIndependently(t *testing.T) {
	store := &recordingContextLifecycleStore{}
	service := &Service{contextLifecycles: store}
	snapshot := lifecycleRichSnapshot(t)
	service.stageContextLifecycleCandidate(
		lifecycleFencedContext(8),
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		&snapshot,
		apperror.New(apperror.CodeWorkspaceUnreachable, nil),
		contextLifecycleCandidateAuthoritative,
	)

	service.reconcileTerminalContextLifecycle(
		context.Background(),
		lifecycleTerminalRun(8, "failed", "runtime_run_failed"),
	)
	if len(store.terminalUpserts) != 1 {
		t.Fatalf("terminal upserts = %d, want 1", len(store.terminalUpserts))
	}
	upsert := store.terminalUpserts[0]
	if !upsert.ReplaceSnapshot || !upsert.ReplaceErrorCode {
		t.Fatalf("candidate authorities = snapshot:%t error_code:%t, want both true", upsert.ReplaceSnapshot, upsert.ReplaceErrorCode)
	}
	if !upsert.ErrorCode.Valid || upsert.ErrorCode.String != string(apperror.CodeWorkspaceUnreachable) {
		t.Fatalf("candidate error code = %#v, want %q", upsert.ErrorCode, apperror.CodeWorkspaceUnreachable)
	}
}

func TestTerminalLifecycleMinimalCandidateOwnsOnlyErrorCode(t *testing.T) {
	store := &recordingContextLifecycleStore{}
	service := &Service{contextLifecycles: store}
	snapshot := minimalContextLifecycleSnapshot()
	service.stageContextLifecycleCandidate(
		lifecycleFencedContext(10),
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		&snapshot,
		apperror.New(apperror.CodeWorkspaceUnreachable, nil),
		contextLifecycleCandidateMinimal,
	)

	service.reconcileTerminalContextLifecycle(
		context.Background(),
		lifecycleTerminalRun(10, "failed", "runtime_run_failed"),
	)
	if len(store.terminalUpserts) != 1 {
		t.Fatalf("terminal upserts = %d, want 1", len(store.terminalUpserts))
	}
	upsert := store.terminalUpserts[0]
	if upsert.ReplaceSnapshot || !upsert.ReplaceErrorCode {
		t.Fatalf("minimal candidate authorities = snapshot:%t error_code:%t, want false/true", upsert.ReplaceSnapshot, upsert.ReplaceErrorCode)
	}
	if !upsert.ErrorCode.Valid || upsert.ErrorCode.String != string(apperror.CodeWorkspaceUnreachable) {
		t.Fatalf("minimal candidate error code = %#v, want %q", upsert.ErrorCode, apperror.CodeWorkspaceUnreachable)
	}
}

func TestTerminalLifecycleIgnoresStaleFenceCandidate(t *testing.T) {
	store := &recordingContextLifecycleStore{}
	service := &Service{contextLifecycles: store}
	snapshot := lifecycleRichSnapshot(t)
	service.stageContextLifecycleCandidate(
		lifecycleFencedContext(3),
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		&snapshot,
		apperror.New(apperror.CodeWorkspaceUnreachable, nil),
		contextLifecycleCandidateAuthoritative,
	)

	service.reconcileTerminalContextLifecycle(
		context.Background(),
		lifecycleTerminalRun(4, "failed", "runtime_run_failed"),
	)
	if len(store.terminalUpserts) != 1 {
		t.Fatalf("terminal upserts = %d, want 1", len(store.terminalUpserts))
	}
	var got contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(store.terminalUpserts[0].Snapshot, &got); err != nil {
		t.Fatal(err)
	}
	if got.Counts != (contextfrag.ManifestCounts{}) || got.AssistantMessageID != "" {
		t.Fatalf("stale candidate snapshot was used: %#v", got)
	}
	if code := store.terminalUpserts[0].ErrorCode; !code.Valid || code.String != "runtime_run_failed" {
		t.Fatalf("terminal error code = %#v, want stable ledger code", code)
	}
	if store.terminalUpserts[0].ReplaceErrorCode {
		t.Fatal("stale-fence candidate was granted error-code authority")
	}
	service.contextLifecycleCandidatesMu.Lock()
	remaining := len(service.contextLifecycleCandidates)
	service.contextLifecycleCandidatesMu.Unlock()
	if remaining != 0 {
		t.Fatalf("stale candidate generations remaining = %d, want 0", remaining)
	}
}

func TestTerminalLifecycleRetainsCandidateAcrossTransientStoreFailure(t *testing.T) {
	store := &recordingContextLifecycleStore{terminalUpsertErr: errors.New("database unavailable")}
	service := &Service{contextLifecycles: store}
	snapshot := lifecycleRichSnapshot(t)
	run := lifecycleTerminalRun(9, "completed", "")
	service.stageContextLifecycleCandidate(
		lifecycleFencedContext(9),
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		&snapshot,
		nil,
		contextLifecycleCandidateAuthoritative,
	)

	service.reconcileTerminalContextLifecycle(context.Background(), run)
	if _, ok := service.contextLifecycleCandidateFor(run); !ok {
		t.Fatal("candidate was discarded after a failed terminal upsert")
	}
	if got := service.contextLifecyclePersistenceErrors.Load(); got != 1 {
		t.Fatalf("persistence failure count = %d, want 1", got)
	}

	store.terminalUpsertErr = nil
	service.reconcileTerminalContextLifecycle(context.Background(), run)
	if _, ok := service.contextLifecycleCandidateFor(run); ok {
		t.Fatal("candidate remained after retry succeeded")
	}
	if len(store.terminalUpserts) != 2 {
		t.Fatalf("terminal upsert attempts = %d, want 2", len(store.terminalUpserts))
	}
	assertLifecycleSnapshot(t, store.terminalUpserts[1].Snapshot, snapshot)
}

func TestTerminalLifecycleReconcilerRepairsCrashGapWithBoundedRead(t *testing.T) {
	snapshot := lifecycleRichSnapshot(t)
	metadata, err := json.Marshal(map[string]any{contextfrag.MetadataContextLifecycleKey: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	const assistantID = "44444444-4444-4444-8444-444444444444"
	store := &recordingContextLifecycleStore{
		assistantID: flowTestUUID(assistantID),
		metadata:    metadata,
		terminalRows: []sqlc.ListTerminalSessionRunsNeedingContextLifecycleRow{{
			RunID:        flowTestUUID(lifecycleTestRunID),
			BotID:        flowTestUUID(lifecycleTestBotID),
			SessionID:    flowTestUUID(lifecycleTestSessionID),
			FencingToken: 12,
			State:        "failed",
			ErrorCode:    pgtype.Text{String: "provider.stable", Valid: true},
		}},
	}
	service := &Service{contextLifecycles: store}

	if err := service.reconcileTerminalContextLifecycles(context.Background()); err != nil {
		t.Fatalf("reconcileTerminalContextLifecycles() error = %v", err)
	}
	if store.terminalListCalls != 1 || store.terminalListLimit != contextLifecycleReconciliationBatchSize || !store.terminalListBound {
		t.Fatalf("reconciliation list = calls:%d limit:%d bounded:%t", store.terminalListCalls, store.terminalListLimit, store.terminalListBound)
	}
	if len(store.terminalUpserts) != 1 {
		t.Fatalf("terminal upserts = %d, want 1", len(store.terminalUpserts))
	}
	upsert := store.terminalUpserts[0]
	if upsert.Status != contextLifecycleStatusFailedProvider || !upsert.ErrorCode.Valid || upsert.ErrorCode.String != "provider.stable" {
		t.Fatalf("reconciled terminal = (%q, %#v)", upsert.Status, upsert.ErrorCode)
	}
	if !upsert.ReplaceSnapshot || upsert.ReplaceErrorCode {
		t.Fatalf("recovered metadata authorities = snapshot:%t error_code:%t, want true/false", upsert.ReplaceSnapshot, upsert.ReplaceErrorCode)
	}
	var recovered contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(upsert.Snapshot, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.AssistantMessageID != assistantID {
		t.Fatalf("recovered assistant message ID = %q, want %q", recovered.AssistantMessageID, assistantID)
	}

	store.terminalListErr = errors.New("query unavailable")
	if err := service.reconcileTerminalContextLifecycles(context.Background()); err == nil {
		t.Fatal("reconciliation query error was swallowed")
	}
}

func TestTerminalLifecycleReconcilerHonorsShorterDeadline(t *testing.T) {
	store := &recordingContextLifecycleStore{terminalListWait: true}
	service := &Service{contextLifecycles: store}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()

	err := service.reconcileTerminalContextLifecycles(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("reconciliation error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("bounded reconciliation took %s", elapsed)
	}
	if !store.terminalListBound {
		t.Fatal("blocking reconciliation query did not receive a deadline")
	}
}

func lifecycleRichSnapshot(t *testing.T) contextfrag.LifecycleSnapshot {
	t.Helper()
	config := lifecycleTestRunConfig()
	config.ContextLifecycle.SetAssistantMessageID("55555555-5555-4555-8555-555555555555")
	snapshot, ok := config.ContextLifecycle.Snapshot()
	if !ok {
		t.Fatal("lifecycle snapshot is unavailable")
	}
	return snapshot
}

func lifecycleFencedContext(token int64) context.Context {
	return runtimefence.WithContext(context.Background(), runtimefence.Fence{
		BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, Token: token,
	})
}

func lifecycleTerminalRun(token int64, state, errorCode string) sessionruntime.TerminalRun {
	return sessionruntime.TerminalRun{
		RunID: lifecycleTestRunID, BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID,
		FencingToken: token, State: state, ErrorCode: errorCode,
	}
}

func assertLifecycleSnapshot(t *testing.T, raw []byte, want contextfrag.LifecycleSnapshot) {
	t.Helper()
	var got contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("lifecycle snapshot = %#v, want %#v", got, want)
	}
}
