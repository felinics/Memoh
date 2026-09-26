package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/contextview"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

type recoveryHistoryQueries struct{ *controllerQueries }

func (*recoveryHistoryQueries) ListCompactionArtifactLineageBySession(context.Context, pgtype.UUID) ([]sqlc.BotHistoryMessageCompact, error) {
	return nil, nil
}

type recoveryHistoryService struct {
	messagepkg.Service
	messages []messagepkg.Message
	loads    int
	maxBytes int64
	err      error
}

func (s *recoveryHistoryService) ListActiveSinceBySessionWithinBytes(_ context.Context, _ string, _ time.Time, maxBytes int64) ([]messagepkg.Message, error) {
	s.loads++
	s.maxBytes = maxBytes
	return s.messages, s.err
}

type recoveryCompactionRunner struct {
	configs []compaction.TriggerConfig
	run     func() (compaction.Result, error)
}

func (r *recoveryCompactionRunner) RunCompactionSync(_ context.Context, cfg compaction.TriggerConfig) (compaction.Result, error) {
	r.configs = append(r.configs, cfg)
	return r.run()
}

func chatRecoveryFixture(t *testing.T) (*Service, *recoveryHistoryService, *recoveryCompactionRunner, native.RunConfig) {
	t.Helper()
	s, _ := newControllerPolicyService(t, nil)
	s.queries = &recoveryHistoryQueries{controllerQueries: s.queries.(*controllerQueries)}
	history := &recoveryHistoryService{}
	s.messageService = history
	runner := &recoveryCompactionRunner{run: func() (compaction.Result, error) {
		history.messages = []messagepkg.Message{{ID: "history-new", BotID: syncCompactBotID, SessionID: syncCompactThreadID, Role: "assistant", Content: newTextContent("compacted history")}}
		return compaction.Result{Status: compaction.StatusOK}, nil
	}}
	s.compactionService = runner
	modelMessages := []ModelMessage{{Role: "assistant", Content: newTextContent("frozen fork")}}
	var summaryRecords []historyfrag.HistoryRecord
	for i := range 20 {
		summary := historyfrag.SummaryRecord(fmt.Sprintf("summary-%d", i), strings.Repeat("s", 200), nil, contextfrag.Scope{})
		summaryRecords = append(summaryRecords, summary)
		modelMessages = append(modelMessages, summary.ModelMessage)
	}
	modelMessages = append(modelMessages,
		ModelMessage{Role: "assistant", Content: newTextContent(strings.Repeat("h", 8000))},
		ModelMessage{Role: "user", Content: newTextContent("frozen memory")},
		ModelMessage{Role: "user", Content: newTextContent("current request")},
	)
	cfg := native.RunConfig{
		RunID:                          "original-run",
		Model:                          &sdk.Model{ID: "original-model"},
		Identity:                       native.SessionContext{BotID: syncCompactBotID, SessionID: syncCompactThreadID},
		Messages:                       modelMessagesToSDKMessages(modelMessages),
		ContextFrags:                   historyContextFragsForMessages(modelMessages, summaryRecords),
		ContextCurrentUserMessageIndex: intPointer(23),
		ContextMemoryMessageIndex:      intPointer(22),
		ContextTrimmableMessages:       22,
		ContextBudgetMaxTokens:         16000,
		ContextToolDefsResolved:        true,
		ContextToolDefs:                []contextfrag.ToolDefAccounting{{Name: "large_roster", TokenEstimate: 10000}},
		ContextHookText:                "frozen hook",
		ContextQueryMaterialized:       true,
		ContextLifecycle:               contextfrag.NewLifecycleHolder(),
		InjectCh:                       make(chan native.InjectMessage),
	}
	cfg.ContextSourceFrags = buildProviderSourceFrags(t.Context(), cfg, []native.SystemSection{{ID: "system", Kind: contextfrag.KindSystemPrompt, Text: strings.Repeat("s", 3200)}}, nil)
	cfg.RecoverContextBudget = s.chatBudgetRecovery(ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID}, chatHistoryLayout{forkCount: 1, historyCount: 22, pressureTokens: 2500})
	return s, history, runner, cfg
}

