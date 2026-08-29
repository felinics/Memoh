package discuss

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	agentevent "github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
)

func TestExtractNewImageRefs(t *testing.T) {
	rc := timeline.RenderedContext{
		{ReceivedAtMs: 100, ImageRefs: []timeline.ImageAttachmentRef{{ContentHash: "old-hash", Mime: "image/png"}}},
		{ReceivedAtMs: 200, IsMyself: true, ImageRefs: []timeline.ImageAttachmentRef{{ContentHash: "self-hash"}}},
		{ReceivedAtMs: 300, ImageRefs: []timeline.ImageAttachmentRef{{ContentHash: "new-hash", Mime: "image/jpeg"}}},
		{ReceivedAtMs: 400, ImageRefs: nil},
	}

	refs := extractNewImageRefs(rc, timeline.DiscussCursorPosition{SourceCursor: 150})
	if len(refs) != 1 {
		t.Fatalf("expected 1 ref, got %d", len(refs))
	}
	if refs[0].ContentHash != "new-hash" {
		t.Fatalf("expected new-hash, got %q", refs[0].ContentHash)
	}
	if refs[0].Mime != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %q", refs[0].Mime)
	}
}

func TestExtractNewImageRefs_IncludesMultiple(t *testing.T) {
	rc := timeline.RenderedContext{
		{ReceivedAtMs: 100},
		{ReceivedAtMs: 200, ImageRefs: []timeline.ImageAttachmentRef{
			{ContentHash: "a"},
			{ContentHash: "b"},
		}},
		{ReceivedAtMs: 300, ImageRefs: []timeline.ImageAttachmentRef{{ContentHash: "c"}}},
	}
	refs := extractNewImageRefs(rc, timeline.DiscussCursorPosition{SourceCursor: 50})
	if len(refs) != 3 {
		t.Fatalf("expected 3 refs, got %d", len(refs))
	}
}

func TestHandleReplyWithTurn_PassesContextAndImageRefs(t *testing.T) {
	rc := timeline.RenderedContext{
		{
			ReceivedAtMs: 200,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">photo</message>`}},
			ImageRefs:    []timeline.ImageAttachmentRef{{ContentHash: "img-hash", Mime: "image/jpeg"}},
		},
	}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{
		config: DiscussSessionConfig{TeamID: "team-1", BotID: "bot-1", ThreadID: "sess-1"},
	}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatalf("StartTurn calls = %d, want 1", svc.calls)
	}
	cmd := svc.lastCmd
	if cmd.Mode != turn.ModeDiscuss || cmd.TeamID != "team-1" || cmd.BotID != "bot-1" {
		t.Fatalf("cmd = %+v", cmd)
	}
	if len(cmd.DiscussImageRefs) != 1 || cmd.DiscussImageRefs[0].ContentHash != "img-hash" || cmd.DiscussImageRefs[0].Mime != "image/jpeg" {
		t.Fatalf("image refs = %+v", cmd.DiscussImageRefs)
	}
	if len(cmd.DiscussMessages) == 0 {
		t.Fatal("expected composed discuss messages")
	}
}

func TestHandleReplyWithTurn_ACPAdvancesCursorOnCleanTerminal(t *testing.T) {
	rc := timeline.RenderedContext{
		{
			ReceivedAtMs: 200,
			MentionsMe:   true,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">please inspect the app</message>`}},
		},
	}
	svc := &fakeTurnService{runtimeType: sessionpkg.RuntimeACPAgent}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:             "bot-1",
			ThreadID:          "sess-1",
			RouteID:           "route-1",
			ChannelIdentityID: "acct-1",
			CurrentPlatform:   "telegram",
			ReplyTarget:       "chat-1",
			ConversationType:  "group",
			SessionToken:      "Bearer owner-token",
			ChatToken:         "chat-token",
			ToolHTTPURL:       "http://example.test/bots/bot-1/tools",
		},
	}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatalf("StartTurn calls = %d, want 1", svc.calls)
	}
	cmd := svc.lastCmd
	if cmd.SessionToken != "Bearer owner-token" || cmd.ChatToken != "chat-token" || cmd.ToolHTTPURL != "http://example.test/bots/bot-1/tools" {
		t.Fatalf("credentials not passed: %+v", cmd)
	}
	if cmd.RouteID != "route-1" || cmd.SourceChannelIdentityID != "acct-1" {
		t.Fatalf("routing not passed: %+v", cmd)
	}
	if !cmd.DiscussAddressed {
		t.Fatal("mentioned message must be addressed")
	}
	if sess.lastProcessed.SourceCursor != 200 {
		t.Fatalf("lastProcessed = %+v, want source 200", sess.lastProcessed)
	}
}

