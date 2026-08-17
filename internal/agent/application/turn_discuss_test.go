package application

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/memohai/memoh/internal/agent/runtime/session"
	"github.com/memohai/memoh/internal/agent/turn"
	"github.com/memohai/memoh/internal/apperror"
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
	resolveResult  ResolveRunConfigResult
	inlineFn       func(ctx context.Context, botID string, refs []timeline.ImageAttachmentRef) []sdk.ImagePart
	storeCalls     int
	storedMessages []sdk.Message
	lastStoreRunID string
	lastLifecycle  *contextfrag.LifecycleHolder
	storeErr       error
	storeFn        func() error
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

func (f *fakeDiscussService) StoreRound(_ context.Context, runID, _, _, _, _ string, messages []sdk.Message, _ string, lifecycle *contextfrag.LifecycleHolder) error {
	f.storeCalls++
	f.storedMessages = append([]sdk.Message(nil), messages...)
	f.lastStoreRunID = runID
	f.lastLifecycle = lifecycle
	if f.storeFn != nil {
		return f.storeFn()
	}
	return f.storeErr
}

type testAgentStreamer interface {
	Stream(context.Context, native.RunConfig) <-chan native.StreamEvent
}

type testDiscussService interface {
	ResolveRunConfig(context.Context, string, string, string, string, string, string, string) (ResolveRunConfigResult, error)
	InlineImageAttachments(context.Context, string, []timeline.ImageAttachmentRef) []sdk.ImagePart
	StoreRound(context.Context, string, string, string, string, string, []sdk.Message, string, *contextfrag.LifecycleHolder) error
}

func newDiscussTestService(streamer testChatStreamer, agent testAgentStreamer, resolver testDiscussService) *Service {
	service := newTurnTestService(streamer)
	service.turnHooks.streamAgent = agent.Stream
	service.turnHooks.resolveRunConfig = resolver.ResolveRunConfig
	service.turnHooks.inlineImages = resolver.InlineImageAttachments
	service.turnHooks.storeRound = resolver.StoreRound
	return service
}

func configureDiscussLifecycle(service *Service) (*lifecycleTurnAdmitter, *recordingContextLifecycleStore) {
	runtime := &lifecycleTurnAdmitter{admission: lifecycleTestAdmission()}
	store := &recordingContextLifecycleStore{}
	resolve := service.turnHooks.resolveRunConfig
	service.turnHooks.resolveRunConfig = func(ctx context.Context, botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken string) (ResolveRunConfigResult, error) {
		resolved, err := resolve(ctx, botID, sessionID, channelIdentityID, currentPlatform, replyTarget, conversationType, chatToken)
		resolved.RunConfig.Identity = native.SessionContext{BotID: botID, SessionID: sessionID}
		return resolved, err
	}
	service.sessionRuntime = runtime
	service.contextLifecycles = store
	service.publishTurnEvent = nil
	return runtime, store
}

func lifecycleDiscussCommand() turn.StartTurnCommand {
	cmd := discussCommand()
	cmd.BotID = lifecycleTestBotID
	cmd.ThreadID = lifecycleTestSessionID
	return cmd
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

func TestDiscussUsesAdmittedRunIDInNativeConfig(t *testing.T) {
	agent := &fakeAgentStreamer{}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{
			RunConfig: native.RunConfig{},
			ModelID:   "model-1",
		},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	_, lifecycles := configureDiscussLifecycle(a)

	h, err := a.StartTurn(context.Background(), lifecycleDiscussCommand())
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, h)

	if agent.lastConfig == nil {
		t.Fatal("expected agent to be called")
	}
	if got := agent.lastConfig.RunID; got != h.RunID() {
		t.Fatalf("native RunID = %q, want admitted run ID %q", got, h.RunID())
	}
	if got := resolver.lastStoreRunID; got != h.RunID() {
		t.Fatalf("persisted RunID = %q, want admitted run ID %q", got, h.RunID())
	}
	if resolver.lastLifecycle == nil {
		t.Fatal("resolved lifecycle holder did not reach discuss persistence")
	}
	if len(lifecycles.creates) != 1 || lifecycles.creates[0].Status != contextLifecycleStatusCompleted {
		t.Fatalf("lifecycle creates = %#v, want one completed row", lifecycles.creates)
	}
	if got := pgUUIDString(lifecycles.creates[0].RunID); got != h.RunID() {
		t.Fatalf("lifecycle RunID = %q, want admitted run ID %q", got, h.RunID())
	}
}

