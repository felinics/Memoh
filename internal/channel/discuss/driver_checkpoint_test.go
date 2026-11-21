package discuss

import (
	"context"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/chat/timeline"
)

// fakeCheckpointSource serves one checkpoint and records what it was asked
// for.
type fakeCheckpointSource struct {
	checkpoint timeline.HistoryCheckpoint
	sessionID  string
	rc         timeline.RenderedContext
	calls      int
}

func (f *fakeCheckpointSource) Load(_ context.Context, sessionID string, rc timeline.RenderedContext) (timeline.HistoryCheckpoint, error) {
	f.calls++
	f.sessionID = sessionID
	f.rc = rc
	return f.checkpoint, nil
}

func TestHandleReplyWithTurn_ComposesUnderTheCheckpoint(t *testing.T) {
	// The checkpoint keeps the earliest input, folds the next one into its
	// summary and leaves the current question after it.
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
	source := &fakeCheckpointSource{checkpoint: timeline.HistoryCheckpoint{
		Items:    []timeline.CheckpointItem{{MessageID: "m0"}, {SummaryID: "a1", Summary: "compacted window"}},
		Messages: map[string]timeline.CheckpointMessage{"m0": {Kept: true}, "m1": {}, "m2": {Kept: true}},
	}}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{Checkpoints: source})
	sess := &discussSession{
		config: DiscussSessionConfig{TeamID: "team-1", BotID: "bot-1", ThreadID: "sess-1"},
	}

	driver.handleReplyWithTurn(context.Background(), sess, rc, driver.logger, svc)

	if svc.calls != 1 {
		t.Fatalf("StartTurn calls = %d, want 1", svc.calls)
	}
	if source.sessionID != "sess-1" || len(source.rc) != len(rc) {
		t.Fatalf("checkpoint loaded for %q with %d segments, want sess-1 with the trigger's %d", source.sessionID, len(source.rc), len(rc))
	}

	msgs := svc.lastCmd.DiscussMessages
	if len(msgs) != 3 {
		t.Fatalf("expected kept input + summary + current question, got %d: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].Content, "earliest survivor") || !msgs[0].Pinned {
		t.Fatalf("the kept input must open the history, pinned, got %+v", msgs)
	}
	if msgs[1].CompactionArtifactID != "a1" || !strings.Contains(msgs[1].Content, "compacted window") || !msgs[1].Pinned {
		t.Fatalf("the summary must follow what the checkpoint keeps before it, pinned, got %+v", msgs)
	}
	if !strings.Contains(msgs[2].Content, "current question") || msgs[2].Pinned {
		t.Fatalf("the current question must follow the checkpoint, unpinned, got %+v", msgs)
	}
	for _, message := range msgs {
		if strings.Contains(message.Content, "old original") {
			t.Fatalf("the folded input must not compose, got %+v", msgs)
		}
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

// foldingCheckpoint folds the inbound message into its summary.
func foldingCheckpoint(messageID, summary string) timeline.HistoryCheckpoint {
	return timeline.HistoryCheckpoint{
		Items:    []timeline.CheckpointItem{{SummaryID: "a1", Summary: summary}},
		Messages: map[string]timeline.CheckpointMessage{messageID: {}},
	}
}

func TestBuildMentionGatesOnWatermarkNotFolding(t *testing.T) {
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
	held := foldingCheckpoint("m1", "folds the pending mention")

	pending, _, ok := discussTriggerBuilder{}.Build(DiscussSessionConfig{ConversationType: "group"}, rc, discussHistory{checkpoint: held}, timeline.DiscussCursorPosition{}, timeline.ComposeBudget{})
	if !ok {
		t.Fatal("expected a composed plan")
	}
	if !pending.command.DiscussAddressed {
		t.Fatal("a mention the watermark has not consumed must wake the session even when the checkpoint folds it")
	}

	consumed, _, ok := discussTriggerBuilder{}.Build(DiscussSessionConfig{ConversationType: "group"}, rc, discussHistory{checkpoint: held}, timeline.DiscussCursorPosition{SourceCursor: 150}, timeline.ComposeBudget{})
	if !ok {
		t.Fatal("expected a composed plan past the mention")
	}
	if consumed.command.DiscussAddressed {
		t.Fatal("a consumed mention must not re-mark the session as mentioned")
	}
}

// A folded message's image cannot ride in the summary. Its ref still
// travels, naming a message the composed context no longer carries, so the
// server records the image instead of attaching it.
func TestBuildKeepsFoldedImageRefsOutsideTheComposedMessages(t *testing.T) {
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

	plan, _, ok := discussTriggerBuilder{}.Build(DiscussSessionConfig{ConversationType: "group"}, rc, discussHistory{checkpoint: foldingCheckpoint("m1", "folds the image message")}, timeline.DiscussCursorPosition{}, timeline.ComposeBudget{})
	if !ok {
		t.Fatal("expected a composed plan")
	}
	if refs := plan.command.DiscussImageRefs; len(refs) != 1 || refs[0].MessageID != "m1" {
		t.Fatalf("folded image ref = %+v, want it to name its folded message", refs)
	}
	for _, message := range plan.command.DiscussMessages {
		if message.Source != nil && message.Source.ID == "m1" {
			t.Fatalf("the folded message is still composed: %+v", plan.command.DiscussMessages)
		}
	}

	planLive, _, ok := discussTriggerBuilder{}.Build(DiscussSessionConfig{ConversationType: "group"}, rc, discussHistory{checkpoint: timeline.HistoryCheckpoint{}}, timeline.DiscussCursorPosition{}, timeline.ComposeBudget{})
	if !ok {
		t.Fatal("expected a composed plan without a checkpoint")
	}
	if len(planLive.command.DiscussImageRefs) != 1 {
		t.Fatalf("unfolded image must be attached, got %+v", planLive.command.DiscussImageRefs)
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
