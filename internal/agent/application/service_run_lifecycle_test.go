package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

const (
	lifecycleTestRunID     = "11111111-1111-4111-8111-111111111111"
	lifecycleTestBotID     = "22222222-2222-4222-8222-222222222222"
	lifecycleTestSessionID = "33333333-3333-4333-8333-333333333333"
)

type recordingContextLifecycleStore struct {
	creates             []sqlc.CreateContextLifecycleParams
	createErr           error
	existing            *sqlc.ContextLifecycle
	getErr              error
	getCalls            int
	updates             []sqlc.UpdateAbortedContextLifecycleSnapshotParams
	updateErr           error
	assistantID         pgtype.UUID
	metadata            []byte
	metadataErr         error
	upserts             []sqlc.UpsertAbortedContextLifecycleParams
	upsertErr           error
	terminalUpserts     []sqlc.UpsertTerminalContextLifecycleParams
	terminalUpsertErr   error
	terminalUpsertBound bool
	terminalRows        []sqlc.ListTerminalSessionRunsNeedingContextLifecycleRow
	terminalListErr     error
	terminalListCalls   int
	terminalListLimit   int32
	terminalListBound   bool
	terminalListWait    bool
}

func (s *recordingContextLifecycleStore) CreateContextLifecycle(
	_ context.Context,
	arg sqlc.CreateContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	s.creates = append(s.creates, arg)
	if s.createErr != nil {
		return sqlc.ContextLifecycle{}, s.createErr
	}
	created := sqlc.ContextLifecycle{
		RunID: arg.RunID, BotID: arg.BotID, SessionID: arg.SessionID,
		Status: arg.Status, ErrorCode: arg.ErrorCode, Snapshot: arg.Snapshot,
	}
	s.existing = &created
	return created, nil
}

type lifecycleTurnAdmitter struct {
	admission sessionruntime.Admission
	cancel    context.CancelFunc
	finishes  []recordedFinish
	finishErr error
}

func (a *lifecycleTurnAdmitter) Admit(
	_ context.Context,
	input sessionruntime.AdmitInput,
) (sessionruntime.Admission, error) {
	a.cancel = input.Execution.Cancel
	return a.admission, nil
}

func (a *lifecycleTurnAdmitter) FinishRun(
	_ context.Context,
	handle sessionruntime.RunHandle,
	status, message string,
) error {
	a.finishes = append(a.finishes, recordedFinish{handle: handle, status: status, message: message})
	return a.finishErr
}

func (a *lifecycleTurnAdmitter) FinishRunWithErrorCode(
	_ context.Context,
	handle sessionruntime.RunHandle,
	status, code string,
) error {
	a.finishes = append(a.finishes, recordedFinish{handle: handle, status: status, message: code})
	return a.finishErr
}

func (s *recordingContextLifecycleStore) GetContextLifecycleByRunID(
	_ context.Context,
	_ pgtype.UUID,
) (sqlc.ContextLifecycle, error) {
	s.getCalls++
	if s.getErr != nil {
		return sqlc.ContextLifecycle{}, s.getErr
	}
	if s.existing == nil {
		return sqlc.ContextLifecycle{}, pgx.ErrNoRows
	}
	return *s.existing, nil
}

func (s *recordingContextLifecycleStore) GetLatestAssistantContextLifecycleMetadataByRunID(
	_ context.Context,
	_ pgtype.UUID,
) ([]byte, error) {
	return s.metadata, s.metadataErr
}

func (s *recordingContextLifecycleStore) GetLatestAssistantContextLifecycleByRunID(
	_ context.Context,
	_ pgtype.UUID,
) (sqlc.GetLatestAssistantContextLifecycleByRunIDRow, error) {
	if s.metadataErr != nil {
		return sqlc.GetLatestAssistantContextLifecycleByRunIDRow{}, s.metadataErr
	}
	if s.metadata == nil {
		return sqlc.GetLatestAssistantContextLifecycleByRunIDRow{}, pgx.ErrNoRows
	}
	return sqlc.GetLatestAssistantContextLifecycleByRunIDRow{
		ID:       s.assistantID,
		Metadata: s.metadata,
	}, nil
}

