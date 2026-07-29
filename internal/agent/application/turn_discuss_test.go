package application

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/agent/runtime/native"
	"github.com/memohai/memoh/internal/agent/turn"
	sessionpkg "github.com/memohai/memoh/internal/chat/thread"
	"github.com/memohai/memoh/internal/chat/timeline"
)

type fakeAgentStreamer struct {
	lastConfig *native.RunConfig
}

func (f *fakeAgentStreamer) Stream(_ context.Context, cfg native.RunConfig) <-chan native.StreamEvent {
	f.lastConfig = &cfg
	ch := make(chan native.StreamEvent, 1)
	ch <- native.StreamEvent{
		Type:     native.EventAgentEnd,
		Messages: json.RawMessage(`[{"role":"assistant","content":"done"}]`),
	}
	close(ch)
	return ch
}

type fakeDiscussService struct {
	resolveResult ResolveRunConfigResult
	inlineFn      func(ctx context.Context, botID string, refs []timeline.ImageAttachmentRef) []sdk.ImagePart
	storeCalls    int
}

func (f *fakeDiscussService) ResolveRunConfig(_ context.Context, _, _, _, _, _, _, _ string) (ResolveRunConfigResult, error) {
	return f.resolveResult, nil
}

func (f *fakeDiscussService) InlineImageAttachments(ctx context.Context, botID string, refs []timeline.ImageAttachmentRef) []sdk.ImagePart {
	if f.inlineFn != nil {
		return f.inlineFn(ctx, botID, refs)
	}
	return nil
}

func (f *fakeDiscussService) StoreRound(_ context.Context, _, _, _, _ string, _ []sdk.Message, _ string) error {
	f.storeCalls++
	return nil
}

type testAgentStreamer interface {
	Stream(context.Context, native.RunConfig) <-chan native.StreamEvent
}

type testDiscussService interface {
	ResolveRunConfig(context.Context, string, string, string, string, string, string, string) (ResolveRunConfigResult, error)
	InlineImageAttachments(context.Context, string, []timeline.ImageAttachmentRef) []sdk.ImagePart
	StoreRound(context.Context, string, string, string, string, []sdk.Message, string) error
}

func newDiscussTestService(streamer testChatStreamer, agent testAgentStreamer, resolver testDiscussService) *Service {
	service := newTurnTestService(streamer)
	service.turnHooks.streamAgent = agent.Stream
	service.turnHooks.resolveRunConfig = resolver.ResolveRunConfig
	service.turnHooks.inlineImages = resolver.InlineImageAttachments
	service.turnHooks.storeRound = resolver.StoreRound
	return service
}

func drainDiscuss(t *testing.T, h turn.RunHandle) []turn.Event {
	t.Helper()
	var events []turn.Event
	for e := range h.Events() {
		events = append(events, e)
	}
	for range h.Errs() {
	}
	return events
}

func discussCommand() turn.StartTurnCommand {
	return turn.StartTurnCommand{
		SchemaVersion: 1,
		TeamID:        "team-1",
		Mode:          turn.ModeDiscuss,
		BotID:         "bot-1",
		ThreadID:      "sess-1",
		DiscussMessages: []turn.DiscussMessage{
			{Role: "user", Content: `<message id="1">photo</message>`},
		},
		DiscussAddressed: true,
	}
}

func TestDiscussInlinesImages(t *testing.T) {
	agent := &fakeAgentStreamer{}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{
			RunConfig: native.RunConfig{SupportsImageInput: true},
			ModelID:   "model-1",
		},
		inlineFn: func(_ context.Context, _ string, refs []timeline.ImageAttachmentRef) []sdk.ImagePart {
			if len(refs) != 1 || refs[0].ContentHash != "img-hash" {
				t.Fatalf("unexpected refs: %v", refs)
			}
			return []sdk.ImagePart{{Image: "data:image/jpeg;base64,FAKE", MediaType: "image/jpeg"}}
		},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	cmd := discussCommand()
	cmd.DiscussImageRefs = []turn.DiscussImageRef{{ContentHash: "img-hash", Mime: "image/jpeg"}}

	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, h)

	if agent.lastConfig == nil {
		t.Fatal("expected agent to be called")
	}
	var userMsgs []sdk.Message
	for _, m := range agent.lastConfig.Messages {
		if m.Role == sdk.MessageRoleUser {
			userMsgs = append(userMsgs, m)
		}
	}
	if len(userMsgs) != 1 {
		t.Fatalf("expected only the canonical RC user message, got %d", len(userMsgs))
	}
	hasImage := false
	for _, part := range userMsgs[0].Content {
		if imgPart, ok := part.(sdk.ImagePart); ok {
			hasImage = true
			if !strings.HasPrefix(imgPart.Image, "data:image/jpeg;base64,") {
				t.Fatalf("unexpected image data: %q", imgPart.Image)
			}
		}
	}
	if !hasImage {
		t.Fatal("expected image part in RC user message")
	}
	if resolver.storeCalls != 1 {
		t.Fatalf("store calls = %d, want 1 after terminal agent_end", resolver.storeCalls)
	}
}