func TestChatBudgetRecoveryReloadsOnlyHistory(t *testing.T) {
	s, history, runner, cfg := chatRecoveryFixture(t)
	got, err := contextview.ProviderRunConfigApplier(nil)(t.Context(), cfg)
	if err != nil {
		t.Fatalf("history recovery failed: %v", err)
	}
	if len(runner.configs) != 1 || history.loads != 1 || history.maxBytes != s.historyLoadMaxBytes(16000) {
		t.Fatalf("compactions=%d history loads=%d maxBytes=%d", len(runner.configs), history.loads, history.maxBytes)
	}
	if compactionCfg := runner.configs[0]; !compactionCfg.HardPressure || compactionCfg.TargetTokens >= 400 || compactionCfg.TotalInputTokens != 3900 {
		t.Fatalf("recovery did not subtract fixed history content: %#v", compactionCfg)
	}
	if got.RunID != cfg.RunID || got.Model != cfg.Model || got.InjectCh != cfg.InjectCh || !reflect.DeepEqual(got.ContextToolDefs, cfg.ContextToolDefs) {
		t.Fatal("recovery replaced the active run configuration")
	}
	for _, text := range []string{"frozen fork", "compacted history", "frozen memory", "frozen hook", "current request"} {
		if countRecoveryText(got.Messages, text) != 1 {
			t.Errorf("provider messages contain %q %d times", text, countRecoveryText(got.Messages, text))
		}
	}
	for _, mutation := range got.ContextMutations.Records() {
		if mutation.Kind == contextfrag.MutationContextBudgetFailure {
			t.Fatal("successful recovery recorded a budget failure")
		}
	}
}

func TestChatBudgetRecoveryCannotBypassFailure(t *testing.T) {
	for _, outcome := range []string{"noop", "failure", "reload failure", "droppable reload"} {
		t.Run(outcome, func(t *testing.T) {
			_, history, runner, cfg := chatRecoveryFixture(t)
			runner.run = func() (compaction.Result, error) {
				switch outcome {
				case "noop":
					return compaction.Result{}, nil
				case "failure":
					return compaction.Result{}, errors.New("private failure")
				case "reload failure":
					history.err = errors.New("private reload failure")
				case "droppable reload":
					history.messages = []messagepkg.Message{{ID: "history-new", BotID: syncCompactBotID, SessionID: syncCompactThreadID, Role: "assistant", Content: newTextContent(strings.Repeat("h", 8000))}}
				}
				return compaction.Result{Status: compaction.StatusOK}, nil
			}
			got, err := contextview.ProviderRunConfigApplier(nil)(t.Context(), cfg)
			if outcome == "droppable reload" {
				if err != nil || len(runner.configs) != 2 || countRecoveryText(got.Messages, strings.Repeat("h", 8000)) != 0 {
					t.Fatalf("final admission did not drop oversized reload: error=%v calls=%d", err, len(runner.configs))
				}
				return
			}
			if !errors.Is(err, contextfrag.ErrProtectedContextOverflow) || len(runner.configs) != 1 {
				t.Fatalf("unrecovered error=%v calls=%d", err, len(runner.configs))
			}
		})
	}
}

func countRecoveryText(messages []sdk.Message, text string) int {
	count := 0
	for _, message := range messages {
		for _, part := range message.Content {
			if value, ok := part.(sdk.TextPart); ok && strings.Contains(value.Text, text) {
				count++
			}
		}
	}
	return count
}

func TestChatBudgetRecoveryPreservesMaterializedCurrentMedia(t *testing.T) {
	s, _, _, cfg := chatRecoveryFixture(t)
	cfg.Messages = cfg.Messages[:23]
	cfg.ContextCurrentUserMessageIndex = nil
	cfg.Query = "the original timestamped request"
	cfg.InlineImages = []sdk.ImagePart{{Image: "data:image/png;base64,YQ==", MediaType: "image/png"}}
	cfg.InlineAttachments = []sdk.MessagePart{sdk.FilePart{Data: "Yg==", MediaType: "application/pdf", Filename: "attached.pdf"}}
	cfg.ForkContextSourceMessageIDs = make([]string, len(cfg.Messages))
	cfg.ForkContextSourceMessageIDs[0] = "inherited-source"
	cfg = s.prepareRunConfig(t.Context(), cfg)
	cfg.ContextManifest.BudgetPlan = &contextfrag.ContextBudgetPlan{HistoryBudget: 2000}
	current := cfg.Messages[*cfg.ContextCurrentUserMessageIndex]
	got, recovered, err := cfg.RecoverContextBudget(t.Context(), cfg)
	if err != nil || !recovered {
		t.Fatalf("recover current media: recovered=%v error=%v", recovered, err)
	}
	if got.ContextCurrentUserMessageIndex == nil || !reflect.DeepEqual(got.Messages[*got.ContextCurrentUserMessageIndex], current) {
		t.Fatalf("current request changed across history collapse: %#v", got.Messages)
	}
	if got.ContextMemoryMessageIndex == nil || countRecoveryText(got.Messages[*got.ContextMemoryMessageIndex:*got.ContextMemoryMessageIndex+1], "frozen memory") != 1 {
		t.Fatal("memory index no longer identifies the frozen memory")
	}
	if got.System != cfg.System || got.ForkContextSourceMessageIDs[0] != "inherited-source" || len(got.ForkContextSourceMessageIDs) != len(got.Messages) {
		t.Fatal("recovery changed system text or lost aligned fork source identities")
	}
	for _, frag := range got.ContextSourceFrags {
		if frag.Kind == contextfrag.KindCurrentUserMessage {
			if frag.ID != "message.003" || frag.Provenance.Index != 3 || !reflect.DeepEqual(*frag.Parts[0].SDKMessage, current) {
				t.Fatalf("current fragment lost its remapped identity or media: %#v", frag)
			}
		}
	}
}