func (s *recordingContextLifecycleStore) UpdateAbortedContextLifecycleSnapshot(
	_ context.Context,
	arg sqlc.UpdateAbortedContextLifecycleSnapshotParams,
) (sqlc.ContextLifecycle, error) {
	s.updates = append(s.updates, arg)
	return sqlc.ContextLifecycle{}, s.updateErr
}

func (s *recordingContextLifecycleStore) UpsertAbortedContextLifecycle(
	_ context.Context,
	arg sqlc.UpsertAbortedContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	s.upserts = append(s.upserts, arg)
	return sqlc.ContextLifecycle{}, s.upsertErr
}

func (s *recordingContextLifecycleStore) UpsertTerminalContextLifecycle(
	ctx context.Context,
	arg sqlc.UpsertTerminalContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	_, s.terminalUpsertBound = ctx.Deadline()
	s.terminalUpserts = append(s.terminalUpserts, arg)
	if s.terminalUpsertErr != nil {
		return sqlc.ContextLifecycle{}, s.terminalUpsertErr
	}
	upserted := sqlc.ContextLifecycle{
		RunID: arg.RunID, BotID: arg.BotID, SessionID: arg.SessionID,
		Status: arg.Status, ErrorCode: arg.ErrorCode, Snapshot: arg.Snapshot,
	}
	s.existing = &upserted
	return upserted, nil
}

func (s *recordingContextLifecycleStore) ListTerminalSessionRunsNeedingContextLifecycle(
	ctx context.Context,
	batchSize int32,
) ([]sqlc.ListTerminalSessionRunsNeedingContextLifecycleRow, error) {
	s.terminalListCalls++
	s.terminalListLimit = batchSize
	_, s.terminalListBound = ctx.Deadline()
	if s.terminalListWait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return append([]sqlc.ListTerminalSessionRunsNeedingContextLifecycleRow(nil), s.terminalRows...), s.terminalListErr
}

func lifecycleTestRunConfig() native.RunConfig {
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{
		View: contextfrag.ViewRunConfigPreProvider,
		Counts: contextfrag.ManifestCounts{
			Fragments: 2,
			Messages:  1,
			TextBytes: 64,
		},
		Items: []contextfrag.ManifestItem{{ID: "private-content-marker"}},
	})
	return native.RunConfig{
		RunID: lifecycleTestRunID,
		Identity: native.SessionContext{
			BotID:     lifecycleTestBotID,
			SessionID: lifecycleTestSessionID,
		},
		ContextLifecycle: holder,
	}
}

func TestContextLifecycleTerminalWritesAdmittedRunExactlyOnce(t *testing.T) {
	store := &recordingContextLifecycleStore{}
	service := &Service{contextLifecycles: store}
	terminal := service.contextLifecycleTerminal(context.Background(), lifecycleTestRunConfig())

	terminal(nil)
	terminal(apperror.New(apperror.CodeWorkspaceUnreachable, nil))

	if len(store.creates) != 1 {
		t.Fatalf("CreateContextLifecycle calls = %d, want 1", len(store.creates))
	}
	got := store.creates[0]
	if pgUUIDString(got.RunID) != lifecycleTestRunID {
		t.Fatalf("persisted run ID = %q, want %q", pgUUIDString(got.RunID), lifecycleTestRunID)
	}
	if got.Status != contextLifecycleStatusCompleted || got.ErrorCode.Valid {
		t.Fatalf("terminal = (%q, %#v), want completed without error code", got.Status, got.ErrorCode)
	}
	if bytes.Contains(got.Snapshot, []byte("private-content-marker")) || bytes.Contains(got.Snapshot, []byte(`"items"`)) {
		t.Fatalf("content-light snapshot leaked manifest items: %s", got.Snapshot)
	}
}