func TestStoreDiscussRoundPersistsAdmittedRunIDAndLifecycleAssociation(t *testing.T) {
	const admittedRunID = "77777777-7777-4777-8777-777777777777"
	messages := &recordingMessageService{}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.BuildManifest(nil))
	service := &Service{
		messageService: messages,
		logger:         slog.New(slog.DiscardHandler),
	}

	err := service.storeDiscussRound(
		context.Background(),
		admittedRunID,
		lifecycleTestBotID,
		lifecycleTestSessionID,
		"",
		"local",
		[]sdk.Message{sdk.AssistantMessage("done")},
		"model-id",
		holder,
	)
	if err != nil {
		t.Fatalf("storeDiscussRound() error = %v", err)
	}
	if len(messages.persisted) != 1 {
		t.Fatalf("persisted messages = %d, want 1", len(messages.persisted))
	}
	if got := messages.persisted[0].RunID; got != admittedRunID {
		t.Fatalf("persisted discuss RunID = %q, want admitted ID %q", got, admittedRunID)
	}
	if _, ok := messages.persisted[0].Metadata[contextfrag.MetadataContextLifecycleKey].(contextfrag.LifecycleSnapshot); !ok {
		t.Fatalf("assistant metadata = %#v, want lifecycle snapshot", messages.persisted[0].Metadata)
	}
	snapshot, ok := holder.Snapshot()
	if !ok || snapshot.AssistantMessageID != "message-id" {
		t.Fatalf("holder snapshot = %#v, %v; want assistant association", snapshot, ok)
	}
}

func TestAdmittedDiscussCancellationPersistsAbortedLifecycle(t *testing.T) {
	agent := &canceledDiscussTerminalReporter{started: make(chan struct{})}
	resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{ModelID: "model-1"}}
	service := newDiscussTestService(&fakeRunner{}, agent, resolver)
	runtime, lifecycles := configureDiscussLifecycle(service)

	handle, err := service.StartTurn(context.Background(), lifecycleDiscussCommand())
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-agent.started:
	case <-time.After(time.Second):
		t.Fatal("discuss turn did not reach native streaming")
	}
	handle.Cancel()
	drainDiscuss(t, handle)

	if len(lifecycles.creates) != 1 || lifecycles.creates[0].Status != contextLifecycleStatusAborted {
		t.Fatalf("lifecycle creates = %#v, want one aborted row", lifecycles.creates)
	}
	if len(runtime.finishes) != 1 || runtime.finishes[0].status != sessionruntime.RunStatusAborted {
		t.Fatalf("runtime finishes = %#v, want one aborted finish", runtime.finishes)
	}
}

func TestAdmittedDiscussDecisionPauseDoesNotPersistLifecycle(t *testing.T) {
	resolver := &fakeDiscussService{resolveResult: ResolveRunConfigResult{ModelID: "model-1"}}
	service := newDiscussTestService(&fakeRunner{}, &decisionDiscussAgentStreamer{}, resolver)
	runtime, lifecycles := configureDiscussLifecycle(service)

	handle, err := service.StartTurn(context.Background(), lifecycleDiscussCommand())
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, handle)

	if len(lifecycles.creates) != 0 {
		t.Fatalf("lifecycle creates = %#v, want none for a parked decision", lifecycles.creates)
	}
	if len(runtime.finishes) != 1 || runtime.finishes[0].status != "" {
		t.Fatalf("runtime finishes = %#v, want one unnamed parked finish", runtime.finishes)
	}
}

