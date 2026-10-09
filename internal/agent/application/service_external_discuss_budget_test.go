package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/runtimefence"
)

type externalDiscussQueries struct{ *recoveryHistoryQueries }

func (*externalDiscussQueries) GetBotByID(_ context.Context, id pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{ID: id}, nil
}

func FuzzExternalDiscussFinalEnvelope(f *testing.F) {
	f.Add([]byte("history and current"), uint16(1000), uint8(0))
	f.Add([]byte("images"), uint16(4000), uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, limit uint16, images uint8) {
		if len(data) > 512 {
			return
		}
		messages := make([]turn.DiscussMessage, len(data))
		for i, n := range data {
			messages[i] = turn.DiscussMessage{Role: "user", Content: strings.Repeat("群", int(n)%19)}
		}
		markdown := strings.Repeat("runtime context\n", len(data)%37)
		imageCount, budget := int(images%2), int(limit)+1
		admitted, decision := admitDiscussAgentContext(messages, budget, len(markdown), imageCount)
		if decision.ProtectedOverflow {
			return
		}
		actual := turn.EstimateTokensFromBytes(len(discussAgentFullContextPrompt(admitted))+len(markdown)) + imageCount*contextfrag.EstimateImageTokens
		if actual > budget || actual != decision.SelectedTokens {
			t.Fatalf("final envelope=%d selected=%d budget=%d", actual, decision.SelectedTokens, budget)
		}
	})
}

func TestExternalDiscussAdmissionIncludesRuntimeContext(t *testing.T) {
	service := &Service{}
	service.SetContextAbsoluteMaxTokens(1000)
	messages := make([]turn.DiscussMessage, 1000)
	for i := range messages {
		messages[i] = turn.DiscussMessage{Role: "user", Content: "abcd"}
	}
	messages[0] = turn.DiscussMessage{Role: "user", Content: "old summary", CompactionArtifactID: "summary"}
	messages[len(messages)-1].Content = "CURRENT"
	sections := buildRuntimeContextSections(runtimeContextRenderInput{})
	markdown, _, _ := runtimeContextViaContextView(t.Context(), nil, sections, "")
	got, err := service.prepareExternalDiscussContext(t.Context(), ChatRequest{discussMessages: messages}, markdown, 0)
	if err != nil {
		t.Fatal(err)
	}
	actual := turn.EstimateTokensFromBytes(len(got.Query) + len(markdown))
	if actual > 1000 {
		t.Fatalf("final external envelope=%d, budget=1000", actual)
	}
	if !strings.Contains(got.Query, "old summary") || !strings.Contains(got.Query, "CURRENT") {
		t.Fatal("lost protected sources")
	}
}

func TestExternalDiscussDriverReceivesBoundedFinalEnvelope(t *testing.T) {
	pool := &recordingACPPrompter{result: acpclient.PromptResult{Text: "done", StopReason: "end_turn"}}
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, pool, messages, nil)
	service.SetACPSessionPool(pool)
	service.SetContextAbsoluteMaxTokens(1000)
	sources := make([]turn.DiscussMessage, 1000)
	for i := range sources {
		sources[i] = turn.DiscussMessage{Role: "user", Content: "abcd"}
	}
	sources[len(sources)-1].Content = "CURRENT"
	chunks, errs := service.StreamChat(t.Context(), ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, Query: "candidate", UserMessagePersisted: true,
		discussMessages: sources,
	})
	drainStreamChunks(t, chunks)
	for err := range errs {
		t.Fatal(err)
	}
	actual := turn.EstimateTokensFromBytes(len(pool.input.Prompt) + len(pool.input.ContextMarkdown))
	if pool.calls != 1 || actual > 1000 || !strings.Contains(pool.input.Prompt, "CURRENT") {
		t.Fatalf("external dispatch escaped admission: calls=%d tokens=%d", pool.calls, actual)
	}
}

