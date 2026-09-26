package discuss

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
)

type countingArtifactProvider struct {
	calls atomic.Int64
}

type recoveringArtifactProvider struct {
	calls int
}

func (p *recoveringArtifactProvider) ActiveCompactionArtifacts(context.Context, string, string) ([]timeline.CompactionArtifact, error) {
	p.calls++
	summary := "small"
	if p.calls == 1 {
		summary = strings.Repeat("s", 8000)
	}
	return []timeline.CompactionArtifact{{ID: "summary", Summary: summary}}, nil
}

func TestDiscussChannelOverflowRequestsRecoveryWithoutPayload(t *testing.T) {
	artifacts := &recoveringArtifactProvider{}
	svc := &fakeTurnService{recomposeRuns: 1}
	svc.onStart = func(cmd turn.StartTurnCommand) {
		if svc.calls == 1 && (!cmd.DiscussContextOverflow || len(cmd.DiscussMessages) != 0 || cmd.DiscussContextTokens <= 1000) {
			t.Errorf("overflow must carry recovery metadata only: overflow=%v messages=%d pressure=%d", cmd.DiscussContextOverflow, len(cmd.DiscussMessages), cmd.DiscussContextTokens)
		}
	}
	driver := NewDiscussDriver(DiscussDriverDeps{Artifacts: artifacts, AdmissionMaxTokens: 1000})
	sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"}}
	driver.handleReplyWithTurn(t.Context(), sess, recomposeTestRC(), driver.logger, svc)
	if svc.calls != 2 || sess.lastProcessed.SourceCursor != 200 || svc.lastCmd.DiscussContextOverflow {
		t.Fatalf("recovery failed: calls=%d cursor=%+v overflow=%v", svc.calls, sess.lastProcessed, svc.lastCmd.DiscussContextOverflow)
	}
}

func TestDiscussChannelOverflowRecoveryIsBoundedAndDoesNotConsume(t *testing.T) {
	for _, retries := range []int{0, 99} {
		svc := &fakeTurnService{recomposeRuns: retries}
		artifacts := &fakeArtifactProvider{artifacts: []timeline.CompactionArtifact{{ID: "summary", Summary: strings.Repeat("s", 8000)}}}
		driver := NewDiscussDriver(DiscussDriverDeps{Artifacts: artifacts, AdmissionMaxTokens: 1000})
		sess := &discussSession{config: DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1"}}
		driver.handleReplyWithTurn(t.Context(), sess, recomposeTestRC(), driver.logger, svc)
		want := 1
		if retries > 0 {
			want = maxDiscussRecomposeAttempts
		}
		if svc.calls != want || sess.lastProcessed.SourceCursor != 0 || len(svc.lastCmd.DiscussMessages) != 0 {
			t.Fatalf("unsafe overflow retry: calls=%d want=%d cursor=%+v payloads=%d", svc.calls, want, sess.lastProcessed, len(svc.lastCmd.DiscussMessages))
		}
	}
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
