package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	acpagent "github.com/felinics/memoh/internal/agent/runtime/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type closedDiscussAgentStreamer struct{}

func (*closedDiscussAgentStreamer) Stream(context.Context, native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent)
	close(ch)
	return ch
}

func TestNativeDiscussCleanCloseAlignsFailedLifecycleAndRuntime(t *testing.T) {
	resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{ModelID: "model-1"}}
	service := newDiscussTestService(&fakeRunner{}, &closedDiscussAgentStreamer{}, resolver)
	runtime, lifecycles := configureDiscussLifecycle(service)

	handle, err := service.StartTurn(context.Background(), lifecycleDiscussCommand())
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, handle)

	if len(lifecycles.creates) != 1 || lifecycles.creates[0].Status != contextLifecycleStatusFailedProvider {
		t.Fatalf("lifecycle creates = %#v, want one failed_provider row", lifecycles.creates)
	}
	if len(runtime.finishes) != 1 || runtime.finishes[0].status != sessionruntime.RunStatusErrored {
		t.Fatalf("runtime finishes = %#v, want one errored finish", runtime.finishes)
	}
}

type blockingLifecycleCreateStore struct {
	*recordingContextLifecycleStore
	started chan struct{}
	release chan struct{}
}

func (s *blockingLifecycleCreateStore) CreateContextLifecycle(
	ctx context.Context,
	arg sqlc.CreateContextLifecycleParams,
) (sqlc.ContextLifecycle, error) {
	close(s.started)
	<-s.release
	return s.recordingContextLifecycleStore.CreateContextLifecycle(ctx, arg)
}

func TestContentLightDiscussCancellationCannotSplitLifecycleFromRuntime(t *testing.T) {
	tests := []struct {
		name    string
		command func() turn.StartTurnCommand
	}{
		{
			name: "passive",
			command: func() turn.StartTurnCommand {
				cmd := lifecycleDiscussCommand()
				cmd.DiscussAddressed = false
				return cmd
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{RuntimeType: sessionpkg.RuntimeACPAgent}}
			service := newDiscussTestService(&fakeRunner{}, &fakeAgentStreamer{}, resolver)
			runtime, _ := configureDiscussLifecycle(service)
			store := &blockingLifecycleCreateStore{
				recordingContextLifecycleStore: &recordingContextLifecycleStore{},
				started:                        make(chan struct{}),
				release:                        make(chan struct{}),
			}
			service.contextLifecycles = store

			handle, err := service.StartTurn(context.Background(), tt.command())
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-store.started:
			case <-time.After(time.Second):
				t.Fatal("content-light lifecycle write did not start")
			}
			handle.Cancel()
			close(store.release)
			drainDiscuss(t, handle)

			if len(store.creates) != 1 {
				t.Fatalf("lifecycle creates = %#v, want one row", store.creates)
			}
			if len(runtime.finishes) != 1 {
				t.Fatalf("runtime finishes = %#v, want one finish", runtime.finishes)
			}
			lifecycleAborted := store.creates[0].Status == contextLifecycleStatusAborted
			runtimeAborted := runtime.finishes[0].status == sessionruntime.RunStatusAborted
			if lifecycleAborted != runtimeAborted {
				t.Fatalf("lifecycle status %q and runtime status %q disagree", store.creates[0].Status, runtime.finishes[0].status)
			}
		})
	}
}

type failNthPersistMessageService struct {
	*recordingMessageService
	failAt int
	calls  int
	err    error
}

func (s *failNthPersistMessageService) Persist(
	ctx context.Context,
	input messagepkg.PersistInput,
) (messagepkg.Message, error) {
	s.calls++
	if s.calls == s.failAt {
		return messagepkg.Message{}, s.err
	}
	return s.recordingMessageService.Persist(ctx, input)
}

// PersistRound mirrors the per-message failure for the atomic round path: a
// round whose write would cross failAt fails as one transaction.
func (s *failNthPersistMessageService) PersistRound(
	ctx context.Context,
	inputs []messagepkg.PersistInput,
	options messagepkg.RoundPersistenceOptions,
) ([]messagepkg.Message, bool, error) {
	if s.calls+len(inputs) >= s.failAt {
		s.calls = s.failAt
		return nil, true, s.err
	}
	s.calls += len(inputs)
	return s.recordingMessageService.PersistRound(ctx, inputs, options)
}

