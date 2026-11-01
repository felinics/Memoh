package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/context/compaction"
	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	"github.com/felinics/memoh/internal/contextview"
)

type recoveryHistoryService struct {
	messagepkg.Service
	messages  []messagepkg.Message
	truncated bool
	loads     int
	maxBytes  int64
	err       error
}

func (s *recoveryHistoryService) ListHistoryWithinBytes(_ context.Context, _ string, window messagepkg.HistoryWindow) (messagepkg.HistoryLoad, error) {
	s.loads++
	s.maxBytes = window.MaxBytes
	return messagepkg.HistoryLoad{Messages: append([]messagepkg.Message(nil), s.messages...), Truncated: s.truncated}, s.err
}

type recoveryCompactionRunner struct {
	configs []compaction.TriggerConfig
	run     func() ([]compaction.Pass, error)
}

func (r *recoveryCompactionRunner) Compact(_ context.Context, cfg compaction.TriggerConfig) ([]compaction.Pass, error) {
	r.configs = append(r.configs, cfg)
	return r.run()
}

func chatRecoveryFixture(t *testing.T) (*Service, *recoveryHistoryService, *recoveryCompactionRunner, native.RunConfig) {
	t.Helper()
	s, _ := newControllerPolicyService(t)
	history := &recoveryHistoryService{}
	s.messageService = history
	s.checkpoints = emptyHistoryCheckpoints()
	runner := &recoveryCompactionRunner{run: func() ([]compaction.Pass, error) {
		history.messages = []messagepkg.Message{{ID: "history-new", BotID: syncCompactBotID, SessionID: syncCompactThreadID, Role: "assistant", Content: newTextContent("compacted history")}}
		history.truncated = false
		return []compaction.Pass{{}}, nil
	}}
	s.compactionService = runner
	modelMessages := []ModelMessage{{Role: "assistant", Content: newTextContent("frozen fork")}}
	var summaryRecords []historyfrag.HistoryRecord
	for i := range 20 {
		summary := historyfrag.SummaryRecord(fmt.Sprintf("summary-%d", i), strings.Repeat("s", 200), contextfrag.Scope{})
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
	return s, history, runner, cfg
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

type triggerRecoveryMessages struct {
	*recordingMessageService
	history *recoveryHistoryService
}

func (m triggerRecoveryMessages) ListHistoryWithinBytes(ctx context.Context, sessionID string, window messagepkg.HistoryWindow) (messagepkg.HistoryLoad, error) {
	return m.history.ListHistoryWithinBytes(ctx, sessionID, window)
}

type delayedRecoveryCompactor struct {
	calls   atomic.Int32
	delay   time.Duration
	history *recoveryHistoryService
}

func (c *delayedRecoveryCompactor) Compact(ctx context.Context, _ compaction.TriggerConfig) ([]compaction.Pass, error) {
	c.calls.Add(1)
	select {
	case <-time.After(c.delay):
	case <-ctx.Done():
		return nil, context.Cause(ctx)
	}
	c.history.messages = []messagepkg.Message{{ID: "history-new", BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, Role: "assistant", Content: newTextContent("compacted history")}}
	c.history.truncated = false
	return []compaction.Pass{{}}, nil
}

// slowRecoveryFixture serves a history its load's byte budget truncates, and
// a compaction slower than the model idle window.
func slowRecoveryFixture(t *testing.T) (directLifecycleFixture, *delayedRecoveryCompactor) {
	t.Helper()
	fixture := newDirectLifecycleFixture(t, directLifecycleModelSuccess)
	queries := fixture.service.queries.(*directLifecycleQueries)
	model := queries.models["direct-lifecycle-model"]
	model.Config = []byte(`{"context_window":8000}`)
	queries.models["direct-lifecycle-model"] = model
	history := &recoveryHistoryService{truncated: true, messages: []messagepkg.Message{{ID: "history-old", BotID: lifecycleTestBotID, SessionID: lifecycleTestSessionID, Role: "assistant", Content: newTextContent(strings.Repeat("h", 14000))}}}
	fixture.service.messageService = triggerRecoveryMessages{recordingMessageService: fixture.messages, history: history}
	compactor := &delayedRecoveryCompactor{delay: 400 * time.Millisecond, history: history}
	fixture.service.compactionService = compactor
	logger := slog.New(slog.DiscardHandler)
	fixture.service.agent = native.New(native.Deps{Logger: logger, ContextViewApplier: contextview.ProviderRunConfigApplier(logger)})
	fixture.service.streamIdleTimeout = 100 * time.Millisecond
	return fixture, compactor
}

func slowRecoveryRequest() ChatRequest {
	return ChatRequest{
		BotID: lifecycleTestBotID, ChatID: lifecycleTestBotID, ThreadID: lifecycleTestSessionID,
		Query: directLifecyclePrompt + " " + strings.Repeat("q", 6000), UserMessagePersisted: true,
	}
}

// A load the byte budget truncated folds before the run starts, so a
// compaction slower than the idle window does not time the run out.
func TestStreamChatFoldsATruncatedLoadBeforeTheRun(t *testing.T) {
	fixture, compactor := slowRecoveryFixture(t)
	chunks, errs := fixture.service.StreamChat(t.Context(), slowRecoveryRequest())
	var failures []error
	for chunks != nil || errs != nil {
		select {
		case _, ok := <-chunks:
			if !ok {
				chunks = nil
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			failures = append(failures, err)
		}
	}
	if compactor.calls.Load() != 1 || len(failures) != 0 {
		t.Fatalf("compactions=%d errors=%v, want one compaction then a completed turn", compactor.calls.Load(), failures)
	}
}

func TestWSFoldsATruncatedLoadBeforeTheRun(t *testing.T) {
	fixture, compactor := slowRecoveryFixture(t)
	eventCh := make(chan WSStreamEvent, 256)
	outcome, err := fixture.service.StreamChatWS(t.Context(), slowRecoveryRequest(), eventCh, nil)
	if err != nil || outcome.Cause != nil || compactor.calls.Load() != 1 {
		t.Fatalf("error=%v outcome=%+v compactions=%d, want one compaction then a completed turn", err, outcome, compactor.calls.Load())
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

func discussHistoryFrag(index int, sourceID string, kind contextfrag.Kind, msg sdk.Message) contextfrag.ContextFrag {
	return contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: fmt.Sprintf("discuss.message.%03d", index), Message: msg, Kind: kind, Slot: contextfrag.SlotHistory,
		SourceID: sourceID, Collector: discussContextCollector, Index: index,
	})
}

// Selection trims answered history with a notice; that notice counts against
// what must stay, so shedding does not stop one notice short and leave the
// selector to drop the older input with its text.
func TestShedOlderBatchImagesChargesTheTrimNotice(t *testing.T) {
	older := sdk.UserMessage("older input", sdk.ImagePart{Image: "data:image/png;base64,YQ=="})
	frags := []contextfrag.ContextFrag{
		discussHistoryFrag(0, "answered", contextfrag.KindConversationEvent, sdk.UserMessage(strings.Repeat("answered ", 500))),
		discussHistoryFrag(1, "older", contextfrag.KindConversationEvent, older),
		discussHistoryFrag(2, "newest", contextfrag.KindCurrentUserMessage, sdk.UserMessage("newest input")),
	}
	batch := map[string]turn.ContextMessageSource{"older": {Kind: "external", ID: "older", Current: true}, "newest": {Kind: "external", ID: "newest", Current: true}}
	notice := contextfrag.ResolveProviderBudgetFragTokens(contextview.TrimNoticeFrag(contextfrag.Scope{}))
	budget := contextfrag.ResolveProviderBudgetFragTokens(frags[1]) + notice - 1
	cfg := native.RunConfig{ContextSourceFrags: frags, Messages: []sdk.Message{sdk.UserMessage(strings.Repeat("answered ", 500)), older, sdk.UserMessage("newest input")}, ContextMutations: contextfrag.NewMutationLedger()}
	if _, shed := shedOlderBatchImages(cfg, batch, budget); !shed {
		t.Fatal("the older input kept an image that leaves no room for the trim notice")
	}
	if _, shed := shedOlderBatchImages(cfg, batch, budget+1); shed {
		t.Fatal("an image that fits beside the trim notice was shed")
	}
}

// A message sheds only as many of its images as the budget needs, oldest
// first.
func TestShedOlderBatchImagesShedsOnlyTheImagesNeeded(t *testing.T) {
	image := func(data string) sdk.ImagePart { return sdk.ImagePart{Image: "data:image/png;base64," + data} }
	album := sdk.UserMessage("album", image("MQ=="), image("Mg=="), image("Mw=="))
	frags := []contextfrag.ContextFrag{
		discussHistoryFrag(0, "album", contextfrag.KindConversationEvent, album),
		discussHistoryFrag(1, "newest", contextfrag.KindCurrentUserMessage, sdk.UserMessage("newest input")),
	}
	batch := map[string]turn.ContextMessageSource{"album": {Kind: "external", ID: "album", Current: true}, "newest": {Kind: "external", ID: "newest", Current: true}}
	provenance := []native.MessageProvenance{
		{Message: album, Sources: []turn.ContextMessageSource{{Kind: "external", ID: "album"}}, Anchor: true},
		{Message: sdk.UserMessage("newest input"), Sources: []turn.ContextMessageSource{{Kind: "external", ID: "newest"}}, Anchor: true},
	}
	cfg := native.RunConfig{ContextSourceFrags: frags, Messages: []sdk.Message{album, sdk.UserMessage("newest input")}, ContextProvenance: provenance, ContextMutations: contextfrag.NewMutationLedger()}
	budget := contextfrag.ResolveProviderBudgetFragTokens(frags[0]) - contextfrag.EstimateImageTokens
	shed, ok := shedOlderBatchImages(cfg, batch, budget)
	if !ok {
		t.Fatal("an album over budget shed nothing")
	}
	var kept []string
	for _, part := range shed.Messages[0].Content {
		if image, isImage := part.(sdk.ImagePart); isImage {
			kept = append(kept, image.Image)
		}
	}
	if len(kept) != 2 || kept[0] != image("Mg==").Image {
		t.Fatalf("album kept %v, want only its oldest image shed", kept)
	}
	if !reflect.DeepEqual(shed.ContextProvenance[0].Message, shed.Messages[0]) || !reflect.DeepEqual(cfg.ContextProvenance[0].Message, album) {
		t.Fatal("the album without its image no longer renders what it named")
	}
	if records := cfg.ContextMutations.Records(); len(records) != 1 || records[0].Detail != "sources=album" {
		t.Fatalf("mutations = %+v", records)
	}
}

func TestDiscussRecoveryFailsWhenOmittedInputHasNoHistoryRoom(t *testing.T) {
	s, _, runner, cfg := chatRecoveryFixture(t)
	cfg.ContextManifest.BudgetPlan = &contextfrag.ContextBudgetPlan{HistoryBudget: 100}
	cfg.ContextSourceFrags = []contextfrag.ContextFrag{contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "memory", Kind: contextfrag.KindMemoryRecall, Role: sdk.MessageRoleUser,
		Slot: contextfrag.SlotAfterHistoryBeforeCurrent, Text: strings.Repeat("m", 2000), Trust: contextfrag.TrustExternal,
	})}
	cmd := turn.StartTurnCommand{BotID: syncCompactBotID, ThreadID: syncCompactThreadID, DiscussOmittedSources: []turn.ContextMessageSource{{Kind: "external", ID: "omitted", Current: true}}}
	if _, _, err := s.recoverDiscussContextBudget(t.Context(), cmd, nil, "", cfg, new(bool)); !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) || len(runner.configs) != 0 {
		t.Fatalf("err=%v compactions=%d, want omitted input without history room to fail the run", err, len(runner.configs))
	}
}