func TestDiscussNoInlineWhenNoVision(t *testing.T) {
	agent := &fakeAgentStreamer{}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{
			RunConfig: native.RunConfig{SupportsImageInput: false},
			ModelID:   "model-1",
		},
		inlineFn: func(_ context.Context, _ string, _ []timeline.ImageAttachmentRef) []sdk.ImagePart {
			t.Fatal("should not be called when model doesn't support vision")
			return nil
		},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	cmd := discussCommand()
	cmd.DiscussImageRefs = []turn.DiscussImageRef{{ContentHash: "img-hash", Mime: "image/jpeg"}}

	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, h)

	if agent.lastConfig == nil {
		t.Fatal("expected agent to be called")
	}
	for _, m := range agent.lastConfig.Messages {
		for _, part := range m.Content {
			if _, ok := part.(sdk.ImagePart); ok {
				t.Fatal("should not have image parts when vision is not supported")
			}
		}
	}
}

func TestDiscussACPUsesChatStreamer(t *testing.T) {
	agent := &fakeAgentStreamer{}
	runner := &fakeRunner{chunks: []string{`{"type":"agent_end"}`}}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{RuntimeType: sessionpkg.RuntimeACPAgent},
	}
	a := newDiscussTestService(runner, agent, resolver)
	cmd := discussCommand()
	cmd.RouteID = "route-1"
	cmd.SourceChannelIdentityID = "acct-1"
	cmd.CurrentChannel = "telegram"
	cmd.ReplyTarget = "chat-1"
	cmd.ConversationType = "group"
	cmd.UserID = "user-1"
	cmd.SessionToken = "Bearer owner-token"
	cmd.ChatToken = "chat-token"
	cmd.ToolHTTPURL = "http://example.test/bots/bot-1/tools"
	cmd.DiscussMessages = []turn.DiscussMessage{{Role: "user", Content: "please inspect the app"}}

	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	events := drainDiscuss(t, h)

	if agent.lastConfig != nil {
		t.Fatal("ordinary agent should not be invoked for ACP discuss runtime")
	}
	req := runner.gotReq
	if req.BotID != "bot-1" || req.ThreadID != "sess-1" || req.SourceChannelIdentityID != "acct-1" {
		t.Fatalf("runtime request = %#v", req)
	}
	if req.RouteID != "route-1" || req.ChatToken != "chat-token" || req.Token != "Bearer owner-token" {
		t.Fatalf("runtime context = route %q chat token %q token %q", req.RouteID, req.ChatToken, req.Token)
	}
	if req.UserID != "user-1" {
		t.Fatalf("runtime user id = %q, want user-1", req.UserID)
	}
	if req.ToolHTTPURL != "http://example.test/bots/bot-1/tools" {
		t.Fatalf("ToolHTTPURL = %q", req.ToolHTTPURL)
	}
	if !strings.Contains(req.Query, "please inspect the app") || !strings.Contains(req.Query, "reset each turn") || !strings.Contains(req.Query, "MUST use the `send` tool") {
		t.Fatalf("runtime query = %q, want full discuss context", req.Query)
	}
	if strings.Contains(req.Query, "Current time:") || strings.Contains(req.Query, "addressed directly") {
		t.Fatalf("runtime query contains volatile late-binding context: %q", req.Query)
	}
	if strings.Index(req.Query, "MUST use the `send` tool") > strings.Index(req.Query, "please inspect the app") {
		t.Fatalf("ACP send contract must stay in the stable preamble: %q", req.Query)
	}
	if !req.UserMessagePersisted {
		t.Fatal("runtime request should avoid duplicating the full-context prompt as a user history message")
	}
	if !req.ForceFreshRuntime {
		t.Fatal("discuss ACP runtime request should force a fresh runtime each turn")
	}
	var sawTerminal bool
	for _, e := range events {
		if e.Kind == "agent_end" {
			sawTerminal = true
		}
	}
	if !sawTerminal {
		t.Fatal("expected terminal agent_end event forwarded from the runtime")
	}
}

