package application

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/settings"
)

const (
	directLifecycleModelID    = "44444444-4444-4444-8444-444444444444"
	directLifecycleProviderID = "55555555-5555-4555-8555-555555555555"
	directLifecyclePrompt     = "PRIVATE_DIRECT_LIFECYCLE_PROMPT"
	directLifecycleResponse   = "PRIVATE_DIRECT_LIFECYCLE_RESPONSE"
)

type directLifecycleQueries struct {
	modelSelectionFakeQueries
	modelID pgtype.UUID
}

func (q *directLifecycleQueries) GetSettingsByBotID(
	_ context.Context,
	botID pgtype.UUID,
) (sqlc.GetSettingsByBotIDRow, error) {
	return sqlc.GetSettingsByBotIDRow{
		BotID:           botID,
		Language:        "auto",
		ReasoningEffort: "medium",
		ChatModelID:     q.modelID,
	}, nil
}

func (*directLifecycleQueries) GetBotByID(
	context.Context,
	pgtype.UUID,
) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{}, pgx.ErrNoRows
}

func (*directLifecycleQueries) ListCompactionArtifactLineageBySession(
	context.Context,
	pgtype.UUID,
) ([]sqlc.BotHistoryMessageCompact, error) {
	return nil, nil
}

type directLifecycleFixture struct {
	service    *Service
	lifecycles *synchronizedLifecycleStore
	messages   *recordingMessageService
	runtime    *lifecycleTurnAdmitter
	started    chan struct{}
}

type synchronizedLifecycleStore struct {
	mu    sync.Mutex
	store recordingContextLifecycleStore
}

func (s *synchronizedLifecycleStore) CreateContextLifecycle(
	ctx context.Context,
	arg sqlc.CreateContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store.existing != nil {
		return sqlc.ContextLifecycle{}, &pgconn.PgError{Code: "23505"}
	}
	return s.store.CreateContextLifecycle(ctx, arg)
}

func (s *synchronizedLifecycleStore) GetContextLifecycleByRunID(
	ctx context.Context,
	runID pgtype.UUID,
) (sqlc.ContextLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.GetContextLifecycleByRunID(ctx, runID)
}

func (s *synchronizedLifecycleStore) GetLatestAssistantContextLifecycleMetadataByRunID(
	ctx context.Context,
	runID pgtype.UUID,
) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.GetLatestAssistantContextLifecycleMetadataByRunID(ctx, runID)
}

func (s *synchronizedLifecycleStore) UpdateAbortedContextLifecycleSnapshot(
	ctx context.Context,
	arg sqlc.UpdateAbortedContextLifecycleSnapshotParams,
) (sqlc.ContextLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, err := s.store.UpdateAbortedContextLifecycleSnapshot(ctx, arg)
	if err == nil && s.store.existing != nil {
		s.store.existing.Snapshot = append([]byte(nil), arg.Snapshot...)
		for i := range s.store.creates {
			if s.store.creates[i].RunID == arg.RunID {
				s.store.creates[i].Snapshot = append([]byte(nil), arg.Snapshot...)
			}
		}
	}
	return row, err
}

func (s *synchronizedLifecycleStore) UpsertAbortedContextLifecycle(
	ctx context.Context,
	arg sqlc.UpsertAbortedContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.UpsertAbortedContextLifecycle(ctx, arg)
}

func (s *synchronizedLifecycleStore) UpsertTerminalContextLifecycle(
	ctx context.Context,
	arg sqlc.UpsertTerminalContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.UpsertTerminalContextLifecycle(ctx, arg)
}

func (s *synchronizedLifecycleStore) ListTerminalSessionRunsNeedingContextLifecycle(
	ctx context.Context,
	batchSize int32,
) ([]sqlc.ListTerminalSessionRunsNeedingContextLifecycleRow, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.store.ListTerminalSessionRunsNeedingContextLifecycle(ctx, batchSize)
}

func (s *synchronizedLifecycleStore) creates() []sqlc.CreateContextLifecycleParams {
	s.mu.Lock()
	defer s.mu.Unlock()
	creates := make([]sqlc.CreateContextLifecycleParams, len(s.store.creates))
	copy(creates, s.store.creates)
	for i := range creates {
		creates[i].Snapshot = append([]byte(nil), creates[i].Snapshot...)
	}
	return creates
}

type directLifecycleModelMode string

const (
	directLifecycleModelSuccess      directLifecycleModelMode = "success"
	directLifecycleModelFailure      directLifecycleModelMode = "failure"
	directLifecycleModelBlock        directLifecycleModelMode = "block"
	directLifecycleModelStallHeaders directLifecycleModelMode = "stall_headers"
)