func TestChatBudgetRecoveryDeclinesIrreducibleContext(t *testing.T) {
	for _, scenario := range []string{"fixed hook", "off"} {
		t.Run(scenario, func(t *testing.T) {
			s, history, runner, cfg := chatRecoveryFixture(t)
			switch scenario {
			case "off":
				s.SetSyncCompactionMode(scenario)
			case "fixed hook":
				cfg.ContextSourceFrags = append(cfg.ContextSourceFrags, contextfrag.TextFrag(contextfrag.TextFragInput{ID: "fixed-hook", Text: strings.Repeat("x", 4000), Kind: contextfrag.KindHookContext, Slot: contextfrag.SlotHistory}))
			}
			cfg.ContextManifest.BudgetPlan = &contextfrag.ContextBudgetPlan{HistoryBudget: 998}
			_, recovered, err := cfg.RecoverContextBudget(t.Context(), cfg)
			if err != nil || recovered || len(runner.configs) != 0 || history.loads != 0 {
				t.Fatalf("irreducible context ran recovery: recovered=%v error=%v calls=%d loads=%d", recovered, err, len(runner.configs), history.loads)
			}
		})
	}
}

type recoverySilentProvider struct {
	triggerCaptureProvider
	calls atomic.Int32
}

func (p *recoverySilentProvider) DoStream(ctx context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
	p.calls.Add(1)
	parts := make(chan sdk.StreamPart)
	go func() {
		<-ctx.Done()
		close(parts)
	}()
	return &sdk.StreamResult{Stream: parts}, nil
}

func TestChatBudgetRecoveryPausesIdleUntilProviderDispatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		_, history, runner, cfg := chatRecoveryFixture(t)
		runner.run = func() (compaction.Result, error) {
			time.Sleep(time.Minute)
			history.messages = []messagepkg.Message{{ID: "history-new", BotID: syncCompactBotID, SessionID: syncCompactThreadID, Role: "assistant", Content: newTextContent("compacted history")}}
			return compaction.Result{Status: compaction.StatusOK}, nil
		}
		provider := &recoverySilentProvider{}
		cfg.Model.Provider = provider
		ctx, idle := withIdleTimeout(t.Context(), 10*time.Second)
		defer idle.Stop()
		cfg = pauseIdleDuringBudgetRecovery(cfg, idle)
		agent := native.New(native.Deps{ContextViewApplier: contextview.ProviderRunConfigApplier(nil)})
		for range agent.Stream(ctx, cfg) {
		}
		if len(runner.configs) != 1 || provider.calls.Load() != 1 || !idle.DidFire() {
			t.Fatalf("compaction calls=%d provider calls=%d idle fired=%v, want compaction then a timed-out provider", len(runner.configs), provider.calls.Load(), idle.DidFire())
		}
	})
}