func TestNotifyRCRefreshesExistingDiscussSessionConfig(t *testing.T) {
	driver := NewDiscussDriver(DiscussDriverDeps{})
	defer driver.StopSession("sess-1")

	driver.NotifyRC(context.Background(), "sess-1", timeline.RenderedContext{}, DiscussSessionConfig{
		BotID:        "bot-1",
		ThreadID:     "sess-1",
		RouteID:      "route-old",
		ChatToken:    "chat-token-old",
		SessionToken: "session-token-old",
		ToolHTTPURL:  "http://old.example/tools",
	})
	driver.NotifyRC(context.Background(), "sess-1", timeline.RenderedContext{}, DiscussSessionConfig{
		BotID:        "bot-1",
		ThreadID:     "sess-1",
		RouteID:      "route-new",
		ChatToken:    "chat-token-new",
		SessionToken: "session-token-new",
		ToolHTTPURL:  "http://new.example/tools",
	})

	driver.mu.Lock()
	got := driver.sessions["sess-1"].config
	driver.mu.Unlock()
	if got.RouteID != "route-new" || got.ChatToken != "chat-token-new" || got.SessionToken != "session-token-new" || got.ToolHTTPURL != "http://new.example/tools" {
		t.Fatalf("config = %#v, want latest NotifyRC config", got)
	}
}

func TestHandleReplyWithTurnReadsConfigUnderDriverLock(t *testing.T) {
	rc := timeline.RenderedContext{
		{
			ReceivedAtMs: 200,
			MentionsMe:   true,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">please inspect the app</message>`}},
		},
	}
	calls := make(chan string, 1)
	svc := &fakeTurnService{onStart: func(cmd turn.StartTurnCommand) {
		calls <- cmd.BotID
	}}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:    "bot-old",
			ThreadID: "sess-1",
		},
	}

	driver.mu.Lock()
	done := make(chan struct{})
	go func() {
		driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)
		close(done)
	}()

	select {
	case got := <-calls:
		driver.mu.Unlock()
		t.Fatalf("turn started before config lock released with bot %q", got)
	case <-time.After(25 * time.Millisecond):
	}

	sess.config = DiscussSessionConfig{
		BotID:    "bot-new",
		ThreadID: "sess-1",
	}
	driver.mu.Unlock()

	select {
	case got := <-calls:
		if got != "bot-new" {
			t.Fatalf("turn bot id = %q, want refreshed config", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for turn start")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for handler")
	}
}

func TestHandleReplyWithTurn_NoCursorAdvanceOnStartError(t *testing.T) {
	rc := timeline.RenderedContext{
		{
			ReceivedAtMs: 200,
			MentionsMe:   true,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">please inspect the app</message>`}},
		},
	}
	svc := &fakeTurnService{startErr: errors.New("discuss runtime not configured")}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{
		config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"},
	}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if sess.lastProcessed != (timeline.DiscussCursorPosition{}) {
		t.Fatalf("lastProcessed = %+v, want zero when the turn cannot start", sess.lastProcessed)
	}
}

func TestHandleReplyWithTurn_ACPDoesNotAdvanceCursorOnRuntimeError(t *testing.T) {
	rc := timeline.RenderedContext{
		{
			ReceivedAtMs: 200,
			MentionsMe:   true,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">please inspect the app</message>`}},
		},
	}
	svc := &fakeTurnService{runtimeType: sessionpkg.RuntimeACPAgent, streamErr: errors.New("runtime failed")}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{
		config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"},
	}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatalf("StartTurn calls = %d, want 1", svc.calls)
	}
	if sess.lastProcessed != (timeline.DiscussCursorPosition{}) {
		t.Fatalf("lastProcessed = %+v, want zero when ACP runtime stream fails", sess.lastProcessed)
	}
}