func TestContextLifecycleTerminalClassifiesOnlyCurrentStackOutcomes(t *testing.T) {
	canceledCtx, cancel := context.WithCancelCause(context.Background())
	cancel(context.Canceled)

	tests := []struct {
		name      string
		ctx       context.Context
		cause     error
		status    string
		errorCode string
	}{
		{name: "completed", ctx: context.Background(), status: contextLifecycleStatusCompleted},
		{
			name:      "provider failure with stable code",
			ctx:       context.Background(),
			cause:     apperror.New(apperror.CodeWorkspaceUnreachable, nil),
			status:    contextLifecycleStatusFailedProvider,
			errorCode: string(apperror.CodeWorkspaceUnreachable),
		},
		{
			name:   "provider cancellation while owner remains active",
			ctx:    context.Background(),
			cause:  context.Canceled,
			status: contextLifecycleStatusFailedProvider,
		},
		{
			name:   "explicit abort",
			ctx:    canceledCtx,
			cause:  context.Canceled,
			status: contextLifecycleStatusAborted,
		},
		{
			name:   "explicit abort behind private application cause",
			ctx:    canceledCtx,
			cause:  apperror.Wrap(apperror.CodeWorkspaceUnreachable, context.Canceled, nil),
			status: contextLifecycleStatusAborted,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, code := classifyContextLifecycleTerminal(tt.ctx, tt.cause)
			if status != tt.status || code != tt.errorCode {
				t.Fatalf("classifyContextLifecycleTerminal() = (%q, %q), want (%q, %q)", status, code, tt.status, tt.errorCode)
			}
		})
	}
}

func TestEnsureTerminalContextLifecycleCreatesMinimalFallbackOnlyWhenMissing(t *testing.T) {
	store := &recordingContextLifecycleStore{}
	service := &Service{contextLifecycles: store}
	cause := apperror.New(apperror.CodeWorkspaceUnreachable, nil)

	service.EnsureTerminalContextLifecycle(
		context.Background(),
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		cause,
	)

	if store.getCalls != 1 || len(store.creates) != 1 {
		t.Fatalf("fallback calls = get %d, create %d; want 1, 1", store.getCalls, len(store.creates))
	}
	row := store.creates[0]
	if row.Status != contextLifecycleStatusFailedProvider || row.ErrorCode.String != string(apperror.CodeWorkspaceUnreachable) {
		t.Fatalf("fallback terminal = (%q, %#v)", row.Status, row.ErrorCode)
	}
	var snapshot contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
		t.Fatalf("decode fallback snapshot: %v", err)
	}
	if snapshot.Version != 1 || snapshot.Counts != (contextfrag.ManifestCounts{}) {
		t.Fatalf("minimal fallback snapshot = %#v", snapshot)
	}

	store.creates = nil
	runUUID, botUUID, sessionUUID, err := parseContextLifecycleIDs(
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	store.existing = &sqlc.ContextLifecycle{RunID: runUUID, BotID: botUUID, SessionID: sessionUUID}
	service.EnsureTerminalContextLifecycle(
		context.Background(),
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		cause,
	)
	if len(store.creates) != 0 {
		t.Fatalf("existing authoritative lifecycle was replaced: %#v", store.creates)
	}
}

func TestContextLifecycleStoreFailureIsCountedAndDoesNotEscape(t *testing.T) {
	store := &recordingContextLifecycleStore{createErr: errors.New("database unavailable")}
	service := &Service{contextLifecycles: store}

	service.contextLifecycleTerminal(context.Background(), lifecycleTestRunConfig())(nil)

	if got := service.contextLifecyclePersistenceErrors.Load(); got != 1 {
		t.Fatalf("persistence failure count = %d, want 1", got)
	}
}

