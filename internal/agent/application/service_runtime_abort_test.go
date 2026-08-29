package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type abortLifecycleQueries struct {
	dbstore.Queries
	mu            sync.Mutex
	existing      *sqlc.ContextLifecycle
	existingAfter int
	getCalls      int
	assistantID   pgtype.UUID
	metadata      []byte
	metadataErr   error
	sessionRun    sqlc.SessionRun
	sessionRunErr error
	pending       bool
	pendingReads  int
	pendingUntil  int
	upserts       []sqlc.UpsertAbortedContextLifecycleParams
	upsertErr     error
	upsertCh      chan struct{}
}

func (*abortLifecycleQueries) CreateContextLifecycle(
	context.Context,
	sqlc.CreateContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	return sqlc.ContextLifecycle{}, errors.New("unexpected lifecycle create")
}

func (q *abortLifecycleQueries) GetContextLifecycleByRunID(
	context.Context,
	pgtype.UUID,
) (sqlc.ContextLifecycle, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.getCalls++
	if q.existing == nil || q.existingAfter > 0 && q.getCalls < q.existingAfter {
		return sqlc.ContextLifecycle{}, pgx.ErrNoRows
	}
	return *q.existing, nil
}

func (q *abortLifecycleQueries) GetLatestAssistantContextLifecycleMetadataByRunID(
	context.Context,
	pgtype.UUID,
) ([]byte, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.metadataErr != nil {
		return nil, q.metadataErr
	}
	if q.metadata == nil {
		return nil, pgx.ErrNoRows
	}
	return append([]byte(nil), q.metadata...), nil
}

func (q *abortLifecycleQueries) GetLatestAssistantContextLifecycleByRunID(
	context.Context,
	pgtype.UUID,
) (sqlc.GetLatestAssistantContextLifecycleByRunIDRow, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.metadataErr != nil {
		return sqlc.GetLatestAssistantContextLifecycleByRunIDRow{}, q.metadataErr
	}
	if q.metadata == nil {
		return sqlc.GetLatestAssistantContextLifecycleByRunIDRow{}, pgx.ErrNoRows
	}
	return sqlc.GetLatestAssistantContextLifecycleByRunIDRow{
		ID:       q.assistantID,
		Metadata: append([]byte(nil), q.metadata...),
	}, nil
}

func (*abortLifecycleQueries) UpdateAbortedContextLifecycleSnapshot(
	context.Context,
	sqlc.UpdateAbortedContextLifecycleSnapshotParams,
) (sqlc.ContextLifecycle, error) {
	return sqlc.ContextLifecycle{}, errors.New("unexpected lifecycle update")
}

func (q *abortLifecycleQueries) UpsertAbortedContextLifecycle(
	_ context.Context,
	arg sqlc.UpsertAbortedContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	q.mu.Lock()
	q.upserts = append(q.upserts, arg)
	q.mu.Unlock()
	if q.upsertCh != nil {
		select {
		case q.upsertCh <- struct{}{}:
		default:
		}
	}
	return sqlc.ContextLifecycle{}, q.upsertErr
}

func (q *abortLifecycleQueries) GetSessionRun(context.Context, pgtype.UUID) (sqlc.SessionRun, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.sessionRun, q.sessionRunErr
}

func (q *abortLifecycleQueries) GetPendingToolApprovalByRun(
	context.Context,
	pgtype.UUID,
) (sqlc.ToolApprovalRequest, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.pendingReads++
	if !q.pending || q.pendingUntil > 0 && q.pendingReads > q.pendingUntil {
		return sqlc.ToolApprovalRequest{}, pgx.ErrNoRows
	}
	return sqlc.ToolApprovalRequest{}, nil
}

func (*abortLifecycleQueries) GetPendingUserInputByRun(
	context.Context,
	pgtype.UUID,
) (sqlc.UserInputRequest, error) {
	return sqlc.UserInputRequest{}, pgx.ErrNoRows
}

func (q *abortLifecycleQueries) recordedUpserts() []sqlc.UpsertAbortedContextLifecycleParams {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]sqlc.UpsertAbortedContextLifecycleParams(nil), q.upserts...)
}

type recordingAbortRuntime struct {
	applied bool
	err     error
}

func TestSetSessionRuntimeConfiguresAbortRuntime(t *testing.T) {
	manager := &sessionruntime.Manager{}
	service := &Service{}

	service.SetSessionRuntime(manager)

	if service.abortRuntime != manager {
		t.Fatalf("abort runtime = %T, want injected session manager", service.abortRuntime)
	}
}

func (r *recordingAbortRuntime) AbortControl(
	context.Context,
	string,
	string,
	string,
	string,
) (bool, error) {
	return r.applied, r.err
}

func TestAbortRuntimeRunReconcilesAssistantLifecycleWithoutChangingAck(t *testing.T) {
	snapshot := lifecycleTestRunConfig().ContextLifecycle
	want, ok := snapshot.Snapshot()
	if !ok {
		t.Fatal("test lifecycle snapshot is unavailable")
	}
	metadata, err := json.Marshal(map[string]any{contextfrag.MetadataContextLifecycleKey: want})
	if err != nil {
		t.Fatal(err)
	}
	const assistantMessageID = "66666666-6666-4666-8666-666666666666"
	want.AssistantMessageID = assistantMessageID
	wantRaw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name             string
		upsertErr        error
		wantFailureCount uint64
	}{
		{name: "writes recovered snapshot"},
		{name: "store failure leaves acknowledgement", upsertErr: errors.New("database unavailable"), wantFailureCount: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queries := newAbortedLifecycleQueries(t)
			queries.assistantID = flowTestUUID(assistantMessageID)
			queries.metadata = metadata
			queries.upsertErr = tt.upsertErr
			service := &Service{
				queries:           queries,
				contextLifecycles: queries,
				abortRuntime:      &recordingAbortRuntime{applied: true},
			}

			applied, err := service.AbortRuntimeRun(
				context.Background(),
				lifecycleTestBotID,
				lifecycleTestSessionID,
				lifecycleTestRunID,
				"abort-control-1",
			)
			if err != nil || !applied {
				t.Fatalf("AbortRuntimeRun() = (%t, %v), want (true, nil)", applied, err)
			}
			waitForAbortedLifecycleUpsert(t, queries)
			upserts := queries.recordedUpserts()
			if len(upserts) != 1 || !bytes.Equal(upserts[0].Snapshot, wantRaw) {
				t.Fatalf("aborted upserts = %#v, want recovered snapshot %s", upserts, wantRaw)
			}
			waitForLifecycleFailureCount(t, service, tt.wantFailureCount)
		})
	}
}