func TestHandleReplyWithTurn_ACPSkipsRuntimeForPassiveMessage(t *testing.T) {
	// Passive group chatter that does not address the bot must NOT spin up the
	// external ACP runtime, but the consumed cursor must still advance so the
	// same batch is not re-evaluated and stays covered as context next turn.
	rc := timeline.RenderedContext{
		{
			ReceivedAtMs: 200,
			MentionsMe:   false,
			RepliesToMe:  false,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">just chatting amongst ourselves</message>`}},
		},
	}
	svc := &fakeTurnService{runtimeType: sessionpkg.RuntimeACPAgent}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:            "bot-1",
			ThreadID:         "sess-1",
			ConversationType: channel.ConversationTypeGroup,
		},
	}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if svc.lastCmd.DiscussAddressed {
		t.Fatal("passive group message must not be addressed")
	}
	if sess.lastProcessed.SourceCursor != 200 {
		t.Fatalf("lastProcessed = %+v, want source 200 (cursor advanced on silent path)", sess.lastProcessed)
	}
}

func TestHandleReplyWithTurn_ACPRepliesInDirectConversation(t *testing.T) {
	// A direct/1:1 conversation is always addressed, so a DM discuss-ACP session
	// must start the runtime even without an explicit @-mention or reply-to.
	rc := timeline.RenderedContext{
		{
			ReceivedAtMs: 200,
			MentionsMe:   false,
			RepliesToMe:  false,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">hey, can you look at this?</message>`}},
		},
	}
	svc := &fakeTurnService{runtimeType: sessionpkg.RuntimeACPAgent}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:            "bot-1",
			ThreadID:         "sess-1",
			ConversationType: channel.ConversationTypePrivate,
		},
	}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if !svc.lastCmd.DiscussAddressed {
		t.Fatal("direct (1:1) message must be addressed even without a mention")
	}
	if sess.lastProcessed.SourceCursor != 200 {
		t.Fatalf("lastProcessed = %+v, want source 200 (cursor advanced after direct reply)", sess.lastProcessed)
	}
}

func TestAnchorFromTRs(t *testing.T) {
	t.Parallel()

	if got := anchorFromTRs(nil); got != 0 {
		t.Fatalf("empty TRs anchor = %d, want 0", got)
	}
	got := anchorFromTRs([]timeline.TurnResponseEntry{
		{RequestedAtMs: 100},
		{RequestedAtMs: 500},
		{RequestedAtMs: 300},
	})
	if got != 500 {
		t.Fatalf("anchor = %d, want 500", got)
	}
}

// TestHandleReplyWithTurn_ColdStartAnchoredByTR simulates idle-timeout
// restart: the session's in-memory lastProcessedCursor is 0, but RC replay has
// brought back old user messages that were already answered in prior
// LLM rounds (represented by TRs). The driver MUST NOT re-answer them.
func TestHandleReplyWithTurn_ColdStartAnchoredByTR(t *testing.T) {
	rc := timeline.RenderedContext{
		{
			ReceivedAtMs: 100,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="old">task 1</message>`}},
		},
	}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{
		MessageService: nil,
	})
	sess := &discussSession{
		config: DiscussSessionConfig{BotID: "b", ThreadID: "s"},
	}

	// Simulate a previously answered round by pre-stuffing a TR newer than
	// the RC segment's ReceivedAtMs. Since we cannot inject MessageService
	// easily, we instead pre-set lastProcessed as the anchor would.
	sess.lastProcessed = timeline.DiscussCursorPosition{SourceCursor: 200} // mimic anchorFromTRs result

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if svc.calls != 0 {
		t.Fatal("turn must not start when all RC segments predate lastProcessedCursor")
	}
}

// TestHandleReplyWithTurn_CursorAdvancesToRCNotWallClock ensures that after
// a turn we set lastProcessedCursor to the max ReceivedAtMs actually consumed in
// the RC snapshot, not time.Now(). This matters for messages that arrive
// mid-turn: they end up in a fresher RC with ReceivedAtMs > cursor, which
// correctly triggers the next round.
func TestHandleReplyWithTurn_CursorAdvancesToRCNotWallClock(t *testing.T) {
	rc := timeline.RenderedContext{
		{
			ReceivedAtMs: 777,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="x">hello</message>`}},
		},
	}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{
		config: DiscussSessionConfig{BotID: "b", ThreadID: "s"},
	}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatal("expected turn to start")
	}
	if sess.lastProcessed.SourceCursor != 777 {
		t.Fatalf("lastProcessed = %+v, want source 777 (max RC ReceivedAtMs)", sess.lastProcessed)
	}
}