func TestChatBudgetRecoveryCompactsOrdinaryHistoryBeforeTrimming(t *testing.T) {
	s, history, runner, cfg := chatRecoveryFixture(t)
	cfg.Messages = []sdk.Message{sdk.AssistantMessage(strings.Repeat("h", 8000)), sdk.UserMessage("continue")}
	cfg.ContextFrags = nil
	cfg.ContextTrimmableMessages = 1
	cfg.ContextCurrentUserMessageIndex = intPointer(1)
	cfg.ContextMemoryMessageIndex = nil
	cfg.ContextHookText = ""
	cfg.ContextSourceFrags = buildProviderSourceFrags(t.Context(), cfg, []native.SystemSection{{ID: "system", Kind: contextfrag.KindSystemPrompt, Text: strings.Repeat("s", 3200)}}, nil)
	cfg.RecoverContextBudget = s.chatBudgetRecovery(ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID}, chatHistoryLayout{historyCount: 1, pressureTokens: 2500})
	got, err := contextview.ProviderRunConfigApplier(nil)(t.Context(), cfg)
	if err != nil || len(runner.configs) != 1 || history.loads != 1 || countRecoveryText(got.Messages, "compacted history") != 1 {
		t.Fatalf("ordinary history was trimmed instead of compacted: error=%v calls=%d loads=%d messages=%v", err, len(runner.configs), history.loads, got.Messages)
	}
	if runner.configs[0].TargetTokens != 399 || runner.configs[0].TotalInputTokens != 3125 {
		t.Fatalf("ordinary recovery did not use history allowance 998: %#v", runner.configs[0])
	}
}

func TestChatBudgetRecoveryLeavesFittingHistoryUntouched(t *testing.T) {
	s, history, runner, cfg := chatRecoveryFixture(t)
	cfg.ContextManifest.BudgetPlan = &contextfrag.ContextBudgetPlan{HistoryBudget: 10000}
	cfg.RecoverContextBudget = s.chatBudgetRecovery(ChatRequest{BotID: syncCompactBotID, ThreadID: syncCompactThreadID}, chatHistoryLayout{forkCount: 1, historyCount: 22, pressureTokens: 2500})
	got, recovered, err := cfg.RecoverContextBudget(t.Context(), cfg)
	if err != nil || recovered || len(runner.configs) != 0 || history.loads != 0 || !reflect.DeepEqual(got.Messages, cfg.Messages) {
		t.Fatalf("fitting history triggered recovery: recovered=%v error=%v calls=%d loads=%d", recovered, err, len(runner.configs), history.loads)
	}
}

func TestChatBudgetRecoveryUpdatesExistingForkSnapshotSources(t *testing.T) {
	_, _, _, cfg := chatRecoveryFixture(t)
	cfg.Messages[0] = sdk.AssistantMessage("compacted history")
	cfg.ContextSourceFrags = buildProviderSourceFrags(t.Context(), cfg, []native.SystemSection{{ID: "system", Kind: contextfrag.KindSystemPrompt, Text: strings.Repeat("s", 3200)}}, nil)
	cfg.ForkContextSourceMessageIDs = make([]string, len(cfg.Messages))
	cfg.ForkContextSourceMessageIDs[0] = "inherited-source"
	cfg.ForkContext = agenttools.NewMessageSnapshotWithSources(cfg.Messages, cfg.ForkContextSourceMessageIDs)
	snapshot := cfg.ForkContext
	got, err := contextview.ProviderRunConfigApplier(nil)(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got.ForkContext != snapshot {
		t.Fatal("recovery replaced the snapshot captured by tool closures")
	}
	if err := snapshot.Store(got.Messages); err != nil {
		t.Fatal(err)
	}
	entries, err := snapshot.Entries()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 || entries[0].SourceMessageID != "inherited-source" || entries[1].SourceMessageID != "history-new" {
		t.Fatalf("tool-visible fork sources = %#v, want preserved fork plus newly loaded row", entries)
	}
}

func TestChatBudgetRecoveryPreservesPipelineCurrentBoundary(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(strconv.FormatBool(changed), func(t *testing.T) {
			_, _, _, cfg := chatRecoveryFixture(t)
			current := sdk.UserMessage("pipeline current", sdk.ImagePart{Image: "data:image/png;base64,YQ==", MediaType: "image/png"})
			cfg.Messages[22], cfg.Messages[23] = current, sdk.UserMessage("frozen memory")
			cfg.ContextCurrentUserMessageIndex, cfg.ContextMemoryMessageIndex = intPointer(22), intPointer(23)
			cfg.ContextTrimmableMessages = 23
			cfg.ContextSourceFrags = buildProviderSourceFrags(t.Context(), cfg, []native.SystemSection{{ID: "system", Kind: contextfrag.KindSystemPrompt, Text: "frozen system"}}, nil)
			history := []ModelMessage{{Role: "assistant", Content: newTextContent("compacted history")}, {Role: "user", Content: newTextContent("pipeline current")}}
			if changed {
				history[1].Content = newTextContent("a different newly arrived request")
			}
			layout := chatHistoryLayout{forkCount: 1, historyCount: 23, usePipeline: true}
			got, err := layout.replaceHistory(t.Context(), cfg, ChatRequest{}, history, nil)
			if changed {
				if err == nil {
					t.Fatal("recovery replaced a different current request")
				}
				return
			}
			if err != nil || got.ContextCurrentUserMessageIndex == nil || *got.ContextCurrentUserMessageIndex != 2 ||
				got.ContextMemoryMessageIndex == nil || *got.ContextMemoryMessageIndex != 3 || !reflect.DeepEqual(got.Messages[2], current) {
				t.Fatalf("pipeline current/media boundary changed: error=%v messages=%v", err, got.Messages)
			}
			for _, frag := range got.ContextSourceFrags {
				if frag.Kind == contextfrag.KindCurrentUserMessage && (frag.ID != "message.002" || frag.Provenance.Index != 2) {
					t.Fatalf("pipeline current fragment was not remapped: %#v", frag)
				}
			}
		})
	}
}

