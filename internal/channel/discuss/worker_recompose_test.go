package discuss

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/felinics/memoh/internal/chat/timeline"
)

type countingArtifactProvider struct {
	calls atomic.Int64
}

func (p *countingArtifactProvider) ActiveCompactionArtifacts(context.Context, string, string) ([]timeline.CompactionArtifact, error) {
	p.calls.Add(1)
	return nil, nil
}

func recomposeTestRC() timeline.RenderedContext {
	return timeline.RenderedContext{
		{
			ReceivedAtMs: 200,
			Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">hello</message>`}},
		},
	}
}

func TestHandleReplyWithTurn_RecomposeReloadsArtifactsAndResubmits(t *testing.T) {
	svc := &fakeTurnService{recomposeRuns: 1}
	artifacts := &countingArtifactProvider{}
	driver := NewDiscussDriver(DiscussDriverDeps{Artifacts: artifacts})
	sess := &discussSession{
		config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"},
	}

	driver.handleReplyWithTurn(context.Background(), sess, recomposeTestRC(), driver.logger, svc)

	if svc.calls != 2 {
		t.Fatalf("StartTurn calls = %d, want 2 (recompose then rerun)", svc.calls)
	}
	if got := artifacts.calls.Load(); got != 2 {
		t.Fatalf("artifact loads = %d, want 2 (frontier reloaded per attempt)", got)
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

	if svc.calls != maxDiscussRecomposeAttempts {
		t.Fatalf("StartTurn calls = %d, want %d", svc.calls, maxDiscussRecomposeAttempts)
	}
	if sess.lastProcessed.SourceCursor == 200 {
		t.Fatal("cursor must not advance when every attempt ended in recompose")
	}
}