func TestDiscussACPTriggersCompaction(t *testing.T) {
	agent := &fakeAgentStreamer{}
	runner := &fakeRunner{chunks: []string{`{"type":"agent_end"}`}}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{RuntimeType: sessionpkg.RuntimeACPAgent},
	}
	a := newDiscussTestService(runner, agent, resolver)
	var (
		called      int
		gotBotID    string
		gotThreadID string
		gotUserID   string
		gotTokens   int
		gotBudget   int
	)
	a.turnHooks.compactDiscuss = func(_ context.Context, botID, threadID, userID string, compactable, budget int) {
		called++
		gotBotID = botID
		gotThreadID = threadID
		gotUserID = userID
		gotTokens = compactable
		gotBudget = budget
	}
	cmd := discussCommand()
	cmd.UserID = "user-1"
	cmd.DiscussMessages = []turn.DiscussMessage{{Role: "user", Content: strings.Repeat("x", 400)}}

	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, h)

	if called != 1 {
		t.Fatalf("compaction triggers = %d, want 1", called)
	}
	if gotBotID != "bot-1" || gotThreadID != "sess-1" || gotUserID != "user-1" {
		t.Fatalf("compaction scope = %q/%q/%q", gotBotID, gotThreadID, gotUserID)
	}
	if gotTokens != 200 || gotBudget != 0 {
		t.Fatalf("compaction pressure = %d tokens, budget %d", gotTokens, gotBudget)
	}
}

func TestDiscussACPSkipsWhenNotAddressed(t *testing.T) {
	agent := &fakeAgentStreamer{}
	runner := &fakeRunner{chunks: []string{`{"type":"agent_end"}`}}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{RuntimeType: sessionpkg.RuntimeACPAgent},
	}
	a := newDiscussTestService(runner, agent, resolver)
	compactionTriggered := false
	a.turnHooks.compactDiscuss = func(context.Context, string, string, string, int, int) {
		compactionTriggered = true
	}
	cmd := discussCommand()
	cmd.DiscussAddressed = false

	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	events := drainDiscuss(t, h)

	if runner.gotReq.BotID != "" {
		t.Fatal("runtime must not start for a passive (unaddressed) message")
	}
	var sawSkip bool
	for _, e := range events {
		if e.Kind == turn.DiscussEventSkipped {
			sawSkip = true
		}
	}
	if !sawSkip {
		t.Fatal("expected skip marker event")
	}
	if compactionTriggered {
		t.Fatal("passive ACP discuss turn must not trigger compaction")
	}
}

func TestDiscussNativeTrimsContextToModelBudget(t *testing.T) {
	agent := &fakeAgentStreamer{}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{
			ContextTokenBudget: 40,
			ModelID:            "model-1",
		},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	cmd := discussCommand()
	cmd.DiscussMessages = []turn.DiscussMessage{
		{Role: "user", Content: "<summary>\ncompacted window\n</summary>", CompactionArtifactID: "artifact-1"},
		{Role: "assistant", Content: strings.Repeat("o", 400)},
		{Role: "user", Content: "recent question"},
	}

	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, h)

	if agent.lastConfig == nil {
		t.Fatal("expected agent to be invoked")
	}
	if len(agent.lastConfig.Messages) != 2 {
		t.Fatalf("trimmed messages = %d, want summary + recent question", len(agent.lastConfig.Messages))
	}
	if got := sdkMessageText(agent.lastConfig.Messages[0]); !strings.Contains(got, "compacted window") {
		t.Fatalf("first message = %q, want pinned summary", got)
	}
	if got := sdkMessageText(agent.lastConfig.Messages[1]); got != "recent question" {
		t.Fatalf("last message = %q, want recent question", got)
	}
}