func TestAuthoritativeSnapshotReplacesOnlyRecoveredAbortedFallback(t *testing.T) {
	runUUID, botUUID, sessionUUID, err := parseContextLifecycleIDs(
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	store := &recordingContextLifecycleStore{
		createErr: &pgconn.PgError{Code: "23505"},
		existing: &sqlc.ContextLifecycle{
			RunID: runUUID, BotID: botUUID, SessionID: sessionUUID,
			Status: contextLifecycleStatusAborted,
		},
	}
	service := &Service{contextLifecycles: store}

	service.contextLifecycleTerminal(context.Background(), lifecycleTestRunConfig())(nil)

	if len(store.updates) != 1 {
		t.Fatalf("aborted snapshot updates = %d, want 1", len(store.updates))
	}
}

func TestTurnRunFinisherCreatesFallbackOnlyAfterFencedTerminalFinish(t *testing.T) {
	abortedCtx, cancel := context.WithCancelCause(context.Background())
	cancel(context.Canceled)
	tests := []struct {
		name        string
		ctx         context.Context
		status      string
		cause       error
		finishErr   error
		wantCreates int
		wantStatus  string
	}{
		{
			name:        "pre-context failure",
			ctx:         context.Background(),
			status:      sessionruntime.RunStatusErrored,
			cause:       apperror.New(apperror.CodeWorkspaceUnreachable, nil),
			wantCreates: 1,
			wantStatus:  contextLifecycleStatusFailedProvider,
		},
		{name: "decision pause", ctx: context.Background()},
		{
			name:        "explicit abort",
			ctx:         abortedCtx,
			status:      sessionruntime.RunStatusAborted,
			wantCreates: 1,
			wantStatus:  contextLifecycleStatusAborted,
		},
		{
			name:      "lost fence",
			ctx:       context.Background(),
			status:    sessionruntime.RunStatusErrored,
			cause:     errors.New("late failure"),
			finishErr: sessionruntime.ErrRunOwnershipLost,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admitter := &lifecycleTurnAdmitter{finishErr: tt.finishErr}
			store := &recordingContextLifecycleStore{}
			service := &Service{sessionRuntime: admitter, contextLifecycles: store}
			admission := lifecycleTestAdmission()

			service.turnRunFinisher(tt.ctx, admission)(tt.status, tt.cause)

			if len(admitter.finishes) != 1 {
				t.Fatalf("FinishRun calls = %d, want 1", len(admitter.finishes))
			}
			if tt.name == "pre-context failure" && admitter.finishes[0].message != string(apperror.CodeWorkspaceUnreachable) {
				t.Fatalf("stable finish code = %q", admitter.finishes[0].message)
			}
			if len(store.creates) != tt.wantCreates {
				t.Fatalf("lifecycle creates = %d, want %d", len(store.creates), tt.wantCreates)
			}
			if tt.wantCreates > 0 && store.creates[0].Status != tt.wantStatus {
				t.Fatalf("lifecycle status = %q, want %q", store.creates[0].Status, tt.wantStatus)
			}
		})
	}
}