func TestPausesIdleDuringInRunCompaction(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, idle := withIdleTimeout(t.Context(), 10*time.Second)
		defer idle.Stop()
		cfg := native.RunConfig{SwitchContext: func(ctx context.Context, _ native.ContextSwitch) (string, error) {
			time.Sleep(time.Minute)
			return "trigger=in_run passes=1", ctx.Err()
		}}
		cfg = pauseIdleDuringBudgetRecovery(cfg, idle)
		_, err := cfg.SwitchContext(ctx, native.ContextSwitch{})
		if err != nil || idle.DidFire() {
			t.Fatalf("in-run compaction canceled by idle watchdog: err=%v idle_fired=%v", err, idle.DidFire())
		}
	})
}

func (*recoveryCompactionRunner) Fold(context.Context, compaction.Event) ([]compaction.Pass, error) {
	return nil, errors.New("pre-turn recovery compacts history")
}

func (*delayedRecoveryCompactor) Fold(context.Context, compaction.Event) ([]compaction.Pass, error) {
	return nil, errors.New("pre-turn recovery compacts history")
}

// A load the byte budget truncated folds the rows it left out first, keeping
// the request's own input, and loads again; a compaction that fails, or a
// load still truncated after it, fails the request.
func TestLoadChatHistoryFoldsATruncatedLoad(t *testing.T) {
	req := ChatRequest{BotID: syncCompactBotID, ChatID: syncCompactBotID, ThreadID: syncCompactThreadID, PersistedUserMessageID: "request-row"}
	for _, tc := range []struct {
		name      string
		truncated bool
		run       func(*recoveryHistoryService) ([]compaction.Pass, error)
		loads     int
		fails     bool
	}{
		{name: "whole", loads: 1},
		{name: "folded", truncated: true, loads: 2},
		{name: "compaction failed", truncated: true, loads: 1, fails: true, run: func(*recoveryHistoryService) ([]compaction.Pass, error) {
			return nil, errors.New("summarizer down")
		}},
		{name: "still truncated", truncated: true, loads: 2, fails: true, run: func(*recoveryHistoryService) ([]compaction.Pass, error) {
			return []compaction.Pass{{}}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, history, runner, _ := chatRecoveryFixture(t)
			history.truncated = tc.truncated
			history.messages = []messagepkg.Message{{ID: "history-old", BotID: syncCompactBotID, SessionID: syncCompactThreadID, Role: "assistant", Content: newTextContent("older history")}}
			if tc.run != nil {
				runner.run = func() ([]compaction.Pass, error) { return tc.run(history) }
			}
			got, err := s.loadChatHistory(t.Context(), req, chatHistoryScope(req), false, 8000, "")
			if fails := apperror.CodeOf(err) == apperror.CodeContextBudgetUnsatisfied; fails != tc.fails || !tc.fails && err != nil || history.loads != tc.loads {
				t.Fatalf("err=%v loads=%d, want failure=%v after %d loads", err, history.loads, tc.fails, tc.loads)
			}
			if !tc.truncated {
				if len(runner.configs) != 0 {
					t.Fatal("a whole load compacted")
				}
				return
			}
			if len(runner.configs) != 1 || !slices.Contains(runner.configs[0].Anchors, turn.ContextMessageSource{Kind: "history", ID: "request-row", Current: true}) {
				t.Fatalf("compactions=%+v, want one keeping the request's own input", runner.configs)
			}
			if tc.name == "folded" && (len(got.messages) != 1 || got.truncated) {
				t.Fatalf("history after the fold = %+v", got)
			}
		})
	}
}