func TestChatBudgetRecoveryAccountsForRenderedPipelineSummary(t *testing.T) {
	s, _, runner, cfg := chatRecoveryFixture(t)
	cfg.Messages = []sdk.Message{sdk.UserMessage("<summary>" + strings.Repeat("s", 3200) + "</summary>"), sdk.AssistantMessage("own raw history"), sdk.UserMessage("continue")}
	cfg.ContextFrags = nil
	cfg.ContextTrimmableMessages = 2
	cfg.ContextCurrentUserMessageIndex = intPointer(2)
	cfg.ContextMemoryMessageIndex = nil
	cfg.ContextHookText = ""
	cfg.ContextSourceFrags = buildProviderSourceFrags(t.Context(), cfg, []native.SystemSection{{ID: "system", Kind: contextfrag.KindSystemPrompt, Text: strings.Repeat("s", 3200)}}, nil)
	cfg.RecoverContextBudget = s.chatBudgetRecovery(ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID}, chatHistoryLayout{historyCount: 2, pressureTokens: 5})
	got, err := contextview.ProviderRunConfigApplier(nil)(t.Context(), cfg)
	if err != nil || len(runner.configs) != 1 || countRecoveryText(got.Messages, "compacted history") != 1 {
		t.Fatalf("rendered summary pressure did not trigger recovery: error=%v calls=%d", err, len(runner.configs))
	}
}

func TestChatBudgetRecoveryFusesSummaryOnlyHistoryInShadow(t *testing.T) {
	s, history, runner, cfg := chatRecoveryFixture(t)
	s.SetSyncCompactionMode("shadow")
	var frags []contextfrag.ContextFrag
	for _, frag := range cfg.ContextSourceFrags {
		if frag.ID != "message.021" {
			frags = append(frags, frag)
		}
	}
	cfg.ContextSourceFrags = frags
	cfg.RecoverContextBudget = s.chatBudgetRecovery(ChatRequest{BotID: syncCompactBotID, ThreadID: syncCompactThreadID}, chatHistoryLayout{forkCount: 1, historyCount: 22})
	got, err := contextview.ProviderRunConfigApplier(nil)(t.Context(), cfg)
	if err != nil || len(runner.configs) != 1 || history.loads != 1 || countRecoveryText(got.Messages, "compacted history") != 1 {
		t.Fatalf("summary-only recovery: err=%v compactions=%d loads=%d", err, len(runner.configs), history.loads)
	}
}

func TestBudgetRecoveryRetainsPressureAfterAllHistoryWasTrimmed(t *testing.T) {
	for _, mode := range []string{"chat", "discuss"} {
		t.Run(mode, func(t *testing.T) {
			s, history, runner, cfg := chatRecoveryFixture(t)
			s.SetSyncCompactionMode("shadow")
			cfg.Messages = []sdk.Message{sdk.UserMessage("current request")}
			cfg.ContextFrags = nil
			cfg.ContextTrimmableMessages = 0
			cfg.ContextCurrentUserMessageIndex = intPointer(0)
			cfg.ContextMemoryMessageIndex = nil
			cfg.ContextHookText = ""
			cfg.ContextSourceFrags = buildProviderSourceFrags(t.Context(), cfg, []native.SystemSection{{ID: "system", Kind: contextfrag.KindSystemPrompt, Text: strings.Repeat("s", 3200)}}, nil)
			if mode == "chat" {
				cfg.RecoverContextBudget = s.chatBudgetRecovery(ChatRequest{BotID: syncCompactBotID, ThreadID: syncCompactThreadID}, chatHistoryLayout{pressureTokens: 20000})
			} else {
				cfg.RecoverContextBudget = func(ctx context.Context, c native.RunConfig) (native.RunConfig, bool, error) {
					return s.recoverDiscussContextBudget(ctx, turn.StartTurnCommand{BotID: syncCompactBotID, ThreadID: syncCompactThreadID, DiscussContextTokens: 20004, DiscussCurrentTokens: 4}, "", c)
				}
			}
			got, err := contextview.ProviderRunConfigApplier(nil)(t.Context(), cfg)
			if len(runner.configs) != 1 {
				t.Fatalf("lost original pressure: calls=%d err=%v", len(runner.configs), err)
			}
			if mode == "discuss" {
				if !errors.Is(err, native.ErrContextRecompose) {
					t.Fatalf("expected recompose: %v", err)
				}
			} else if err != nil || history.loads != 1 || countRecoveryText(got.Messages, "compacted history") != 1 || countRecoveryText(got.Messages, "current request") != 1 {
				t.Fatalf("reloaded history/current lost: err=%v loads=%d messages=%v", err, history.loads, got.Messages)
			}
		})
	}
}

