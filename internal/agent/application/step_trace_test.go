package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func providerStepEnd(started, first, ended int64, usage sdk.Usage, reason string) native.StreamEvent {
	raw, _ := json.Marshal(usage)
	return native.StreamEvent{
		Type:         native.EventStepEnd,
		FinishReason: reason,
		Usage:        raw,
		Timing:       &native.StepTiming{StartedAtMS: started, FirstTokenAtMS: first, EndedAtMS: ended},
	}
}

func TestStepTraceTrackerTakesCompletedProviderSteps(t *testing.T) {
	t.Parallel()

	tracker := newStepTraceTracker(nil)
	tracker.observeProvider(native.StreamEvent{Type: native.EventRetry})
	tracker.observeProvider(native.StreamEvent{Type: native.EventStepStart, Timing: &native.StepTiming{StartedAtMS: 1_000}})
	tracker.observeProvider(providerStepEnd(1_000, 1_250, 3_000, sdk.Usage{
		InputTokens: 120, OutputTokens: 15, ReasoningTokens: 5,
		InputTokenDetails: sdk.InputTokenDetail{NoCacheTokens: 40, CacheReadTokens: 80, CacheWriteTokens: 10},
	}, "tool-calls"))

	traces := tracker.take()
	if len(traces) != 1 {
		t.Fatalf("traces = %#v, want one", traces)
	}
	got := traces[0]
	if got.Version != messagepkg.StepTraceVersion || got.StepIndex != 0 || got.StartedAtMS != 1_000 || got.FirstTokenAtMS != 1_250 || got.EndedAtMS != 3_000 || got.FinishReason != "tool-calls" {
		t.Fatalf("trace = %#v", got)
	}
	if got.Usage == nil || *got.Usage != (messagepkg.StepTraceUsage{InputTokens: 120, CachedInputTokens: 80, CacheWriteTokens: 10, OutputTokens: 15, ReasoningTokens: 5}) {
		t.Fatalf("usage = %#v", got.Usage)
	}
	if again := tracker.take(); len(again) != 0 {
		t.Fatalf("second take = %#v, want empty", again)
	}

	tracker.observeProvider(providerStepEnd(4_000, 4_100, 5_000, sdk.Usage{InputTokens: 10, CachedInputTokens: 6}, "stop"))
	next := tracker.take()
	if len(next) != 1 || next[0].StepIndex != 1 || next[0].Usage.CachedInputTokens != 6 {
		t.Fatalf("second step = %#v", next)
	}
}

func TestStepTraceTrackerIgnoresPublicStepEndAndUnfinishedAttempts(t *testing.T) {
	t.Parallel()

	tracker := newStepTraceTracker(nil)
	tracker.observe(providerStepEnd(1, 2, 3, sdk.Usage{}, "stop"))
	tracker.observeProvider(native.StreamEvent{Type: native.EventStepStart, Timing: &native.StepTiming{StartedAtMS: 5}})
	tracker.observeProvider(native.StreamEvent{Type: native.EventRetry})
	if traces := tracker.take(); len(traces) != 0 {
		t.Fatalf("traces = %#v, want none", traces)
	}
}

func TestStepTraceTrackerCheckpointKeepsStepsAcrossProviderCalls(t *testing.T) {
	t.Parallel()

	tracker := newStepTraceTracker(nil)
	cfg := native.RunConfig{}
	configureNativeStepTrace(&cfg, tracker, nil)

	cfg.OnProviderStreamEventObserved(providerStepEnd(1_000, 1_100, 2_000, sdk.Usage{OutputTokens: 3}, "tool-calls"))
	if err := cfg.OnStepCommitted(context.Background(), 0, &sdk.StepResult{}); err != nil {
		t.Fatalf("commit: %v", err)
	}
	cfg.OnProviderStreamEventObserved(native.StreamEvent{Type: native.EventRetry})
	cfg.OnProviderStreamEventObserved(providerStepEnd(3_000, 3_100, 4_000, sdk.Usage{OutputTokens: 4}, "stop"))
	if err := cfg.OnStepCommitted(context.Background(), 1, &sdk.StepResult{}); err != nil {
		t.Fatalf("commit: %v", err)
	}

	traces := tracker.take()
	if len(traces) != 2 || traces[0].StepIndex != 0 || traces[1].StepIndex != 1 {
		t.Fatalf("traces = %#v", traces)
	}
}