func TestSubagentTerminalPersistsSnapshotAndPreContextFallbackExactlyOnce(t *testing.T) {
	tests := []struct {
		name         string
		terminal     func() tools.SubagentTerminal
		wantStatus   string
		wantFinish   string
		wantSnapshot contextfrag.LifecycleSnapshot
	}{
		{
			name: "final snapshot",
			terminal: func() tools.SubagentTerminal {
				snapshot, ok := lifecycleTestRunConfig().ContextLifecycle.Snapshot()
				if !ok {
					t.Fatal("test lifecycle snapshot is unavailable")
				}
				return tools.SubagentTerminal{ContextLifecycle: &snapshot}
			},
			wantStatus:   contextLifecycleStatusCompleted,
			wantFinish:   sessionruntime.RunStatusCompleted,
			wantSnapshot: contextfrag.LifecycleSnapshot{Version: 1},
		},
		{
			name: "failure before context",
			terminal: func() tools.SubagentTerminal {
				return tools.SubagentTerminal{Cause: apperror.New(apperror.CodeWorkspaceUnreachable, nil)}
			},
			wantStatus:   contextLifecycleStatusFailedProvider,
			wantFinish:   sessionruntime.RunStatusErrored,
			wantSnapshot: contextfrag.LifecycleSnapshot{Version: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			admission := lifecycleTestAdmission()
			admitter := &lifecycleTurnAdmitter{admission: admission}
			store := &recordingContextLifecycleStore{}
			service := &Service{sessionRuntime: admitter, contextLifecycles: store}

			_, subagentAdmission, terminal, err := service.AdmitSubagentRun(
				context.Background(),
				lifecycleTestBotID,
				lifecycleTestSessionID,
				"subagent:test",
				[]byte(`{"message":"work"}`),
			)
			if err != nil {
				t.Fatalf("AdmitSubagentRun() error = %v", err)
			}
			if subagentAdmission.RunID != lifecycleTestRunID {
				t.Fatalf("run ID = %q, want %q", subagentAdmission.RunID, lifecycleTestRunID)
			}
			terminal(tt.terminal())
			terminal(tools.SubagentTerminal{Cause: errors.New("late duplicate")})

			if len(store.creates) != 1 || store.creates[0].Status != tt.wantStatus {
				t.Fatalf("lifecycle creates = %#v, want one %s", store.creates, tt.wantStatus)
			}
			var snapshot contextfrag.LifecycleSnapshot
			if err := json.Unmarshal(store.creates[0].Snapshot, &snapshot); err != nil {
				t.Fatalf("decode snapshot: %v", err)
			}
			if snapshot.Version != tt.wantSnapshot.Version {
				t.Fatalf("snapshot = %#v, want version %d", snapshot, tt.wantSnapshot.Version)
			}
			if len(admitter.finishes) != 1 || admitter.finishes[0].status != tt.wantFinish {
				t.Fatalf("runtime finishes = %#v, want one %s", admitter.finishes, tt.wantFinish)
			}
		})
	}
}

func TestCanceledSubagentUsesOwningContextToPersistAborted(t *testing.T) {
	admitter := &lifecycleTurnAdmitter{admission: lifecycleTestAdmission()}
	store := &recordingContextLifecycleStore{}
	service := &Service{sessionRuntime: admitter, contextLifecycles: store}
	runCtx, _, terminal, err := service.AdmitSubagentRun(
		context.Background(),
		lifecycleTestBotID,
		lifecycleTestSessionID,
		"subagent:abort",
		[]byte(`{"message":"work"}`),
	)
	if err != nil {
		t.Fatalf("AdmitSubagentRun() error = %v", err)
	}
	snapshot, ok := lifecycleTestRunConfig().ContextLifecycle.Snapshot()
	if !ok {
		t.Fatal("test lifecycle snapshot is unavailable")
	}

	admitter.cancel()
	<-runCtx.Done()
	terminal(tools.SubagentTerminal{Cause: context.Canceled, ContextLifecycle: &snapshot})

	if len(store.creates) != 1 || store.creates[0].Status != contextLifecycleStatusAborted {
		t.Fatalf("lifecycle creates = %#v, want one aborted row", store.creates)
	}
	if len(admitter.finishes) != 1 || admitter.finishes[0].status != sessionruntime.RunStatusAborted {
		t.Fatalf("runtime finishes = %#v, want one aborted finish", admitter.finishes)
	}
}

func lifecycleTestAdmission() sessionruntime.Admission {
	return sessionruntime.Admission{
		RunID:   lifecycleTestRunID,
		Started: true,
		Handle: sessionruntime.RunHandle{
			RunID:        lifecycleTestRunID,
			BotID:        lifecycleTestBotID,
			SessionID:    lifecycleTestSessionID,
			FencingToken: 1,
		},
	}
}