func TestExternalDiscussOversizedBatchCompactsOlderInputOrRecordsOmission(t *testing.T) {
	var messages []turn.DiscussMessage
	for _, id := range []string{"a", "b", "c", "d"} {
		messages = append(messages, turn.DiscussMessage{Role: "user", Content: id + strings.Repeat("x", 1200), Source: &turn.ContextMessageSource{Kind: "external", ID: id, Current: true}})
	}
	for _, mode := range []string{"shadow", "off"} {
		service, compactor := newControllerPolicyService(t, nil)
		service.SetContextAbsoluteMaxTokens(1000)
		service.SetSyncCompactionMode(mode)
		got, err := service.prepareExternalDiscussContext(t.Context(), ChatRequest{BotID: syncCompactBotID, ThreadID: syncCompactThreadID, discussMessages: messages}, "", 0)
		if mode == "shadow" {
			if !errors.Is(err, native.ErrContextRecompose) || len(compactor.configs) != 1 {
				t.Fatalf("older input must be compacted: err=%v compactions=%d", err, len(compactor.configs))
			}
			for _, source := range compactor.configs[0].ProtectedSources {
				if source.ID == "a" {
					t.Fatalf("the oldest input was protected from compaction: %+v", compactor.configs[0].ProtectedSources)
				}
			}
			continue
		}
		if err != nil || len(got.discussOmittedSources) == 0 || got.discussOmittedSources[0].ID != "a" || !strings.Contains(got.Query, "d"+strings.Repeat("x", 1200)) {
			t.Fatalf("without recovery the newest input must proceed and the omission must be recorded: err=%v omitted=%+v", err, got.discussOmittedSources)
		}
		ledger := contextfrag.NewMutationLedger()
		recordOmittedCurrentInput(ledger, got.discussOmittedSources)
		if records := ledger.Records(); len(records) != 1 || records[0].Kind != contextfrag.MutationCurrentInputOmitted || !strings.HasPrefix(records[0].Detail, "sources=a") {
			t.Fatalf("omission record=%+v", records)
		}
	}
}

type externalResumeQueries struct {
	*externalDiscussQueries
	saved *resumeSaveQueries
}

func (externalResumeQueries) DeleteRuntimeDecisionProjectionsByRun(context.Context, sqlc.DeleteRuntimeDecisionProjectionsByRunParams) (int64, error) {
	return 0, nil
}

func (q externalResumeQueries) SaveSessionRunResumeContext(ctx context.Context, arg sqlc.SaveSessionRunResumeContextParams) (int64, error) {
	return q.saved.SaveSessionRunResumeContext(ctx, arg)
}

func externalResumeService(t *testing.T) (*Service, *recordingACPPrompter, *resumeSaveQueries, context.Context) {
	t.Helper()
	pool := &recordingACPPrompter{result: acpclient.PromptResult{Text: "done", StopReason: "end_turn"}}
	service := newACPLifecycleService(t, pool, &recordingMessageService{}, nil)
	service.SetACPSessionPool(pool)
	service.SetContextAbsoluteMaxTokens(1000)
	saved := &resumeSaveQueries{}
	policy, _ := newControllerPolicyService(t, nil)
	service.queries = externalResumeQueries{externalDiscussQueries: &externalDiscussQueries{&recoveryHistoryQueries{policy.queries.(*controllerQueries)}}, saved: saved}
	service.resumeSecret = "resume-test-secret"
	ctx := runtimefence.WithContext(t.Context(), runtimefence.Fence{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, Token: 7})
	return service, pool, saved, ctx
}

func externalOversizedDiscuss() []turn.DiscussMessage {
	sources := make([]turn.DiscussMessage, 1000)
	for i := range sources {
		sources[i] = turn.DiscussMessage{Role: "user", Content: "abcd"}
	}
	sources[len(sources)-1].Content = "CURRENT"
	return sources
}

// resumedRequest is the request the resume worker builds from a saved
// context.
func resumedRequest(t *testing.T, saved []byte) ChatRequest {
	t.Helper()
	const teamID = "00000000-0000-0000-0000-000000000001"
	runner := &fakeRunner{chunks: []string{`{"type":"agent_end"}`}}
	s, _ := newAdmittedTurnTestService(runner)
	s.SetAllowedTeam(teamID)
	s.logger = slog.New(slog.DiscardHandler)
	raw, err := json.Marshal(map[string]json.RawMessage{"resume": saved})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.resumeInterruptedSession(t.Context(), sqlc.SessionRun{
		TeamID: db.ParseUUIDOrEmpty(teamID), RunID: db.ParseUUIDOrEmpty(uuid.NewString()),
		BotID: db.ParseUUIDOrEmpty(lifecycleTestBotID), SessionID: db.ParseUUIDOrEmpty(lifecycleTestSessionID), InputJson: raw,
	})
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	<-done
	return runner.gotReq
}