func TestACPGenericPromptFailurePublishesSanitizedErroredTerminalToChatAndDiscuss(t *testing.T) {
	const privateDetail = "PRIVATE_ACP_PROVIDER_FAILURE"
	pool := &recordingACPPrompter{err: errors.New(privateDetail)}
	lifecycles := &recordingContextLifecycleStore{}
	service := newACPLifecycleService(t, pool, &recordingMessageService{}, lifecycles)
	eventCh := make(chan WSStreamEvent, 16)

	if err := service.streamACPAgentWS(context.Background(), ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
		RunID: lifecycleTestRunID, Query: "inspect",
	}, eventCh, make(chan struct{})); err != nil {
		t.Fatalf("streamACPAgentWS() error = %v", err)
	}
	events := drainAgentEvents(t, eventCh)
	assertACPFailedTerminal(t, events, "runtime_prompt_failed", privateDetail)
	requireACPLifecycle(t, lifecycles, lifecycleTestRunID, contextLifecycleStatusFailedProvider)

	chunks := make([]string, 0, len(events))
	for _, event := range events {
		raw, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, string(raw))
	}
	for _, mode := range []turn.Mode{turn.ModeChat, turn.ModeDiscuss} {
		t.Run(string(mode), func(t *testing.T) {
			runner := &fakeRunner{chunks: chunks}
			var turnService *Service
			cmd := lifecycleDiscussCommand()
			cmd.Mode = mode
			if mode == turn.ModeDiscuss {
				resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{RuntimeType: sessionpkg.RuntimeACPAgent}}
				turnService = newDiscussTestService(runner, &fakeAgentStreamer{}, resolver)
			} else {
				turnService = newTurnTestService(runner)
			}
			admitter := turnService.sessionRuntime.(*scriptedAdmitter)
			handle, err := turnService.StartTurn(context.Background(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			for range handle.Events() {
			}
			for range handle.Errs() {
			}
			assertPublishedErroredTerminal(t, admitter.published)
			if finish := admitter.awaitFinish(t); finish.status != "" {
				t.Fatalf("runtime-derived finish status = %q, want unnamed", finish.status)
			}
		})
	}
}

func TestACPPersistFailureReplacesSuccessTerminalWithSanitizedErrorAbort(t *testing.T) {
	privateErr := errors.New("PRIVATE_ACP_PERSIST_FAILURE")
	messages := &failNthPersistMessageService{
		recordingMessageService: &recordingMessageService{},
		failAt:                  2,
		err:                     privateErr,
	}
	lifecycles := &recordingContextLifecycleStore{}
	service := newACPLifecycleService(
		t,
		&recordingACPPrompter{result: acpclient.PromptResult{Text: "done", StopReason: "end_turn"}},
		messages,
		lifecycles,
	)
	eventCh := make(chan WSStreamEvent, 16)

	if err := service.streamACPAgentWS(context.Background(), ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
		RunID: lifecycleTestRunID, Query: "inspect",
	}, eventCh, make(chan struct{})); err != nil {
		t.Fatalf("streamACPAgentWS() error = %v", err)
	}
	events := drainAgentEvents(t, eventCh)
	assertACPFailedTerminal(t, events, string(apperror.CodeSessionHistoryInconsistent), privateErr.Error())
	row, _ := requireACPLifecycle(t, lifecycles, lifecycleTestRunID, contextLifecycleStatusFailedProvider)
	if !row.ErrorCode.Valid || row.ErrorCode.String != string(apperror.CodeSessionHistoryInconsistent) {
		t.Fatalf("lifecycle error code = %#v, want %q", row.ErrorCode, apperror.CodeSessionHistoryInconsistent)
	}
	// The eagerly persisted leading user message is deliberately KEPT on a
	// definite round rollback: the user watched their message send, a visible
	// message must never vanish, and keeping it can never destroy a committed
	// turn the way a misclassified rollback deletion would. This pin exists
	// because reviewers have proposed both directions; the keep decision is
	// intentional, not an omission.
	for _, batch := range messages.deleted {
		for _, id := range batch {
			if id == "message-id" {
				t.Fatalf("round rollback deleted the leading user message: %#v", messages.deleted)
			}
		}
	}
}

func TestACPExplicitCancellationDoesNotPublishProviderError(t *testing.T) {
	started := make(chan struct{})
	pool := &recordingACPPrompter{promptFn: func(ctx context.Context, _ acpagent.PromptInput) (acpclient.PromptResult, error) {
		close(started)
		<-ctx.Done()
		return acpclient.PromptResult{}, context.Cause(ctx)
	}}
	service := newACPLifecycleService(t, pool, &recordingMessageService{}, &recordingContextLifecycleStore{})
	eventCh := make(chan WSStreamEvent, 16)
	abortCh := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- service.streamACPAgentWS(context.Background(), ChatRequest{
			BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
			RunID: lifecycleTestRunID, Query: "inspect",
		}, eventCh, abortCh)
	}()
	<-started
	close(abortCh)
	if err := <-done; err != nil {
		t.Fatalf("streamACPAgentWS() error = %v", err)
	}
	events := drainAgentEvents(t, eventCh)
	if containsStreamEvent(events, native.EventError) || containsStreamEvent(events, native.EventEnd) {
		t.Fatalf("events = %#v, explicit cancellation must not look completed or provider-failed", events)
	}
}