func TestChatBudgetRecoveryContinuesAfterPartialProgress(t *testing.T) {
	_, history, runner, cfg := chatRecoveryFixture(t)
	runner.run = func() (compaction.Result, error) {
		text := strings.Repeat("h", 8000)
		status := compaction.StatusProgress
		if len(runner.configs) == 2 {
			text = "fitting final summary"
			status = compaction.StatusOK
		}
		history.messages = []messagepkg.Message{{ID: "history-new", BotID: syncCompactBotID, SessionID: syncCompactThreadID, Role: "assistant", Content: newTextContent(text)}}
		return compaction.Result{Status: status}, nil
	}
	got, err := contextview.ProviderRunConfigApplier(nil)(t.Context(), cfg)
	if err != nil || len(runner.configs) != 2 || countRecoveryText(got.Messages, "fitting final summary") != 1 {
		t.Fatalf("progress did not reach admission: error=%v calls=%d", err, len(runner.configs))
	}
}

func TestChatBudgetRecoveryInsertsEmptyHistoryBeforeFrozenSuffix(t *testing.T) {
	s, _, _, cfg := chatRecoveryFixture(t)
	cfg.Messages = []sdk.Message{sdk.UserMessage("frozen memory"), sdk.UserMessage("current request")}
	cfg.ContextFrags = nil
	cfg.ContextTrimmableMessages = 0
	cfg.ContextCurrentUserMessageIndex = intPointer(1)
	cfg.ContextMemoryMessageIndex = intPointer(0)
	cfg.ContextSourceFrags = buildProviderSourceFrags(t.Context(), cfg, []native.SystemSection{{ID: "system", Kind: contextfrag.KindSystemPrompt, Text: strings.Repeat("s", 3200)}}, nil)
	cfg.RecoverContextBudget = s.chatBudgetRecovery(ChatRequest{BotID: syncCompactBotID, ThreadID: syncCompactThreadID}, chatHistoryLayout{pressureTokens: 20000})
	got, err := contextview.ProviderRunConfigApplier(nil)(t.Context(), cfg)
	want := []sdk.Message{sdk.AssistantMessage("compacted history"), sdk.UserMessage("frozen memory"), sdk.UserMessage("frozen hook"), sdk.UserMessage("current request")}
	if err != nil || !reflect.DeepEqual(got.Messages, want) {
		t.Fatalf("recovery reordered context: err=%v messages=%v", err, got.Messages)
	}
}

func TestPipelineMediaFollowsExplicitCurrentInput(t *testing.T) {
	s, _, _, cfg := chatRecoveryFixture(t)
	cfg.Messages = []sdk.Message{sdk.UserMessage("current"), sdk.UserMessage("self echo"), sdk.UserMessage("memory")}
	cfg.ContextCurrentUserMessageIndex = intPointer(0)
	cfg.ContextMemoryMessageIndex = intPointer(2)
	cfg.InlineImages = []sdk.ImagePart{{Image: "data:image/png;base64,YQ=="}}
	cfg.InlineAttachments = []sdk.MessagePart{sdk.FilePart{Data: "Yg==", MediaType: "application/pdf"}}
	cfg = s.prepareRunConfig(t.Context(), cfg)
	if len(cfg.Messages[0].Content) != 3 || len(cfg.Messages[1].Content) != 1 || len(cfg.Messages[2].Content) != 1 {
		t.Fatalf("media detached from current input: %v", cfg.Messages)
	}
}