// A graceful shutdown can interrupt the turn before final admission or after
// dispatch. Either saved context resumes by admitting the saved batch again
// behind the resume instruction, so the resumed prompt fits the budget and
// still carries the current input.
func TestExternalDiscussResumeAdmitsEverySavedBatchAgain(t *testing.T) {
	service, pool, saved, ctx := externalResumeService(t)
	chunks, errs := service.StreamChat(ctx, ChatRequest{
		BotID: lifecycleTestBotID, ChatID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: uuid.NewString(),
		Query: discussAgentFullContextPrompt(externalOversizedDiscuss()), UserMessagePersisted: true, discussMessages: externalOversizedDiscuss(),
	})
	drainStreamChunks(t, chunks)
	for err := range errs {
		t.Fatal(err)
	}
	if pool.calls != 1 || len(saved.history) != 2 {
		t.Fatalf("dispatches=%d resume saves=%d, want one dispatch saved before and after final admission", pool.calls, len(saved.history))
	}
	dispatched := pool.input.Prompt
	for i, save := range saved.history {
		calls := pool.calls
		chunks, errs := service.StreamChat(ctx, resumedRequest(t, save.ResumeContext))
		drainStreamChunks(t, chunks)
		for err := range errs {
			t.Fatalf("save %d: resume failed: %v", i, err)
		}
		prompt := pool.input.Prompt
		if pool.calls != calls+1 || !strings.HasPrefix(prompt, resumeInstruction) || !strings.Contains(prompt, "CURRENT") ||
			turn.EstimateTokensFromBytes(len(prompt)) > 1000 {
			t.Fatalf("save %d: dispatches=%d prompt=%d bytes, want one resumed prompt within the budget carrying the current input", i, pool.calls-calls, len(prompt))
		}
		if body := strings.TrimPrefix(strings.TrimPrefix(prompt, resumeInstruction), discussAgentPromptPrefix); i == len(saved.history)-1 && !strings.HasSuffix(dispatched, body) {
			t.Fatal("the resume after dispatch replayed input the dispatched prompt did not carry")
		}
	}
}

// A batch that cannot fit even alone fails boundedly on resume, as it did
// before the shutdown.
func TestExternalDiscussResumeOfIrreducibleBatchFailsBounded(t *testing.T) {
	service, pool, saved, ctx := externalResumeService(t)
	irreducible := []turn.DiscussMessage{{Role: "user", Content: strings.Repeat("c", 4400)}}
	chunks, errs := service.StreamChat(ctx, ChatRequest{
		BotID: lifecycleTestBotID, ChatID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: uuid.NewString(),
		Query: discussAgentFullContextPrompt(irreducible), UserMessagePersisted: true, discussMessages: irreducible,
	})
	drainStreamChunks(t, chunks)
	for range errs {
	}
	if pool.calls != 0 || len(saved.history) != 1 {
		t.Fatalf("dispatches=%d resume saves=%d, want the pre-admission save only", pool.calls, len(saved.history))
	}
	chunks, errs = service.StreamChat(ctx, resumedRequest(t, saved.history[0].ResumeContext))
	drainStreamChunks(t, chunks)
	var failure error
	for err := range errs {
		failure = err
	}
	if apperror.CodeOf(failure) != apperror.CodeContextProtectedOverflow || pool.calls != 0 {
		t.Fatalf("irreducible resume: err=%v dispatches=%d, want a bounded protected_overflow", failure, pool.calls)
	}
}

func TestExternalShutdownResumeBoundsInstructionPromptAndRuntimeContext(t *testing.T) {
	service, pool, _, ctx := externalResumeService(t)
	chunks, errs := service.StreamChat(ctx, ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, RunID: uuid.NewString(), ShutdownResume: true,
		Query: resumeInstruction + discussAgentFullContextPrompt(externalOversizedDiscuss()), UserMessagePersisted: true,
	})
	drainStreamChunks(t, chunks)
	var failure error
	for err := range errs {
		failure = err
	}
	if apperror.CodeOf(failure) != apperror.CodeContextProtectedOverflow || pool.calls != 0 {
		t.Fatalf("oversized resume: err=%v dispatches=%d, want a bounded protected_overflow", failure, pool.calls)
	}
}

func TestExternalDiscussFinalAdmissionRejectsIrreducibleContext(t *testing.T) {
	for _, images := range []int{0, 1} {
		service, compactor := newControllerPolicyService(t, nil)
		service.SetContextAbsoluteMaxTokens(1000)
		_, err := service.prepareExternalDiscussContext(t.Context(), ChatRequest{
			BotID: syncCompactBotID, ThreadID: syncCompactThreadID,
			discussMessages: []turn.DiscussMessage{{Role: "user", Content: strings.Repeat("c", 3300)}},
		}, strings.Repeat("r", 665), images)
		if apperror.CodeOf(err) != apperror.CodeContextProtectedOverflow || len(compactor.configs) != 0 {
			t.Fatalf("irreducible external context: err=%v compactions=%d", err, len(compactor.configs))
		}
	}
}