func TestAdmittedDiscussStoreFailurePersistsFailedProviderLifecycle(t *testing.T) {
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{ModelID: "model-1"},
		storeErr:      errors.New("private store failure"),
	}
	service := newDiscussTestService(&fakeRunner{}, &decisionDiscussAgentStreamer{}, resolver)
	runtime, lifecycles := configureDiscussLifecycle(service)

	handle, err := service.StartTurn(context.Background(), lifecycleDiscussCommand())
	if err != nil {
		t.Fatal(err)
	}
	drainDiscuss(t, handle)

	if len(lifecycles.creates) != 1 || lifecycles.creates[0].Status != contextLifecycleStatusFailedProvider {
		t.Fatalf("lifecycle creates = %#v, want one failed_provider row", lifecycles.creates)
	}
	if got := lifecycles.creates[0].ErrorCode.String; got != string(apperror.CodeSessionHistoryInconsistent) {
		t.Fatalf("lifecycle error code = %q, want %q", got, apperror.CodeSessionHistoryInconsistent)
	}
	if len(runtime.finishes) != 1 || runtime.finishes[0].status != sessionruntime.RunStatusErrored {
		t.Fatalf("runtime finishes = %#v, want one errored finish", runtime.finishes)
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
	if req.RunID != h.RunID() {
		t.Fatalf("ACP discuss RunID = %q, want admitted run ID %q", req.RunID, h.RunID())
	}
	if req.RouteID != "route-1" || req.ChatToken != "chat-token" || req.Token != "Bearer owner-token" {
		t.Fatalf("runtime context = route %q chat token %q token %q", req.RouteID, req.ChatToken, req.Token)
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

func TestDiscussACPSkipsWhenNotAddressed(t *testing.T) {
	agent := &fakeAgentStreamer{}
	runner := &fakeRunner{chunks: []string{`{"type":"agent_end"}`}}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{RuntimeType: sessionpkg.RuntimeACPAgent},
	}
	a := newDiscussTestService(runner, agent, resolver)
	_, lifecycles := configureDiscussLifecycle(a)
	cmd := lifecycleDiscussCommand()
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
	if len(lifecycles.creates) != 1 || lifecycles.creates[0].Status != contextLifecycleStatusCompleted {
		t.Fatalf("lifecycle creates = %#v, want one completed row", lifecycles.creates)
	}
}

func TestDiscussRefreshesContextFragWithoutLateBindingMessage(t *testing.T) {
	agent := &fakeAgentStreamer{}
	staleIndex := 0
	staleMemoryIndex := 0
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{
			RunConfig: native.RunConfig{
				System:                         "base system",
				ContextCurrentUserMessageIndex: &staleIndex,
				ContextMemoryMessageIndex:      &staleMemoryIndex,
				ContextSourceFrags: []contextfrag.ContextFrag{{
					ID: "stale-system-only-source", Slot: contextfrag.SlotSystem,
				}},
			},
			ModelID: "model-1",
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
	if cfg.ContextSourceFrags != nil {
		t.Fatalf("discuss retained stale authoritative source: %#v", cfg.ContextSourceFrags)
	}
	if cfg.ContextCurrentUserMessageIndex != nil || cfg.ContextMemoryMessageIndex != nil {
		t.Fatalf("discuss retained stale message markers: current=%#v memory=%#v", cfg.ContextCurrentUserMessageIndex, cfg.ContextMemoryMessageIndex)
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

// TestDiscussCancelUnblocksFullEventBuffer mirrors the chat-mode burst
// repro for the discuss pump's emit path.
func TestDiscussCancelUnblocksFullEventBuffer(t *testing.T) {
	agent := &burstAgentStreamer{count: 40}
	resolver := &fakeDiscussService{
		resolveResult: ResolveRunConfigResult{ModelID: "model-1"},
	}
	a := newDiscussTestService(&fakeRunner{}, agent, resolver)
	admitter := a.sessionRuntime.(*scriptedAdmitter)
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
				if got := admitter.awaitFinish(t); got.status != sessionruntime.RunStatusAborted {
					t.Fatalf("status = %q, want %q", got.status, sessionruntime.RunStatusAborted)
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