func TestStepTraceTrackerRunTraceRollsUpStepsAndTools(t *testing.T) {
	t.Parallel()

	clock := newReasoningTimingTestClock()
	tracker := newStepTraceTracker(clock.read)
	tracker.observe(native.StreamEvent{Type: native.EventAgentStart})
	tracker.observeProvider(providerStepEnd(1_000, 1_400, 3_000, sdk.Usage{
		InputTokens: 100, OutputTokens: 20,
		InputTokenDetails: sdk.InputTokenDetail{CacheReadTokens: 60, CacheWriteTokens: 5},
	}, "tool-calls"))
	tracker.observe(native.StreamEvent{Type: native.EventToolCallStart, ToolCallID: "c1"})
	tracker.observe(native.StreamEvent{Type: native.EventToolCallEnd, ToolCallID: "c1", Metadata: map[string]any{
		event.ExecutionTimingMetadataKey: event.ExecutionTiming{StartedAtMS: 3_100, EndedAtMS: 3_700},
	}})
	tracker.observeProvider(providerStepEnd(4_000, 0, 6_000, sdk.Usage{InputTokens: 200, OutputTokens: 30, ReasoningTokens: 7}, "stop"))
	tracker.checkpoint()
	clock.advance(time.Second)
	tracker.observe(native.StreamEvent{Type: native.EventAgentEnd})

	got := tracker.runTrace()
	if got == nil {
		t.Fatalf("run trace missing")
	}
	want := runTraceFields{
		Steps: 2, ToolCalls: 1, LLMMs: 4_000, ToolMs: 600, TTFTMs: 400, DecodeMs: 1_600, DecodeOutputTokens: 20,
		InputTokens: 300, CachedInputTokens: 60, CacheWriteTokens: 5, OutputTokens: 50, ReasoningTokens: 7,
	}
	if fields := runTraceFieldsOf(got); *fields != want {
		t.Fatalf("run trace = %#v, want %#v", *fields, want)
	}
	if got.StartedAtMS != clock.read().Add(-time.Second).UnixMilli() || got.EndedAtMS != clock.read().UnixMilli() {
		t.Fatalf("run bounds = %d..%d", got.StartedAtMS, got.EndedAtMS)
	}
}

func TestStepTraceTrackerFallsBackToObservedToolTimingAndTerminalUsage(t *testing.T) {
	t.Parallel()

	clock := newReasoningTimingTestClock()
	tracker := newStepTraceTracker(clock.read)
	tracker.observe(native.StreamEvent{Type: native.EventAgentStart})
	tracker.observe(native.StreamEvent{Type: native.EventToolCallStart, ToolCallID: "acp-1"})
	clock.advance(700 * time.Millisecond)
	tracker.observe(native.StreamEvent{Type: native.EventToolCallEnd, ToolCallID: "acp-1"})
	usage, _ := json.Marshal(sdk.Usage{InputTokens: 900, OutputTokens: 40, CachedInputTokens: 300})
	tracker.observe(native.StreamEvent{Type: native.EventAgentEnd, Usage: usage})

	got := tracker.runTrace()
	if got == nil || got.Steps != 0 || got.ToolCalls != 1 || got.ToolMs != 700 || got.InputTokens != 900 || got.CachedInputTokens != 300 || got.OutputTokens != 40 {
		t.Fatalf("run trace = %#v", got)
	}
}

func TestStepTraceTrackerRunTraceIsNilWithoutObservations(t *testing.T) {
	t.Parallel()

	if got := newStepTraceTracker(nil).runTrace(); got != nil {
		t.Fatalf("run trace = %#v, want nil", got)
	}
}