func TestExternalDiscussFinalAdmissionControlNeverReachesRuntimeOrHistory(t *testing.T) {
	for _, mode := range []string{"shadow", "off"} {
		t.Run(mode, func(t *testing.T) {
			pool := &recordingACPPrompter{}
			messages := &recordingMessageService{}
			external := newACPLifecycleService(t, pool, messages, nil)
			service := newDiscussTestService(&fakeRunner{}, &fakeAgentStreamer{}, &fakeDiscussService{
				resolveResult: ResolveRunConfigResult{RuntimeType: sessionpkg.RuntimeACPAgent},
			})
			service.messageService = messages
			service.botPermissions = external.botPermissions
			service.sessionService = external.sessionService
			service.SetACPSessionPool(pool)
			service.turnHooks.streamChat = nil
			service.SetContextAbsoluteMaxTokens(1000)
			service.SetSyncCompactionMode(mode)
			policy, compactor := newControllerPolicyService(t, nil)
			service.settingsService, service.modelsService = policy.settingsService, policy.modelsService
			service.queries = &externalDiscussQueries{recoveryHistoryQueries: &recoveryHistoryQueries{controllerQueries: policy.queries.(*controllerQueries)}}
			service.compactionService = compactor
			configureDiscussLifecycle(service)
			published := 0
			service.publishTurnEvent = func(context.Context, sessionruntime.RunHandle, native.StreamEvent) error { published++; return nil }
			cmd := lifecycleDiscussCommand()
			cmd.DiscussMessages = []turn.DiscussMessage{
				{Role: "user", Content: strings.Repeat("s", 2000), CompactionArtifactID: "summary"},
				{Role: "user", Content: strings.Repeat("c", 1400)},
			}
			handle, err := service.StartTurn(t.Context(), cmd)
			if err != nil {
				t.Fatal(err)
			}
			recomposed := false
			for event := range handle.Events() {
				recomposed = recomposed || event.Kind == turn.DiscussEventRecompose
				if event.Kind == string(native.EventContextRecompose) {
					t.Fatal("private runtime control leaked")
				}
			}
			failures := 0
			for err := range handle.Errs() {
				failures++
				if apperror.CodeOf(err) != apperror.CodeContextProtectedOverflow || errors.Is(err, native.ErrContextRecompose) {
					t.Fatalf("unexpected failure: %v", err)
				}
			}
			if mode == "shadow" && (!recomposed || failures != 0 || len(compactor.configs) != 1) {
				t.Fatalf("recovery outcome: recompose=%v failures=%d compactions=%d", recomposed, failures, len(compactor.configs))
			}
			if mode == "off" && (recomposed || failures != 1 || len(compactor.configs) != 0) {
				t.Fatal("explicit off did not fail closed")
			}
			if pool.calls != 0 || published != 0 || len(messages.persisted) != 0 || len(messages.roundOptions) != 0 || len(messages.deleted) != 0 {
				t.Fatalf("rejected context had side effects: driver=%d public=%d history=%d rounds=%d deletes=%d", pool.calls, published, len(messages.persisted), len(messages.roundOptions), len(messages.deleted))
			}
		})
	}
}

func TestExternalDiscussRecoversPretrimmedHistoryPressure(t *testing.T) {
	service, runner := newControllerPolicyService(t, nil)
	service.SetContextAbsoluteMaxTokens(1000)
	req := ChatRequest{BotID: syncCompactBotID, ThreadID: syncCompactThreadID, discussContextTokens: 20000, discussMessages: []turn.DiscussMessage{{Role: "user", Content: "current"}}}
	_, err := service.prepareExternalDiscussContext(t.Context(), req, "runtime context", 0)
	if !errors.Is(err, native.ErrContextRecompose) || len(runner.configs) != 1 {
		t.Fatalf("lost pretrim pressure: err=%v calls=%d", err, len(runner.configs))
	}
}

func TestExternalDiscussOriginalPressureUsesFinalHistoryAllowance(t *testing.T) {
	service, runner := newControllerPolicyService(t, nil)
	service.SetContextAbsoluteMaxTokens(1000)
	req := ChatRequest{BotID: syncCompactBotID, ThreadID: syncCompactThreadID, discussContextTokens: 900, discussMessages: []turn.DiscussMessage{{Role: "user", Content: "current"}}}
	_, err := service.prepareExternalDiscussContext(t.Context(), req, strings.Repeat("m", 2800), 0)
	if !errors.Is(err, native.ErrContextRecompose) || len(runner.configs) != 1 {
		t.Fatalf("ignored final fixed overhead: err=%v calls=%d", err, len(runner.configs))
	}
}