func TestRecoverContextLifecycleFromAssistantMetadata(t *testing.T) {
	snapshot, ok := lifecycleTestRunConfig().ContextLifecycle.Snapshot()
	if !ok {
		t.Fatal("test lifecycle snapshot is unavailable")
	}
	metadata, err := json.Marshal(map[string]any{
		contextfrag.MetadataContextLifecycleKey: snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	const assistantMessageID = "44444444-4444-4444-8444-444444444444"
	store := &recordingContextLifecycleStore{
		assistantID: flowTestUUID(assistantMessageID),
		metadata:    metadata,
	}
	service := &Service{contextLifecycles: store}

	service.recoverContextLifecycleFromAssistantMetadata(
		context.Background(),
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		apperror.New(apperror.CodeWorkspaceUnreachable, nil),
	)

	if len(store.creates) != 1 {
		t.Fatalf("CreateContextLifecycle calls = %d, want 1", len(store.creates))
	}
	row := store.creates[0]
	if row.Status != contextLifecycleStatusFailedProvider ||
		!row.ErrorCode.Valid || row.ErrorCode.String != string(apperror.CodeWorkspaceUnreachable) {
		t.Fatalf("recovered terminal = (%q, %#v), want failed_provider with stable code", row.Status, row.ErrorCode)
	}
	var recovered contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(row.Snapshot, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.AssistantMessageID != assistantMessageID {
		t.Fatalf("assistant message ID = %q, want %q", recovered.AssistantMessageID, assistantMessageID)
	}
}

func TestRecoverContextLifecycleFromAssistantMetadataSkipsExistingOrUnavailable(t *testing.T) {
	runID, botID, sessionID, err := parseContextLifecycleIDs(
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		store *recordingContextLifecycleStore
	}{
		{
			name: "existing lifecycle",
			store: &recordingContextLifecycleStore{existing: &sqlc.ContextLifecycle{
				RunID: runID, BotID: botID, SessionID: sessionID,
			}},
		},
		{name: "missing assistant metadata", store: &recordingContextLifecycleStore{}},
		{name: "malformed assistant metadata", store: &recordingContextLifecycleStore{metadata: []byte(`{"context_lifecycle":"invalid"}`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			service := &Service{contextLifecycles: tt.store}
			service.recoverContextLifecycleFromAssistantMetadata(
				context.Background(),
				lifecycleTestRunID,
				lifecycleTestBotID,
				lifecycleTestSessionID,
				errors.New("continuation failed"),
			)
			if len(tt.store.creates) != 0 || service.contextLifecyclePersistenceErrors.Load() != 0 {
				t.Fatalf("recovery writes = %d, failures = %d; want no-op", len(tt.store.creates), service.contextLifecyclePersistenceErrors.Load())
			}
		})
	}
}

func TestRecoverContextLifecycleFromAssistantMetadataCountsStoreErrorsAndSkipsOwnershipLoss(t *testing.T) {
	metadataErr := errors.New("metadata unavailable")
	store := &recordingContextLifecycleStore{metadataErr: metadataErr}
	service := &Service{contextLifecycles: store}
	service.recoverContextLifecycleFromAssistantMetadata(
		context.Background(),
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		errors.New("continuation failed"),
	)
	if got := service.contextLifecyclePersistenceErrors.Load(); got != 1 {
		t.Fatalf("persistence failure count = %d, want 1", got)
	}

	ownershipStore := &recordingContextLifecycleStore{}
	service = &Service{contextLifecycles: ownershipStore}
	service.recoverContextLifecycleFromAssistantMetadata(
		context.Background(),
		lifecycleTestRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		sessionruntime.ErrRunOwnershipLost,
	)
	if ownershipStore.getCalls != 0 || len(ownershipStore.creates) != 0 {
		t.Fatalf("ownership-lost recovery touched store: gets=%d creates=%d", ownershipStore.getCalls, len(ownershipStore.creates))
	}
}