func TestAbortRuntimeRunMalformedAssistantLifecycleFallsBackToMinimal(t *testing.T) {
	queries := newAbortedLifecycleQueries(t)
	queries.assistantID = flowTestUUID("77777777-7777-4777-8777-777777777777")
	queries.metadata = []byte(`{"context_lifecycle":"invalid"}`)
	service := &Service{
		queries:           queries,
		contextLifecycles: queries,
		abortRuntime:      &recordingAbortRuntime{applied: true},
	}

	applied, err := service.AbortRuntimeRun(
		context.Background(),
		lifecycleTestBotID,
		lifecycleTestSessionID,
		lifecycleTestRunID,
		"abort-malformed-metadata",
	)
	if err != nil || !applied {
		t.Fatalf("AbortRuntimeRun() = (%t, %v), want (true, nil)", applied, err)
	}
	waitForAbortedLifecycleUpsert(t, queries)
	upserts := queries.recordedUpserts()
	minimal, marshalErr := json.Marshal(minimalContextLifecycleSnapshot())
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	if len(upserts) != 1 || !bytes.Equal(upserts[0].Snapshot, minimal) {
		t.Fatalf("aborted upserts = %#v, want minimal fallback %s", upserts, minimal)
	}
	if got := service.contextLifecyclePersistenceErrors.Load(); got != 0 {
		t.Fatalf("persistence failure count = %d, want 0 for non-recoverable metadata", got)
	}
}

func TestAbortRuntimeRunFallsBackToMinimalAfterPendingDecisionGrace(t *testing.T) {
	queries := newAbortedLifecycleQueries(t)
	queries.pending = true
	queries.pendingUntil = 2
	service := &Service{
		queries:           queries,
		contextLifecycles: queries,
		abortRuntime:      &recordingAbortRuntime{applied: true},
	}

	applied, err := service.AbortRuntimeRun(
		context.Background(),
		lifecycleTestBotID,
		lifecycleTestSessionID,
		lifecycleTestRunID,
		"abort-before-context",
	)
	if err != nil || !applied {
		t.Fatalf("AbortRuntimeRun() = (%t, %v), want (true, nil)", applied, err)
	}
	waitForAbortedLifecycleUpsert(t, queries)
	upserts := queries.recordedUpserts()
	if len(upserts) != 1 {
		t.Fatalf("aborted upserts = %d, want 1", len(upserts))
	}
	minimal, err := json.Marshal(minimalContextLifecycleSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(upserts[0].Snapshot, minimal) {
		t.Fatalf("fallback snapshot = %s, want %s", upserts[0].Snapshot, minimal)
	}
	queries.mu.Lock()
	pendingReads := queries.pendingReads
	queries.mu.Unlock()
	if pendingReads < 3 {
		t.Fatalf("pending decision reads = %d, want recheck after pending cleared", pendingReads)
	}
}

func TestAbortRuntimeRunPrefersExistingAuthoritativeSnapshot(t *testing.T) {
	queries := newAbortedLifecycleQueries(t)
	queries.existing = &sqlc.ContextLifecycle{Snapshot: []byte(`{"version":7}`)}
	service := &Service{
		queries:           queries,
		contextLifecycles: queries,
		abortRuntime:      &recordingAbortRuntime{applied: true},
	}

	_, err := service.AbortRuntimeRun(
		context.Background(),
		lifecycleTestBotID,
		lifecycleTestSessionID,
		lifecycleTestRunID,
		"abort-existing",
	)
	if err != nil {
		t.Fatalf("AbortRuntimeRun() error = %v", err)
	}
	waitForAbortedLifecycleUpsert(t, queries)
	upserts := queries.recordedUpserts()
	if len(upserts) != 1 || string(upserts[0].Snapshot) != `{"version":7}` {
		t.Fatalf("aborted upserts = %#v, want existing authoritative snapshot", upserts)
	}
}

func newAbortedLifecycleQueries(t *testing.T) *abortLifecycleQueries {
	t.Helper()
	runID, botID, sessionID, err := parseContextLifecycleIDs(
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	return &abortLifecycleQueries{
		sessionRun: sqlc.SessionRun{
			RunID: runID, BotID: botID, SessionID: sessionID, State: "aborted",
		},
		upsertCh: make(chan struct{}, 1),
	}
}

func waitForAbortedLifecycleUpsert(t *testing.T, queries *abortLifecycleQueries) {
	t.Helper()
	select {
	case <-queries.upsertCh:
	case <-time.After(2 * time.Second):
		t.Fatal("aborted lifecycle reconciliation did not write")
	}
}

func waitForLifecycleFailureCount(t *testing.T, service *Service, want uint64) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for service.contextLifecyclePersistenceErrors.Load() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := service.contextLifecyclePersistenceErrors.Load(); got != want {
		t.Fatalf("persistence failure count = %d, want %d", got, want)
	}
}
