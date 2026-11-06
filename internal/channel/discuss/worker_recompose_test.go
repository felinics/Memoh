package discuss

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/apperror"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	sessionpkg "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// movingCheckpointSource emulates the compaction a recompose follows: each
// load returns a checkpoint past the one before. With oversized set, the
// first one's summary does not fit the admission budget.
type movingCheckpointSource struct {
	oversized bool
	calls     int
}

func (s *movingCheckpointSource) Load(context.Context, string, timeline.RenderedContext) (timeline.HistoryCheckpoint, error) {
	s.calls++
	summary := "small"
	if s.oversized && s.calls == 1 {
		summary = strings.Repeat("s", 8000)
	}
	return timeline.HistoryCheckpoint{
		Position: messagepkg.HistoryPosition{Turn: int64(s.calls)},
		Items:    []timeline.CheckpointItem{{SummaryID: "summary", Summary: summary}},
	}, nil
}

func TestDiscussChannelOverflowRequestsRecoveryWithoutPayload(t *testing.T) {
	checkpoints := &movingCheckpointSource{oversized: true}
	svc := &fakeTurnService{recomposeRuns: 1}
	svc.onStart = func(cmd turn.StartTurnCommand) {
		if svc.calls == 1 && (!cmd.DiscussContextOverflow || len(cmd.DiscussMessages) != 0 || cmd.DiscussContextTokens <= 1000 || cmd.DiscussCurrentTokens <= 0 || cmd.DiscussCurrentTokens >= 1000) {
			t.Errorf("overflow must carry recovery metadata only: overflow=%v messages=%d pressure=%d", cmd.DiscussContextOverflow, len(cmd.DiscussMessages), cmd.DiscussContextTokens)
		}
	}
	driver := NewDiscussDriver(DiscussDriverDeps{Checkpoints: checkpoints, AdmissionMaxTokens: 1000})
	sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"}}
	driver.handleReplyWithTurn(t.Context(), sess, recomposeTestRC(), driver.logger, svc)
	if svc.calls != 2 || sess.lastProcessed.SourceCursor != 200 || svc.lastCmd.DiscussContextOverflow {
		t.Fatalf("recovery failed: calls=%d cursor=%+v overflow=%v", svc.calls, sess.lastProcessed, svc.lastCmd.DiscussContextOverflow)
	}
}

func TestDiscussChannelOverflowRecoveryIsBoundedAndDoesNotConsume(t *testing.T) {
	for _, retries := range []int{0, 99} {
		svc := &fakeTurnService{recomposeRuns: retries}
		checkpoints := &fakeCheckpointSource{checkpoint: timeline.HistoryCheckpoint{
			Items: []timeline.CheckpointItem{{SummaryID: "summary", Summary: strings.Repeat("s", 8000)}},
		}}
		driver := NewDiscussDriver(DiscussDriverDeps{Checkpoints: checkpoints, AdmissionMaxTokens: 1000})
		sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"}}
		driver.handleReplyWithTurn(t.Context(), sess, recomposeTestRC(), driver.logger, svc)
		want := 1
		if retries > 0 {
			want = (maxDiscussRecoveries + 1)
		}
		if svc.calls != want || sess.lastProcessed.SourceCursor != 0 || len(svc.lastCmd.DiscussMessages) != 0 {
			t.Fatalf("unsafe overflow retry: calls=%d want=%d cursor=%+v payloads=%d", svc.calls, want, sess.lastProcessed, len(svc.lastCmd.DiscussMessages))
		}
	}
}

func recomposeTestRC() timeline.RenderedContext {
	return timeline.RenderedContext{
		{
			ReceivedAtMs: 200,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">hello</message>`}},
		},
	}
}

func TestHandleReplyWithTurn_RecomposeReloadsHistoryAndResubmits(t *testing.T) {
	svc := &fakeTurnService{recomposeRuns: 1}
	checkpoints := &movingCheckpointSource{}
	history := &lightweightHistoryStub{}
	driver := NewDiscussDriver(DiscussDriverDeps{Checkpoints: checkpoints, MessageService: history})
	sess := &discussSession{
		config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"},
	}

	driver.handleReplyWithTurn(context.Background(), sess, recomposeTestRC(), driver.logger, svc)

	if svc.calls != 2 {
		t.Fatalf("StartTurn calls = %d, want 2 (recompose then rerun)", svc.calls)
	}
	if checkpoints.calls != 2 {
		t.Fatalf("checkpoint loads = %d, want 2 (checkpoint reloaded per attempt)", checkpoints.calls)
	}
	if want := []messagepkg.HistoryPosition{{Turn: 1}, {Turn: 2}}; !reflect.DeepEqual(history.after, want) {
		t.Fatalf("turn responses read after %v, want after each checkpoint loaded (%v)", history.after, want)
	}
	if sess.lastProcessed.SourceCursor != 200 {
		t.Fatalf("cursor must advance after the rerun completes, got %+v", sess.lastProcessed)
	}
}