func TestDiscussNativeHonorsResolvedMessageTokenBudget(t *testing.T) {
	agent := &fakeAgentStreamer{}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{
			ContextTokenBudget:        1000000,
			DiscussMessageTokenBudget: 100,
			ModelID:                   "model-1",
		},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	cmd := discussCommand()
	cmd.DiscussMessages = []turn.DiscussMessage{
		{Role: "assistant", Content: strings.Repeat("o", 400)},
		{Role: "user", Content: "recent question"},
	}

	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, h)

	if agent.lastConfig == nil {
		t.Fatal("expected agent to be invoked")
	}
	if len(agent.lastConfig.Messages) != 1 {
		t.Fatalf("trimmed messages = %d, want only recent question", len(agent.lastConfig.Messages))
	}
	if got := sdkMessageText(agent.lastConfig.Messages[0]); got != "recent question" {
		t.Fatalf("retained message = %q, want recent question", got)
	}
}

func TestEffectiveDiscussMessageTokenBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		contextTokenBudget  int
		compactionEnabled   bool
		compactionThreshold int
		want                int
	}{
		{
			name:                "enabled threshold caps large model window",
			contextTokenBudget:  1000000,
			compactionEnabled:   true,
			compactionThreshold: 100000,
			want:                100000,
		},
		{
			name:                "disabled compaction uses reserved model budget",
			contextTokenBudget:  128000,
			compactionEnabled:   false,
			compactionThreshold: 100000,
			want:                89600,
		},
		{
			name:                "higher threshold does not expand model budget",
			contextTokenBudget:  100000,
			compactionEnabled:   true,
			compactionThreshold: 100000,
			want:                70000,
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveDiscussMessageTokenBudget(
				tt.contextTokenBudget,
				tt.compactionEnabled,
				tt.compactionThreshold,
			); got != tt.want {
				t.Fatalf("effectiveDiscussMessageTokenBudget() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTrimDiscussMessagesUsesReservedBudgetAndLogs(t *testing.T) {
	messages := []turn.DiscussMessage{
		{Role: "assistant", Content: strings.Repeat("o", 80)},
		{Role: "user", Content: strings.Repeat("r", 240)},
	}
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))

	trimmed := trimDiscussMessagesByTokens(log, messages, 100)

	if len(trimmed) != 1 || trimmed[0].Role != "user" {
		t.Fatalf("trimmed messages = %#v, want only recent user message", trimmed)
	}
	output := logs.String()
	for _, want := range []string{
		"level=WARN",
		"dropped_messages=1",
		"estimated_tokens=160",
		"message_token_budget=70",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("log output = %q, want %q", output, want)
		}
	}
}

func TestTrimDiscussMessagesPreservesLatestTurnBeforeToolOnlySuffix(t *testing.T) {
	messages := []turn.DiscussMessage{
		{Role: "assistant", Content: strings.Repeat("o", 400)},
		{Role: "user", Content: "latest question"},
		{Role: "assistant", Content: strings.Repeat("c", 120)},
		{Role: "tool", Content: "done"},
	}

	trimmed := trimDiscussMessagesByTokens(nil, messages, 40)

	if len(trimmed) != 3 {
		t.Fatalf("trimmed messages = %d, want latest user/assistant/tool turn", len(trimmed))
	}
	if trimmed[0].Role != "user" || trimmed[0].Content != "latest question" {
		t.Fatalf("first retained message = %#v, want latest user message", trimmed[0])
	}
	if trimmed[2].Role != "tool" {
		t.Fatalf("last retained message = %#v, want tool result", trimmed[2])
	}
}