func newDirectLifecycleFixture(t *testing.T, mode directLifecycleModelMode) directLifecycleFixture {
	t.Helper()

	started := make(chan struct{})
	var startedOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Stream bool `json:"stream"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode model request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if mode == directLifecycleModelStallHeaders && !request.Stream {
			startedOnce.Do(func() { close(started) })
			<-r.Context().Done()
			return
		}
		if mode == directLifecycleModelBlock && request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-block\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"},\"finish_reason\":null}]}\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			startedOnce.Do(func() { close(started) })
			<-r.Context().Done()
			return
		}
		if mode == directLifecycleModelSuccess && request.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"chatcmpl-direct-lifecycle\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%q},\"finish_reason\":null}]}\n\n", directLifecycleResponse)
			_, _ = fmt.Fprint(w, "data: {\"id\":\"chatcmpl-direct-lifecycle\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if mode == directLifecycleModelFailure {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"message":"private provider failure"}}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":    "chatcmpl-direct-lifecycle",
			"model": "direct-lifecycle-model",
			"choices": []map[string]any{{
				"index":         0,
				"finish_reason": "stop",
				"message": map[string]any{
					"role":    "assistant",
					"content": directLifecycleResponse,
				},
			}},
		})
	}))
	t.Cleanup(server.Close)

	provider := modelSelectionProviderRow(
		t,
		directLifecycleProviderID,
		string(models.ClientTypeOpenAICompletions),
		true,
	)
	provider.Config, _ = json.Marshal(map[string]any{
		"api_key":  "test-key",
		"base_url": server.URL,
	})
	model := modelSelectionModelRow(
		t,
		directLifecycleModelID,
		"direct-lifecycle-model",
		provider.ID,
		models.ModelTypeChat,
		true,
	)
	model.Config = []byte(`{"context_window":128000}`)
	queries := &directLifecycleQueries{
		modelSelectionFakeQueries: modelSelectionFakeQueries{
			models:   map[string]sqlc.Model{model.ModelID: model},
			provider: provider,
		},
		modelID: model.ID,
	}
	logger := slog.New(slog.DiscardHandler)
	streamTransport, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("httptest transport = %T, want *http.Transport", server.Client().Transport)
	}
	nonStreamingTransport := streamTransport.Clone()
	nonStreamingTransport.ResponseHeaderTimeout = 50 * time.Millisecond
	lifecycles := &synchronizedLifecycleStore{}
	messages := &recordingMessageService{}
	runtime := &lifecycleTurnAdmitter{admission: lifecycleTestAdmission()}
	service := &Service{
		agent:                  native.New(native.Deps{Logger: logger}),
		modelsService:          models.NewService(logger, queries),
		queries:                queries,
		contextLifecycles:      lifecycles,
		messageService:         messages,
		settingsService:        settings.NewService(logger, queries, nil, nil),
		sessionRuntime:         runtime,
		streamHTTPClient:       &http.Client{Transport: streamTransport},
		nonStreamingHTTPClient: &http.Client{Transport: nonStreamingTransport, Timeout: time.Second},
		logger:                 logger,
	}
	return directLifecycleFixture{
		service:    service,
		lifecycles: lifecycles,
		messages:   messages,
		runtime:    runtime,
		started:    started,
	}
}

func TestDirectChatWithoutCallerDeadlineUsesBoundedNonStreamingClient(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelStallHeaders)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		t.Fatal("test caller context unexpectedly has a deadline")
	}

	done := make(chan error, 1)
	go func() {
		_, err := fixture.service.Chat(ctx, ChatRequest{
			BotID:                lifecycleTestBotID,
			ChatID:               lifecycleTestBotID,
			ThreadID:             lifecycleTestSessionID,
			Query:                directLifecyclePrompt,
			UserMessagePersisted: true,
		})
		done <- err
	}()

	select {
	case <-fixture.started:
	case <-time.After(time.Second):
		cancel()
		<-done
		t.Fatal("provider did not receive synchronous Chat request")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Chat() error = nil, want response-header timeout")
		}
		if !strings.Contains(err.Error(), "timeout awaiting response headers") {
			t.Fatalf("Chat() error = %v, want response-header timeout", err)
		}
	case <-time.After(500 * time.Millisecond):
		cancel()
		<-done
		t.Fatal("Chat() remained blocked after the bounded response-header window")
	}
}

func TestDirectChatCallerDeadlineWinsOverNonStreamingClientTimeout(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelStallHeaders)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := fixture.service.Chat(ctx, ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Chat() error = %v, want caller context deadline exceeded", err)
	}
}

func assertDirectLifecycle(
	t *testing.T,
	store *synchronizedLifecycleStore,
	wantRunID, wantStatus, wantAssistantID string,
) {
	t.Helper()
	creates := store.creates()
	if len(creates) != 1 {
		t.Fatalf("lifecycle creates = %d, want 1", len(creates))
	}
	row := creates[0]
	if got := pgUUIDString(row.RunID); got != wantRunID {
		t.Fatalf("lifecycle run ID = %q, want %q", got, wantRunID)
	}
	if row.Status != wantStatus {
		t.Fatalf("lifecycle status = %q, want %q", row.Status, wantStatus)
	}
	if bytes.Contains(row.Snapshot, []byte(directLifecyclePrompt)) ||
		bytes.Contains(row.Snapshot, []byte(directLifecycleResponse)) {
		t.Fatalf("content-light snapshot leaked run content: %s", row.Snapshot)
	}
	var snapshot contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(row.Snapshot, &snapshot); err != nil {
		t.Fatalf("decode lifecycle snapshot: %v", err)
	}
	if snapshot.Version != 1 || snapshot.View == "" || snapshot.AssistantMessageID != wantAssistantID {
		t.Fatalf("lifecycle snapshot = %#v, want authoritative version 1 and assistant %q", snapshot, wantAssistantID)
	}
}

func TestDirectChatMintsRunAndPersistsCompletedLifecycleAfterAssistant(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)

	response, err := fixture.service.Chat(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}
	if len(response.Messages) == 0 || len(fixture.messages.persisted) == 0 {
		t.Fatal("Chat() did not persist its assistant response")
	}
	runID := fixture.messages.persisted[len(fixture.messages.persisted)-1].RunID
	if _, err := uuid.Parse(runID); err != nil {
		t.Fatalf("persisted direct run ID = %q, want UUID: %v", runID, err)
	}
	assertDirectLifecycle(t, fixture.lifecycles, runID, contextLifecycleStatusCompleted, "message-id")
}

func TestDirectChatProviderFailurePersistsFailedProviderLifecycle(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelFailure)

	_, err := fixture.service.Chat(context.Background(), ChatRequest{
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	})
	if err == nil {
		t.Fatal("Chat() error = nil, want provider failure")
	}
	creates := fixture.lifecycles.creates()
	if len(creates) != 1 {
		t.Fatalf("lifecycle creates = %d, want 1", len(creates))
	}
	runID := pgUUIDString(creates[0].RunID)
	if _, parseErr := uuid.Parse(runID); parseErr != nil {
		t.Fatalf("failed direct run ID = %q, want UUID: %v", runID, parseErr)
	}
	assertDirectLifecycle(t, fixture.lifecycles, runID, contextLifecycleStatusFailedProvider, "")
	if creates[0].ErrorCode.Valid {
		t.Fatalf("private provider diagnostic became stable error code: %#v", creates[0].ErrorCode)
	}
}

func TestAdmittedStreamCancellationPersistsAbortedLifecycle(t *testing.T) {
	fixture := newDirectLifecycleFixture(t, directLifecycleModelBlock)

	handle, err := fixture.service.StartTurn(context.Background(), turn.StartTurnCommand{
		SchemaVersion:        1,
		TeamID:               "direct-lifecycle-team",
		Mode:                 turn.ModeChat,
		BotID:                lifecycleTestBotID,
		ChatID:               lifecycleTestBotID,
		ThreadID:             lifecycleTestSessionID,
		Query:                directLifecyclePrompt,
		UserMessagePersisted: true,
	})
	if err != nil {
		t.Fatalf("StartTurn() error = %v", err)
	}
	select {
	case <-fixture.started:
	case <-time.After(3 * time.Second):
		t.Fatal("streaming provider did not start")
	}
	handle.Cancel()
	done := make(chan struct{})
	go func() {
		for range handle.Events() {
		}
		for range handle.Errs() {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("canceled admitted stream did not finish")
	}

	assertDirectLifecycle(t, fixture.lifecycles, lifecycleTestRunID, contextLifecycleStatusAborted, "")
	if len(fixture.runtime.finishes) != 1 || fixture.runtime.finishes[0].status != sessionruntime.RunStatusAborted {
		t.Fatalf("runtime finishes = %#v, want one aborted finish", fixture.runtime.finishes)
	}
}