func TestHandleReplyWithTurn_RecomposeLimitDefersToNextTrigger(t *testing.T) {
	svc := &fakeTurnService{recomposeRuns: 99}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{
		config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"},
	}

	driver.handleReplyWithTurn(context.Background(), sess, recomposeTestRC(), driver.logger, svc)

	if svc.calls != (maxDiscussRecoveries + 1) {
		t.Fatalf("StartTurn calls = %d, want %d", svc.calls, (maxDiscussRecoveries + 1))
	}
	if sess.lastProcessed.SourceCursor == 200 {
		t.Fatal("cursor must not advance when every attempt ended in recompose")
	}
}

func TestHandleReplyWithTurnRecoveryCompletesOriginalInput(t *testing.T) {
	svc := &fakeTurnService{recomposeRuns: 1}
	svc.onStart = func(cmd turn.StartTurnCommand) {
		if cmd.DiscussRecoveryExhausted != (svc.calls == 2) {
			t.Errorf("attempt %d exhausted=%v", svc.calls, cmd.DiscussRecoveryExhausted)
		}
	}
	driver := NewDiscussDriver(DiscussDriverDeps{})
	sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"}}
	driver.handleReplyWithTurn(t.Context(), sess, recomposeTestRC(), driver.logger, svc)
	if svc.calls != 2 || sess.lastProcessed.SourceCursor != 200 {
		t.Fatalf("calls=%d cursor=%+v; the attempt after recovery must complete original input", svc.calls, sess.lastProcessed)
	}
	driver.handleReplyWithTurn(t.Context(), sess, recomposeTestRC(), driver.logger, svc)
	if svc.calls != 2 {
		t.Fatal("same input processed twice")
	}
}

func TestDiscussSkippedAgentTurnConsumesOverflowingInput(t *testing.T) {
	svc := &fakeTurnService{runtimeType: sessionpkg.RuntimeACPAgent}
	driver := NewDiscussDriver(DiscussDriverDeps{AdmissionMaxTokens: 100})
	sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1", ConversationType: "group"}}
	rc := timeline.RenderedContext{{MessageID: "big", ReceivedAtMs: 200, Content: []timeline.RenderedContentPiece{{Type: "text", Text: strings.Repeat("x", 2000)}}}}
	driver.handleReplyWithTurn(t.Context(), sess, rc, driver.logger, svc)
	if svc.calls != 1 || !svc.lastCmd.DiscussContextOverflow || sess.lastProcessed.SourceCursor != 200 {
		t.Fatalf("calls=%d overflow=%v cursor=%+v; an unaddressed agent turn must consume what it skips", svc.calls, svc.lastCmd.DiscussContextOverflow, sess.lastProcessed)
	}
}

// failingCheckpointSource fails every load after the first ok ones.
type failingCheckpointSource struct {
	ok    int
	calls int
}

func (s *failingCheckpointSource) Load(context.Context, string, timeline.RenderedContext) (timeline.HistoryCheckpoint, error) {
	s.calls++
	if s.calls > s.ok {
		return timeline.HistoryCheckpoint{}, errors.New("checkpoint unavailable")
	}
	return timeline.HistoryCheckpoint{}, nil
}

type failingHistoryReader struct{}

func (failingHistoryReader) ListTurnResponseSourcesWithinBytes(context.Context, string, messagepkg.HistoryPosition, int64) (messagepkg.HistoryLoad, error) {
	return messagepkg.HistoryLoad{}, errors.New("history unavailable")
}

func TestDiscussMissingSourceFailsWithoutRunningOrConsuming(t *testing.T) {
	for name, deps := range map[string]DiscussDriverDeps{
		"checkpoint": {Checkpoints: &failingCheckpointSource{}},
		"history":    {MessageService: &failingHistoryReader{}},
	} {
		t.Run(name, func(t *testing.T) {
			broadcaster := &recordingBroadcaster{}
			deps.Broadcaster = broadcaster
			svc := &fakeTurnService{}
			driver := NewDiscussDriver(deps)
			sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"}}
			driver.handleReplyWithTurn(t.Context(), sess, recomposeTestRC(), driver.logger, svc)
			failures := broadcaster.errors()
			if svc.calls != 0 || sess.lastProcessed.SourceCursor != 0 || len(failures) != 1 || failures[0].ErrorCode != string(apperror.CodeInternal) {
				t.Fatalf("calls=%d cursor=%+v failures=%+v, want one internal failure and nothing run or consumed", svc.calls, sess.lastProcessed, failures)
			}
		})
	}
}

// A recompose composes from history it loads again; when that history cannot
// be loaded the trigger fails after the first attempt and leaves its input
// for the next trigger.
func TestDiscussRecoveryReloadFailureFailsWithoutConsuming(t *testing.T) {
	broadcaster := &recordingBroadcaster{}
	svc := &fakeTurnService{recomposeRuns: 1}
	driver := NewDiscussDriver(DiscussDriverDeps{Checkpoints: &failingCheckpointSource{ok: 1}, Broadcaster: broadcaster})
	sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"}}

	driver.handleReplyWithTurn(t.Context(), sess, recomposeTestRC(), driver.logger, svc)

	failures := broadcaster.errors()
	if svc.calls != 1 || sess.lastProcessed.SourceCursor != 0 || len(failures) != 1 || failures[0].ErrorCode != string(apperror.CodeInternal) {
		t.Fatalf("calls=%d cursor=%+v failures=%+v, want the first attempt only, one internal failure and nothing consumed", svc.calls, sess.lastProcessed, failures)
	}
}