func TestHandleReplyWithTurn_UsesPersistedDiscussCursor(t *testing.T) {
	store := &fakeDiscussCursorStore{position: timeline.DiscussCursorPosition{SourceCursor: 500}}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{
		CursorStore: store,
	})
	sess := &discussSession{
		config: DiscussSessionConfig{
			BotID:           "b",
			ThreadID:        "s",
			RouteID:         "route-1",
			CurrentPlatform: "telegram",
		},
	}

	driver.handleReplyWithTurn(context.Background(), sess, timeline.RenderedContext{
		{ReceivedAtMs: 400, Content: []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="old">old</message>`}}},
	}, driver.logger, svc)

	if svc.calls != 0 {
		t.Fatal("turn must not start for RC covered by persisted cursor")
	}
	if sess.lastProcessed.SourceCursor != 500 {
		t.Fatalf("lastProcessed = %+v, want persisted source 500", sess.lastProcessed)
	}

	driver.handleReplyWithTurn(context.Background(), sess, timeline.RenderedContext{
		{ReceivedAtMs: 700, Content: []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="new">new</message>`}}},
	}, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatal("expected turn to start for RC past persisted cursor")
	}
	if store.upsertPosition.SourceCursor != 700 {
		t.Fatalf("persisted cursor = %+v, want source 700", store.upsertPosition)
	}
	if store.upsertScope != "route:route-1" {
		t.Fatalf("scope = %q, want route:route-1", store.upsertScope)
	}
	if store.upsertRouteID != "route-1" {
		t.Fatalf("route id = %q, want route-1", store.upsertRouteID)
	}
}

func TestAgentEventToChannelEventMapsACPDecisionRequests(t *testing.T) {
	approval, ok := agentEventToChannelEvent(agentevent.StreamEvent{
		Type:       agentevent.ToolApprovalRequest,
		ToolName:   "edit",
		ToolCallID: "call-1",
		ApprovalID: "approval-1",
		ShortID:    7,
		Input:      map[string]any{"path": "main.go"},
	})
	if !ok || approval.Type != channel.StreamEventToolCallStart || approval.ToolCall == nil {
		t.Fatalf("approval event = %#v, ok=%v", approval, ok)
	}
	if approval.ToolCall.ApprovalID != "approval-1" || len(approval.ToolCall.Actions) != 2 {
		t.Fatalf("approval tool call = %#v", approval.ToolCall)
	}

	userInput, ok := agentEventToChannelEvent(agentevent.StreamEvent{
		Type:        agentevent.UserInputRequest,
		ToolName:    "ask_user",
		ToolCallID:  "call-2",
		ApprovalID:  "approval-2",
		UserInputID: "input-1",
		ShortID:     8,
		Status:      "pending",
		Input:       map[string]any{"question": "Proceed?"},
	})
	if !ok || userInput.Type != channel.StreamEventToolCallStart || userInput.ToolCall == nil {
		t.Fatalf("user input event = %#v, ok=%v", userInput, ok)
	}
	payload, ok := userInput.ToolCall.Input.(map[string]any)
	if !ok {
		t.Fatalf("user input payload = %#v", userInput.ToolCall.Input)
	}
	if payload["user_input_id"] != "input-1" || payload["status"] != "pending" || len(userInput.ToolCall.Actions) != 1 {
		t.Fatalf("user input tool call = %#v payload=%#v", userInput.ToolCall, payload)
	}
}

// --- Test helpers ---

// fakeTurnService emulates the in-process adapter's discuss event protocol:
// a run-resolved event first, then either a skip marker (ACP participation
// gate) or a terminal agent event stream.
type fakeTurnService struct {
	runtimeType string // empty means the native model runtime
	startErr    error
	streamErr   error
	onStart     func(turn.StartTurnCommand)
	calls       int
	lastCmd     turn.StartTurnCommand
}

