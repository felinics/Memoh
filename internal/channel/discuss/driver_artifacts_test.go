package discuss

import (
	"context"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/chat/timeline"
)

type fakeArtifactProvider struct {
	artifacts []timeline.CompactionArtifact
	botID     string
	sessionID string
}

func (f *fakeArtifactProvider) ActiveCompactionArtifacts(_ context.Context, botID, sessionID string) ([]timeline.CompactionArtifact, error) {
	f.botID = botID
	f.sessionID = sessionID
	return f.artifacts, nil
}

func TestHandleReplyWithTurn_InsertsArtifactSummaryAtCoveredSlot(t *testing.T) {
	// The covered message sits between two survivors, so a blind prepend and a
	// slot insert produce different orders and this test can tell them apart.
	rc := timeline.RenderedContext{
		{
			MessageID:    "m0",
			ReceivedAtMs: 50,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="m0">earliest survivor</message>`}},
		},
		{
			MessageID:    "m1",
			ReceivedAtMs: 100,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="m1">old original</message>`}},
		},
		{
			MessageID:    "m2",
			ReceivedAtMs: 200,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="m2">current question</message>`}},
		},
	}
	artifacts := []timeline.CompactionArtifact{{
		ID:            "a1",
		Summary:       "compacted window",
		AnchorStartMs: 100,
		Sources:       []timeline.CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 100}},
	}}
	provider := &fakeArtifactProvider{artifacts: artifacts}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{Artifacts: provider})
	sess := &discussSession{
		config: DiscussSessionConfig{TeamID: "team-1", BotID: "bot-1", ThreadID: "sess-1"},
	}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatalf("StartTurn calls = %d, want 1", svc.calls)
	}
	if provider.botID != "bot-1" || provider.sessionID != "sess-1" {
		t.Fatalf("artifact provider scoped to %q/%q", provider.botID, provider.sessionID)
	}

	msgs := svc.lastCmd.DiscussMessages
	if len(msgs) != 3 {
		t.Fatalf("expected survivor + summary + survivor, got %d: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].Content, "earliest survivor") {
		t.Fatalf("summary must not be prepended ahead of an earlier survivor, got %+v", msgs)
	}
	if msgs[1].CompactionArtifactID != "a1" || !strings.Contains(msgs[1].Content, "compacted window") {
		t.Fatalf("summary must occupy the covered slot, got %+v", msgs)
	}
	if !strings.Contains(msgs[2].Content, "current question") {
		t.Fatalf("later survivor must follow the summary, got %+v", msgs)
	}
	for _, message := range msgs {
		if strings.Contains(message.Content, "old original") {
			t.Fatalf("covered original must be replaced, got %+v", msgs)
		}
	}
}

func TestHandleReplyWithTurn_NilArtifactProviderComposesPlain(t *testing.T) {
	rc := timeline.RenderedContext{
		{
			MessageID:    "m1",
			ReceivedAtMs: 200,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="m1">hello</message>`}},
		},
	}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"}}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatalf("StartTurn calls = %d, want 1", svc.calls)
	}
	if len(svc.lastCmd.DiscussMessages) != 1 || !strings.Contains(svc.lastCmd.DiscussMessages[0].Content, "hello") {
		t.Fatalf("expected plain composition, got %+v", svc.lastCmd.DiscussMessages)
	}
}

func TestWasRecentlyMentionedSkipsSelfSent(t *testing.T) {
	selfMention := timeline.RenderedSegment{ReceivedAtMs: 200, MentionsMe: true, IsSelfSent: true}
	ownMention := timeline.RenderedSegment{ReceivedAtMs: 300, RepliesToMe: true, IsMyself: true}
	rc := timeline.RenderedContext{selfMention, ownMention}

	if wasRecentlyMentioned(rc, timeline.DiscussCursorPosition{}) {
		t.Fatal("self-sent mentions must not wake the bot")
	}

	external := timeline.RenderedSegment{ReceivedAtMs: 400, MentionsMe: true}
	if !wasRecentlyMentioned(append(rc, external), timeline.DiscussCursorPosition{}) {
		t.Fatal("external mention must wake the bot")
	}
}