func TestStoreRoundAttachesStepTraceToAssistantRows(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	service := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
	first := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{ToolCallID: "c1", ToolName: "exec", Input: map[string]any{}}}}
	tool := sdk.Message{Role: sdk.MessageRoleTool, Content: []sdk.MessagePart{sdk.ToolResultPart{ToolCallID: "c1", ToolName: "exec", Result: "ok"}}}
	second := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.TextPart{Text: "answer"}}}
	round := append([]ModelMessage{{Role: "user", Content: newTextContent("hello")}}, sdkMessagesToModelMessages([]sdk.Message{first, tool, second})...)
	traces := []messagepkg.StepTraceMetadata{
		{Version: messagepkg.StepTraceVersion, StepIndex: 0, StartedAtMS: 1, EndedAtMS: 2, FinishReason: "tool-calls"},
		{Version: messagepkg.StepTraceVersion, StepIndex: 1, StartedAtMS: 3, EndedAtMS: 4, FinishReason: "stop"},
	}

	_, err := service.storeRoundWithOptionsResult(t.Context(), ChatRequest{
		BotID: "bot-1", ThreadID: "session-1", Query: "hello", UserMessagePersisted: true,
	}, round, "model-1", storeRoundOptions{StepTraces: traces})
	if err != nil {
		t.Fatalf("storeRoundWithOptionsResult() error = %v", err)
	}
	var assistants []map[string]any
	for _, persisted := range messages.persisted {
		if persisted.Role == "assistant" {
			assistants = append(assistants, persisted.Metadata)
		}
	}
	if len(assistants) != 2 {
		t.Fatalf("persisted assistant rows = %d, want 2", len(assistants))
	}
	for i, metadata := range assistants {
		trace := messagepkg.StepTraceFromMetadata(metadata)
		if trace == nil || trace.StepIndex != i || trace.FinishReason != traces[i].FinishReason {
			t.Fatalf("assistant %d step trace = %#v", i, trace)
		}
	}
}

type runTraceFields struct {
	Steps, ToolCalls                                 int
	LLMMs, ToolMs, TTFTMs, DecodeMs                  int64
	DecodeOutputTokens                               int
	InputTokens, CachedInputTokens, CacheWriteTokens int
	OutputTokens, ReasoningTokens                    int
}

func runTraceFieldsOf(trace *contextfrag.RunTrace) *runTraceFields {
	return &runTraceFields{
		Steps: trace.Steps, ToolCalls: trace.ToolCalls, LLMMs: trace.LLMMs, ToolMs: trace.ToolMs, TTFTMs: trace.TTFTMs,
		DecodeMs: trace.DecodeMs, DecodeOutputTokens: trace.DecodeOutputTokens, InputTokens: trace.InputTokens,
		CachedInputTokens: trace.CachedInputTokens, CacheWriteTokens: trace.CacheWriteTokens,
		OutputTokens: trace.OutputTokens, ReasoningTokens: trace.ReasoningTokens,
	}
}

func TestStepTraceTrackerDiscardsFinishedStepOnRetryBeforeCommit(t *testing.T) {
	t.Parallel()

	tracker := newStepTraceTracker(nil)
	tracker.observeProvider(providerStepEnd(1_000, 1_100, 2_000, sdk.Usage{OutputTokens: 3}, "tool-calls"))
	tracker.observeProvider(native.StreamEvent{Type: native.EventRetry})
	tracker.observeProvider(providerStepEnd(3_000, 3_100, 4_000, sdk.Usage{OutputTokens: 4}, "stop"))

	traces := tracker.take()
	if len(traces) != 1 || traces[0].StepIndex != 0 || traces[0].StartedAtMS != 3_000 {
		t.Fatalf("traces = %#v, want only the surviving attempt at index 0", traces)
	}
	got := tracker.runTrace()
	if got == nil || got.Steps != 1 || got.LLMMs != 1_000 || got.OutputTokens != 4 {
		t.Fatalf("run trace = %#v, want the surviving attempt only", got)
	}
}

func TestStepTraceTrackerCheckpointSurvivesLaterRetry(t *testing.T) {
	t.Parallel()

	tracker := newStepTraceTracker(nil)
	tracker.observeProvider(providerStepEnd(1_000, 1_100, 2_000, sdk.Usage{OutputTokens: 3}, "tool-calls"))
	tracker.checkpoint()
	tracker.observeProvider(native.StreamEvent{Type: native.EventRetry})
	tracker.observeProvider(providerStepEnd(3_000, 3_100, 4_000, sdk.Usage{OutputTokens: 4}, "stop"))

	traces := tracker.take()
	if len(traces) != 2 || traces[0].StepIndex != 0 || traces[1].StepIndex != 1 {
		t.Fatalf("traces = %#v, want both steps", traces)
	}
	if got := tracker.runTrace(); got == nil || got.Steps != 2 {
		t.Fatalf("run trace = %#v", got)
	}
}

