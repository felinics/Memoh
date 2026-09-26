package application

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
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