func TestBuildMentionGatesOnWatermarkNotCoverage(t *testing.T) {
	mention := timeline.RenderedSegment{
		MessageID:    "m1",
		ReceivedAtMs: 100,
		MentionsMe:   true,
		Content:      []timeline.RenderedContentPiece{{Type: "text", Text: "@bot ping"}},
	}
	rc := timeline.RenderedContext{
		mention,
		{
			MessageID:    "m2",
			ReceivedAtMs: 200,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: "unrelated chatter"}},
		},
	}
	artifacts := []timeline.CompactionArtifact{{
		ID:      "a1",
		Summary: "covers the pending mention",
		Sources: []timeline.CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 100}},
	}}

	pending, _, ok := discussTriggerBuilder{}.Build(DiscussSessionConfig{ConversationType: "group"}, rc, nil, timeline.DiscussCursorPosition{}, artifacts, timeline.ComposeBudget{})
	if !ok {
		t.Fatal("expected a composed plan")
	}
	if !pending.command.DiscussAddressed {
		t.Fatal("a mention the watermark has not consumed must wake the session even when compaction covers it")
	}

	consumed, _, ok := discussTriggerBuilder{}.Build(DiscussSessionConfig{ConversationType: "group"}, rc, nil, timeline.DiscussCursorPosition{SourceCursor: 150}, artifacts, timeline.ComposeBudget{})
	if !ok {
		t.Fatal("expected a composed plan past the mention")
	}
	if consumed.command.DiscussAddressed {
		t.Fatal("a consumed mention must not re-mark the session as mentioned")
	}
}

func TestBuildSkipsImageRefsCoveredByArtifacts(t *testing.T) {
	imageMsg := timeline.RenderedSegment{
		MessageID:    "m1",
		ReceivedAtMs: 100,
		Content:      []timeline.RenderedContentPiece{{Type: "text", Text: "photo drop"}},
		ImageRefs:    []timeline.ImageAttachmentRef{{ContentHash: "img-1", Mime: "image/png"}},
	}
	rc := timeline.RenderedContext{
		imageMsg,
		{
			MessageID:    "m2",
			ReceivedAtMs: 200,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: "current"}},
		},
	}
	artifacts := []timeline.CompactionArtifact{{
		ID:      "a1",
		Summary: "covers the image message",
		Sources: []timeline.CompactionSource{{ExternalMessageID: "m1", CreatedAtMs: 100}},
	}}

	plan, _, ok := discussTriggerBuilder{}.Build(DiscussSessionConfig{ConversationType: "group"}, rc, nil, timeline.DiscussCursorPosition{}, artifacts, timeline.ComposeBudget{})
	if !ok {
		t.Fatal("expected a composed plan")
	}
	if len(plan.command.DiscussImageRefs) != 0 {
		t.Fatalf("covered image must not be re-attached, got %+v", plan.command.DiscussImageRefs)
	}

	planLive, _, ok := discussTriggerBuilder{}.Build(DiscussSessionConfig{ConversationType: "group"}, rc, nil, timeline.DiscussCursorPosition{}, nil, timeline.ComposeBudget{})
	if !ok {
		t.Fatal("expected a composed plan without artifacts")
	}
	if len(planLive.command.DiscussImageRefs) != 1 {
		t.Fatalf("uncovered image must be attached, got %+v", planLive.command.DiscussImageRefs)
	}
}

func TestHandleReplyWithTurn_ColdStartAnchorCoversCursorBearingSegments(t *testing.T) {
	// Idle eviction makes cold start a hot path: the durable cursor row may be
	// absent while persisted replies prove the context was already answered.
	answered := timeline.RenderedSegment{
		MessageID:       "m1",
		ReceivedAtMs:    1000,
		LastEventCursor: 5,
		MentionsMe:      true,
		Content:         []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="m1">@bot old ping</message>`}},
	}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{CursorStore: &fakeDiscussCursorStore{}})
	driver.history = discussHistoryReader{messages: nil, logger: driver.logger}
	sess := &discussSession{config: DiscussSessionConfig{BotID: "b", ThreadID: "s"}}
	sess.lastProcessed = timeline.DiscussCursorPosition{SourceCursor: 3000}

	driver.handleReplyWithTurn(context.Background(), sess, timeline.RenderedContext{answered}, driver.logger, svc)

	if svc.calls != 0 {
		t.Fatal("a cursor-bearing segment inside the answered window must not re-fire a turn")
	}

	fresh := timeline.RenderedSegment{
		MessageID:       "m2",
		ReceivedAtMs:    4000,
		LastEventCursor: 6,
		Content:         []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="m2">new</message>`}},
	}
	driver.handleReplyWithTurn(context.Background(), sess, timeline.RenderedContext{answered, fresh}, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatalf("a segment past the anchor must fire a turn, got %d calls", svc.calls)
	}
}