func TestDiscussRefreshesContextFragWithoutLateBindingMessage(t *testing.T) {
	agent := &fakeAgentStreamer{}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{
			RunConfig: native.RunConfig{System: "base system"},
			ModelID:   "model-1",
		},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	cmd := discussCommand()
	cmd.DiscussMessages = []turn.DiscussMessage{{Role: "user", Content: `<message id="x">hello</message>`}}

	h, err := a.StartTurn(context.Background(), cmd)
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, h)

	if agent.lastConfig == nil {
		t.Fatal("expected agent to be invoked")
	}
	cfg := agent.lastConfig
	if cfg.ContextManifest.Counts.Messages != len(cfg.Messages) {
		t.Fatalf("manifest message count = %d, messages = %d", cfg.ContextManifest.Counts.Messages, len(cfg.Messages))
	}
	if len(cfg.Messages) != 1 {
		t.Fatalf("messages = %d, want only composed discuss context", len(cfg.Messages))
	}
	if lastMessageFragContains(cfg.ContextFrags, "Current time:") ||
		lastMessageFragContains(cfg.ContextFrags, "MUST use the `send` tool") {
		t.Fatalf("context frags include a volatile late-binding prompt: %#v", cfg.ContextManifest.Items)
	}
}

func TestInjectImagePartsIntoLastUserMessage(t *testing.T) {
	msgs := []sdk.Message{
		sdk.UserMessage("hello"),
		sdk.AssistantMessage("hi"),
		sdk.UserMessage("look at this"),
	}
	parts := []sdk.ImagePart{
		{Image: "data:image/png;base64,abc", MediaType: "image/png"},
	}

	injectImagePartsIntoLastUserMessage(msgs, parts)

	lastUser := msgs[2]
	if len(lastUser.Content) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(lastUser.Content))
	}
	imgPart, ok := lastUser.Content[1].(sdk.ImagePart)
	if !ok {
		t.Fatalf("expected ImagePart, got %T", lastUser.Content[1])
	}
	if imgPart.Image != "data:image/png;base64,abc" {
		t.Fatalf("unexpected image: %q", imgPart.Image)
	}
}

func TestInjectImagePartsIntoLastUserMessage_Empty(t *testing.T) {
	msgs := []sdk.Message{sdk.UserMessage("hello")}
	injectImagePartsIntoLastUserMessage(msgs, nil)
	if len(msgs[0].Content) != 1 {
		t.Fatalf("expected no change, got %d parts", len(msgs[0].Content))
	}
}

func TestInjectImagePartsIntoLastUserMessage_SkipsEmptyImage(t *testing.T) {
	msgs := []sdk.Message{sdk.UserMessage("hello")}
	parts := []sdk.ImagePart{{Image: "", MediaType: "image/png"}}
	injectImagePartsIntoLastUserMessage(msgs, parts)
	if len(msgs[0].Content) != 1 {
		t.Fatalf("expected no change, got %d parts", len(msgs[0].Content))
	}
}

func lastMessageFragContains(frags []contextfrag.ContextFrag, needle string) bool {
	for i := len(frags) - 1; i >= 0; i-- {
		frag := frags[i]
		if frag.Kind != contextfrag.KindConversationEvent || len(frag.Parts) == 0 || frag.Parts[0].SDKMessage == nil {
			continue
		}
		for _, part := range frag.Parts[0].SDKMessage.Content {
			if text, ok := part.(sdk.TextPart); ok && strings.Contains(text.Text, needle) {
				return true
			}
		}
		return false
	}
	return false
}

func sdkMessageText(message sdk.Message) string {
	var text strings.Builder
	for _, part := range message.Content {
		if value, ok := part.(sdk.TextPart); ok {
			text.WriteString(value.Text)
		}
	}
	return text.String()
}

// TestDiscussCancelUnblocksFullEventBuffer mirrors the chat-mode burst
// repro for the discuss pump's emit path.
func TestDiscussCancelUnblocksFullEventBuffer(t *testing.T) {
	agent := &burstAgentStreamer{count: 40}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{ModelID: "model-1"},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	h, err := a.StartTurn(context.Background(), discussCommand())
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	h.Cancel()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case _, ok := <-h.Events():
			if !ok {
				for range h.Errs() {
				}
				return
			}
		case <-deadline:
			t.Fatal("discuss events channel not closed after cancel with full buffer")
		}
	}
}

type burstAgentStreamer struct {
	count int
}

func (f *burstAgentStreamer) Stream(ctx context.Context, _ native.RunConfig) <-chan native.StreamEvent {
	ch := make(chan native.StreamEvent)
	go func() {
		defer close(ch)
		for range f.count {
			select {
			case ch <- native.StreamEvent{Type: native.EventTextDelta, Delta: "x"}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch
}
