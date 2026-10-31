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
	acpagent "github.com/felinics/memoh/internal/agent/runtime/external/acp"
	acpclient "github.com/felinics/memoh/internal/agent/runtime/external/acp/client"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
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
	messages := externalDiscuss(100)
	messages[0] = turn.DiscussMessage{Role: "user", Content: "old summary", CompactionArtifactID: "summary"}
	sections := buildRuntimeContextSections(runtimeContextRenderInput{})
	markdown, _, _ := runtimeContextViaContextView(t.Context(), nil, sections, "")
	got, err := service.prepareExternalDiscussContext(t.Context(), ChatRequest{discussMessages: messages}, markdown, 0)
	if err != nil {
		t.Fatal(err)
	}
	if actual := turn.EstimateTokensFromBytes(len(got.Query) + len(markdown)); actual > 1000 || !strings.Contains(got.Query, "old summary") || !strings.Contains(got.Query, "CURRENT") {
		t.Fatalf("final external envelope=%d tokens, want every message within 1000", actual)
	}
	_, err = service.prepareExternalDiscussContext(t.Context(), ChatRequest{discussMessages: messages}, markdown+strings.Repeat("runtime context\n", 125), 0)
	if apperror.CodeOf(err) != apperror.CodeContextBudgetUnsatisfied {
		t.Fatalf("runtime context crowding out history: err=%v, want budget_unsatisfied instead of dropping messages", err)
	}
}

func TestExternalDiscussDriverFailsInsteadOfDroppingInput(t *testing.T) {
	pool := &recordingACPPrompter{result: acpclient.PromptResult{Text: "done", StopReason: "end_turn"}}
	messages := &recordingMessageService{}
	service := newACPLifecycleService(t, pool, messages, nil)
	service.SetRuntimeRegistry(testRuntimes(t, acpagent.NewDriver(pool)))
	service.SetContextAbsoluteMaxTokens(1000)
	chunks, errs := service.StreamChat(t.Context(), ChatRequest{
		BotID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID, Query: "candidate", UserMessagePersisted: true,
		discussMessages: externalDiscuss(1000),
	})
	drainStreamChunks(t, chunks)
	var failure error
	for err := range errs {
		failure = err
	}
	if apperror.CodeOf(failure) != apperror.CodeContextBudgetUnsatisfied || pool.calls != 0 {
		t.Fatalf("err=%v dispatches=%d, want budget_unsatisfied before dispatch", failure, pool.calls)
	}
}

func TestExternalDiscussOversizedBatchCompactsOlderInputOrFails(t *testing.T) {
	var messages []turn.DiscussMessage
	for _, id := range []string{"a", "b", "c", "d"} {
		messages = append(messages, turn.DiscussMessage{Role: "user", Content: id + strings.Repeat("x", 1200), Source: &turn.ContextMessageSource{Kind: "external", ID: id, Current: true}})
	}
	for _, exhausted := range []bool{false, true} {
		service, compactor := newControllerPolicyService(t)
		service.SetContextAbsoluteMaxTokens(1000)
		got, err := service.prepareExternalDiscussContext(t.Context(), ChatRequest{BotID: syncCompactBotID, ThreadID: syncCompactThreadID, discussMessages: messages, discussRecoveryExhausted: exhausted}, "", 0)
		if !exhausted {
			if !errors.Is(err, native.ErrContextRecompose) || len(compactor.configs) != 1 {
				t.Fatalf("older input must be compacted: err=%v compactions=%d", err, len(compactor.configs))
			}
			for _, source := range compactor.configs[0].Anchors {
				if source.ID == "a" {
					t.Fatalf("the oldest input was protected from compaction: %+v", compactor.configs[0].Anchors)
				}
			}
			continue
		}
		if apperror.CodeOf(err) != apperror.CodeContextBudgetUnsatisfied || len(compactor.configs) != 0 || got.Query != "" {
			t.Fatalf("exhausted recovery must fail instead of omitting input: err=%v compactions=%d", err, len(compactor.configs))
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
	service.SetRuntimeRegistry(testRuntimes(t, acpagent.NewDriver(pool)))
	service.SetContextAbsoluteMaxTokens(1000)
	saved := &resumeSaveQueries{}
	policy, _ := newControllerPolicyService(t)
	service.queries = externalResumeQueries{externalDiscussQueries: &externalDiscussQueries{&recoveryHistoryQueries{policy.queries.(*controllerQueries)}}, saved: saved}
	service.resumeSecret = "resume-test-secret"
	ctx := runtimefence.WithContext(t.Context(), runtimefence.Fence{BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, Token: 7})
	return service, pool, saved, ctx
}

func externalDiscuss(n int) []turn.DiscussMessage {
	sources := make([]turn.DiscussMessage, n)
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
		Query: discussAgentFullContextPrompt(externalDiscuss(100)), UserMessagePersisted: true, discussMessages: externalDiscuss(100),
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