func TestWithStepTraceMetadataSkipsEmptyAssistantRowsWithoutShiftingTraces(t *testing.T) {
	t.Parallel()

	empty := sdkMessagesToModelMessages([]sdk.Message{{Role: sdk.MessageRoleAssistant}})
	text := sdkMessagesToModelMessages([]sdk.Message{sdk.AssistantMessage("answer")})
	messages := slices.Concat(empty, text)
	opts := storeRoundOptions{StepTraces: []messagepkg.StepTraceMetadata{
		{Version: messagepkg.StepTraceVersion, StepIndex: 0, StartedAtMS: 1, EndedAtMS: 2},
		{Version: messagepkg.StepTraceVersion, StepIndex: 1, StartedAtMS: 3, EndedAtMS: 4},
	}}.withStepTraceMetadata(messages)
	if _, ok := opts.MessageMetadataByIndex[0]; ok {
		t.Fatalf("empty assistant row received a trace: %#v", opts.MessageMetadataByIndex)
	}
	trace := messagepkg.StepTraceFromMetadata(opts.MessageMetadataByIndex[1])
	if trace == nil || trace.StepIndex != 1 {
		t.Fatalf("text row trace = %#v, want step 1", trace)
	}
}

func TestContextLifecycleRowCopyOmitsRunTrace(t *testing.T) {
	t.Parallel()

	holder := contextfrag.NewLifecycleHolder()
	manifest := contextfrag.BuildManifest(nil)
	manifest.ToolDefs = []contextfrag.ToolDefAccounting{{Provider: "workspace", Name: "exec", TokenEstimate: 22, ContentHash: "tool-exec"}}
	holder.SetManifest(manifest)
	holder.SetRunTraceSource(func() *contextfrag.RunTrace { return &contextfrag.RunTrace{Steps: 1} })
	messages := sdkMessagesToModelMessages([]sdk.Message{sdk.AssistantMessage("answer")})
	opts := storeRoundOptions{ContextLifecycle: holder}.withContextLifecycleMetadata(slog.New(slog.DiscardHandler), ChatRequest{}, messages)
	rowCopy, ok := opts.MessageMetadataByIndex[0][contextfrag.MetadataContextLifecycleKey].(contextfrag.LifecycleSnapshot)
	if !ok || rowCopy.RunTrace != nil || len(rowCopy.ToolDefs) != 1 || rowCopy.ToolDefs[0].ContentHash != "" {
		t.Fatalf("row lifecycle copy = %#v, want no run trace and no tool hashes", opts.MessageMetadataByIndex[0])
	}
	if snapshot, _ := holder.Snapshot(); snapshot.RunTrace == nil {
		t.Fatalf("terminal snapshot lost its run trace")
	}
}

func TestStoreRoundDropsTracesOfFilteredAssistantRows(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	service := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
	round := append([]ModelMessage{{Role: "user", Content: newTextContent("hello")}},
		sdkMessagesToModelMessages([]sdk.Message{{Role: sdk.MessageRoleAssistant}, sdk.AssistantMessage("answer")})...)
	traces := []messagepkg.StepTraceMetadata{
		{Version: messagepkg.StepTraceVersion, StepIndex: 0, StartedAtMS: 1, EndedAtMS: 2},
		{Version: messagepkg.StepTraceVersion, StepIndex: 1, StartedAtMS: 3, EndedAtMS: 4},
	}
	_, err := service.storeRoundWithOptionsResult(t.Context(), ChatRequest{
		BotID: "bot-1", ThreadID: "session-1", Query: "hello", UserMessagePersisted: true,
	}, round, "model-1", storeRoundOptions{StepTraces: traces})
	if err != nil {
		t.Fatalf("storeRoundWithOptionsResult() error = %v", err)
	}
	var assistant []messagepkg.PersistInput
	for _, persisted := range messages.persisted {
		if persisted.Role == "assistant" {
			assistant = append(assistant, persisted)
		}
	}
	if len(assistant) != 1 {
		t.Fatalf("assistant rows = %#v, want the non-empty one", assistant)
	}
	trace := messagepkg.StepTraceFromMetadata(assistant[0].Metadata)
	if trace == nil || trace.StepIndex != 1 {
		t.Fatalf("trace = %#v, want the second step on the surviving row", trace)
	}
}