func assertACPFailedTerminal(t *testing.T, events []native.StreamEvent, wantError, privateDetail string) {
	t.Helper()
	errorIndex, abortIndex := -1, -1
	for index, event := range events {
		switch event.Type {
		case native.EventError:
			if errorIndex < 0 {
				errorIndex = index
				if event.Error != wantError {
					t.Fatalf("runtime error = %q, want %q", event.Error, wantError)
				}
			}
		case native.EventAbort:
			abortIndex = index
		case native.EventEnd:
			t.Fatalf("events = %#v, failed ACP turn published agent_end", events)
		}
	}
	if errorIndex < 0 || abortIndex <= errorIndex {
		t.Fatalf("events = %#v, want error before abort", events)
	}
	raw, err := json.Marshal(events)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), privateDetail) {
		t.Fatalf("runtime events leaked private diagnostic: %s", raw)
	}
}

func assertPublishedErroredTerminal(t *testing.T, events []native.StreamEvent) {
	t.Helper()
	errorSeen := false
	for _, event := range events {
		if event.Type == native.EventError {
			errorSeen = true
		}
		if event.Type == native.EventAbort {
			if !errorSeen {
				t.Fatalf("published events = %#v, abort preceded error", events)
			}
			return
		}
	}
	t.Fatalf("published events = %#v, want error and abort", events)
}

// reconcileOutcomeQueries fakes the commit-unknown reconciliation store: it
// answers LockSessionForCommitReconciliation + GetRuntimeRoundOutcome inside a
// pass-through transaction, can fail a configurable number of attempts first,
// and signals when the background retry's projection cleanup runs.
type reconcileOutcomeQueries struct {
	dbstore.Queries
	mu               sync.Mutex
	outcome          string // "" means no committed round (proven rollback)
	failFirst        int    // reconcile attempts to fail before answering
	projectionsSwept chan struct{}
}

func (*reconcileOutcomeQueries) SupportsTransactions() bool { return true }

func (*reconcileOutcomeQueries) ListCompactionArtifactLineageBySession(context.Context, pgtype.UUID) ([]sqlc.BotHistoryMessageCompact, error) {
	return nil, nil
}

func (q *reconcileOutcomeQueries) InTx(_ context.Context, fn func(dbstore.Queries) error) error {
	q.mu.Lock()
	if q.failFirst > 0 {
		q.failFirst--
		q.mu.Unlock()
		return errors.New("reconciliation store temporarily unavailable")
	}
	q.mu.Unlock()
	return fn(q)
}

// GetBotByID satisfies the incidental timezone lookup on this path.
func (*reconcileOutcomeQueries) GetBotByID(context.Context, pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{}, pgx.ErrNoRows
}

func (*reconcileOutcomeQueries) LockSessionForCommitReconciliation(
	context.Context, sqlc.LockSessionForCommitReconciliationParams,
) (pgtype.UUID, error) {
	return pgtype.UUID{}, nil
}

func (q *reconcileOutcomeQueries) GetRuntimeRoundOutcome(context.Context, sqlc.GetRuntimeRoundOutcomeParams) (string, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.outcome == "" {
		return "", pgx.ErrNoRows
	}
	return q.outcome, nil
}

func (q *reconcileOutcomeQueries) DeleteRuntimeDecisionProjectionsByRun(
	context.Context, sqlc.DeleteRuntimeDecisionProjectionsByRunParams,
) (int64, error) {
	select {
	case q.projectionsSwept <- struct{}{}:
	default:
	}
	return 0, nil
}