func (f *fakeTurnService) StartTurn(_ context.Context, cmd turn.StartTurnCommand) (turn.RunHandle, error) {
	f.calls++
	f.lastCmd = cmd
	if f.onStart != nil {
		f.onStart(cmd)
	}
	if f.startErr != nil {
		return nil, f.startErr
	}
	runtimeType := f.runtimeType
	if runtimeType == "" {
		runtimeType = "native"
	}
	h := &fakeRunHandle{events: make(chan turn.Event, 8), errs: make(chan error, 1)}
	go func() {
		defer close(h.events)
		defer close(h.errs)
		seq := int64(0)
		emit := func(kind string, payload []byte) {
			seq++
			h.events <- turn.Event{RunID: "run-1", Seq: seq, Kind: kind, Payload: payload}
		}
		resolved, _ := json.Marshal(turn.DiscussRunResolvedPayload{RuntimeType: runtimeType})
		emit(turn.DiscussEventRunResolved, resolved)
		if runtimeType == sessionpkg.RuntimeACPAgent && !cmd.DiscussAddressed {
			emit(turn.DiscussEventSkipped, nil)
			return
		}
		if f.streamErr != nil {
			h.errs <- f.streamErr
			return
		}
		end, _ := json.Marshal(agentevent.StreamEvent{Type: agentevent.AgentEnd})
		emit(string(agentevent.AgentEnd), end)
	}()
	return h, nil
}

func (*fakeTurnService) RespondToolApproval(context.Context, turn.ToolApprovalResponse, chan<- json.RawMessage) error {
	return nil
}

func (*fakeTurnService) RespondUserInput(context.Context, turn.UserInputResponse, chan<- json.RawMessage) error {
	return nil
}

func (*fakeTurnService) AdvancePlainTextUserInput(context.Context, userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error) {
	return userinput.AdvanceTextResult{}, nil
}

type fakeRunHandle struct {
	events chan turn.Event
	errs   chan error
}

func (*fakeRunHandle) RunID() string                                    { return "run-1" }
func (h *fakeRunHandle) Events() <-chan turn.Event                      { return h.events }
func (h *fakeRunHandle) Errs() <-chan error                             { return h.errs }
func (*fakeRunHandle) Inject(context.Context, turn.InjectMessage) error { return nil }
func (*fakeRunHandle) AddOutboundAssets([]turn.OutboundAssetRef)        {}
func (*fakeRunHandle) Cancel()                                          {}

type fakeDiscussCursorStore struct {
	position       timeline.DiscussCursorPosition
	upsertPosition timeline.DiscussCursorPosition
	upsertScope    string
	upsertRouteID  string
}

func (f *fakeDiscussCursorStore) GetDiscussCursor(_ context.Context, _, _ string) (timeline.DiscussCursorPosition, error) {
	return f.position, nil
}

func (f *fakeDiscussCursorStore) UpsertDiscussCursor(_ context.Context, _, scopeKey, routeID, _ string, position timeline.DiscussCursorPosition) error {
	f.upsertScope = scopeKey
	f.upsertRouteID = routeID
	f.upsertPosition = position
	return nil
}

func TestHandleReplyWithTurn_TrustsPersistedEventCursor(t *testing.T) {
	store := &fakeDiscussCursorStore{position: timeline.DiscussCursorPosition{EventCursor: 9000, SourceCursor: 500}}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{CursorStore: store})
	sess := &discussSession{
		config: DiscussSessionConfig{BotID: "b", ThreadID: "s", CurrentPlatform: "telegram"},
	}

	covered := timeline.RenderedSegment{
		ReceivedAtMs:    400,
		LastEventCursor: 8500,
		Content:         []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="old">old</message>`}},
	}
	driver.handleReplyWithTurn(context.Background(), sess, timeline.RenderedContext{covered}, driver.logger, svc)

	if svc.calls != 0 {
		t.Fatal("turn must not start for segments at or below the persisted event cursor")
	}
	if sess.lastProcessed.EventCursor != 9000 {
		t.Fatalf("lastProcessed = %+v, want persisted event cursor 9000", sess.lastProcessed)
	}

	fresh := timeline.RenderedSegment{
		ReceivedAtMs:    700,
		LastEventCursor: 9100,
		Content:         []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="new">new</message>`}},
	}
	driver.handleReplyWithTurn(context.Background(), sess, timeline.RenderedContext{covered, fresh}, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatal("expected turn for segment past the persisted event cursor")
	}
	if store.upsertPosition.EventCursor != 9100 || store.upsertPosition.SourceCursor != 700 {
		t.Fatalf("persisted position = %+v, want cursor 9100 source 700", store.upsertPosition)
	}
}