// TestACPCommitUnknownResolutionMatrix pins the three-way commit-unknown
// resolution contract of resolveRuntimeRoundPersistFailure through the completed
// -turn path, plus the incomplete-abort rollback path. Two invariants hold in
// every cell: the eagerly persisted leading user message is never deleted,
// and the warm runtime is only discarded on a PROVEN rollback of a completed
// turn.
func TestACPCommitUnknownResolutionMatrix(t *testing.T) {
	assertUserMessageKept := func(t *testing.T, messages *recordingMessageService) {
		t.Helper()
		for _, batch := range messages.deleted {
			for _, id := range batch {
				if id == "message-id" {
					t.Fatalf("leading user message was deleted: %#v", messages.deleted)
				}
			}
		}
	}

	t.Run("committed", func(t *testing.T) {
		messages := &recordingMessageService{roundPersistErr: db.ErrCommitOutcomeUnknown}
		pool := &recordingACPPrompter{result: acpclient.PromptResult{Text: "done", StopReason: "end_turn"}}
		service := newACPLifecycleService(t, pool, messages, &recordingContextLifecycleStore{})
		service.queries = &reconcileOutcomeQueries{outcome: "succeeded"}
		eventCh := make(chan WSStreamEvent, 16)

		if err := service.streamACPAgentWS(context.Background(), ChatRequest{
			BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
			RunID: lifecycleTestRunID, Query: "inspect",
		}, eventCh, make(chan struct{})); err != nil {
			t.Fatalf("streamACPAgentWS() error = %v", err)
		}
		events := drainAgentEvents(t, eventCh)
		if !containsStreamEvent(events, native.EventEnd) {
			t.Fatalf("proven-committed round did not terminate as completed: %#v", events)
		}
		if len(pool.closed) != 0 {
			t.Fatalf("proven-committed round closed the runtime: %v", pool.closed)
		}
		assertUserMessageKept(t, messages)
	})

	t.Run("rolled back", func(t *testing.T) {
		messages := &recordingMessageService{roundPersistErr: db.ErrCommitOutcomeUnknown}
		pool := &recordingACPPrompter{result: acpclient.PromptResult{Text: "done", StopReason: "end_turn"}}
		service := newACPLifecycleService(t, pool, messages, &recordingContextLifecycleStore{})
		service.queries = &reconcileOutcomeQueries{outcome: ""}
		eventCh := make(chan WSStreamEvent, 16)

		if err := service.streamACPAgentWS(context.Background(), ChatRequest{
			BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
			RunID: lifecycleTestRunID, Query: "inspect",
		}, eventCh, make(chan struct{})); err != nil {
			t.Fatalf("streamACPAgentWS() error = %v", err)
		}
		events := drainAgentEvents(t, eventCh)
		if !containsStreamEvent(events, native.EventError) || containsStreamEvent(events, native.EventEnd) {
			t.Fatalf("proven rollback of a completed turn must surface a sanitized error abort: %#v", events)
		}
		if len(pool.closed) != 1 || pool.closed[0] != lifecycleTestSessionID {
			t.Fatalf("proven rollback must discard the diverged warm runtime once: %v", pool.closed)
		}
		assertUserMessageKept(t, messages)
	})

	t.Run("unresolved", func(t *testing.T) {
		messages := &recordingMessageService{roundPersistErr: db.ErrCommitOutcomeUnknown}
		pool := &recordingACPPrompter{result: acpclient.PromptResult{Text: "done", StopReason: "end_turn"}}
		service := newACPLifecycleService(t, pool, messages, &recordingContextLifecycleStore{})
		queries := &reconcileOutcomeQueries{
			outcome:          "succeeded",
			failFirst:        1, // the in-turn reconcile fails; the background retry succeeds
			projectionsSwept: make(chan struct{}, 1),
		}
		service.queries = queries
		eventCh := make(chan WSStreamEvent, 16)

		err := service.streamACPAgentWS(context.Background(), ChatRequest{
			BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
			RunID: lifecycleTestRunID, Query: "inspect",
		}, eventCh, make(chan struct{}))
		if err == nil || apperror.CodeOf(err) != apperror.CodeSessionHistoryInconsistent {
			t.Fatalf("unresolved outcome must fail closed, got %v", err)
		}
		if len(pool.closed) != 0 {
			t.Fatalf("unresolved outcome must not touch the runtime: %v", pool.closed)
		}
		assertUserMessageKept(t, messages)
		// The background reconciliation owns the deferred projection sweep.
		select {
		case <-queries.projectionsSwept:
		case <-time.After(5 * time.Second):
			t.Fatal("background reconciliation never swept the projections")
		}
		assertUserMessageKept(t, messages)
	})

	t.Run("incomplete abort rollback keeps runtime", func(t *testing.T) {
		messages := &recordingMessageService{roundPersistErr: errors.New("definite rollback")}
		started := make(chan struct{})
		pool := &recordingACPPrompter{promptFn: func(ctx context.Context, _ acpagent.PromptInput) (acpclient.PromptResult, error) {
			close(started)
			<-ctx.Done()
			return acpclient.PromptResult{Text: "partial"}, context.Cause(ctx)
		}}
		service := newACPLifecycleService(t, pool, messages, &recordingContextLifecycleStore{})
		service.queries = &reconcileOutcomeQueries{}
		eventCh := make(chan WSStreamEvent, 16)
		abortCh := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- service.streamACPAgentWS(context.Background(), ChatRequest{
				BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
				RunID: lifecycleTestRunID, Query: "inspect",
			}, eventCh, abortCh)
		}()
		<-started
		close(abortCh)
		if err := <-done; err != nil {
			t.Fatalf("streamACPAgentWS() error = %v", err)
		}
		if len(pool.closed) != 0 {
			t.Fatalf("incomplete abort must keep the runtime (head never needed to move): %v", pool.closed)
		}
		assertUserMessageKept(t, messages)
	})
}
