package discuss

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/chat/timeline"
)

// processingRecorder is a fake StartProcessingFunc that records the order of
// indicator starts, runs and finishes.
type processingRecorder struct {
	events []string
	causes []error
}

func (r *processingRecorder) start(context.Context) func(error) {
	r.events = append(r.events, "start")
	return func(cause error) {
		r.events = append(r.events, "finish")
		r.causes = append(r.causes, cause)
	}
}

func processingTestSession(r *processingRecorder, conversationType string) *discussSession {
	return &discussSession{config: DiscussSessionConfig{
		BotID:            "bot-1",
		ThreadID:         "sess-1",
		ConversationType: conversationType,
		StartProcessing:  r.start,
	}}
}

func mentionRC() timeline.RenderedContext {
	return timeline.RenderedContext{{
		ReceivedAtMs: 200,
		MentionsMe:   true,
		Content:      []timeline.RenderedContentPiece{{Type: "text", Text: `<message id="1">@bot help</message>`}},
	}}
}

func TestHandleReplyWithTurn_AddressedRunShowsProcessingAroundRun(t *testing.T) {
	recorder := &processingRecorder{}
	svc := &fakeTurnService{onStart: func(turn.StartTurnCommand) {
		recorder.events = append(recorder.events, "run")
	}}
	driver := NewDiscussDriver(DiscussDriverDeps{})

	driver.handleReplyWithTurn(context.Background(), processingTestSession(recorder, "group"), mentionRC(), driver.logger, svc)

	if got := recorder.events; len(got) != 3 || got[0] != "start" || got[1] != "run" || got[2] != "finish" {
		t.Fatalf("events = %v, want [start run finish]", got)
	}
	if recorder.causes[0] != nil {
		t.Fatalf("finish cause = %v, want nil for a clean run", recorder.causes[0])
	}
}

func TestHandleReplyWithTurn_PrivateConversationShowsProcessing(t *testing.T) {
	recorder := &processingRecorder{}
	driver := NewDiscussDriver(DiscussDriverDeps{})

	driver.handleReplyWithTurn(context.Background(), processingTestSession(recorder, "private"), recomposeTestRC(), driver.logger, &fakeTurnService{})

	if got := recorder.events; len(got) != 2 || got[0] != "start" || got[1] != "finish" {
		t.Fatalf("events = %v, want [start finish]", got)
	}
}

func TestHandleReplyWithTurn_UnaddressedGroupRunShowsNoProcessing(t *testing.T) {
	recorder := &processingRecorder{}
	svc := &fakeTurnService{}
	driver := NewDiscussDriver(DiscussDriverDeps{})

	driver.handleReplyWithTurn(context.Background(), processingTestSession(recorder, "group"), recomposeTestRC(), driver.logger, svc)

	if svc.calls != 1 {
		t.Fatalf("StartTurn calls = %d, want 1", svc.calls)
	}
	if len(recorder.events) != 0 {
		t.Fatalf("events = %v, want none for an unaddressed group round", recorder.events)
	}
}

func TestHandleReplyWithTurn_FailedRunFinishesProcessingWithCause(t *testing.T) {
	recorder := &processingRecorder{}
	svc := &fakeTurnService{streamErr: errors.New("runtime failed")}
	driver := NewDiscussDriver(DiscussDriverDeps{})

	driver.handleReplyWithTurn(context.Background(), processingTestSession(recorder, "group"), mentionRC(), driver.logger, svc)

	if len(recorder.causes) != 1 || !errors.Is(recorder.causes[0], errDiscussRunFailed) {
		t.Fatalf("finish causes = %v, want one errDiscussRunFailed", recorder.causes)
	}
}

func TestHandleReplyWithTurn_StartErrorStillFinishesProcessing(t *testing.T) {
	recorder := &processingRecorder{}
	svc := &fakeTurnService{startErr: turn.ErrSessionBusy}
	driver := NewDiscussDriver(DiscussDriverDeps{})

	driver.handleReplyWithTurn(context.Background(), processingTestSession(recorder, "group"), mentionRC(), driver.logger, svc)

	if got := recorder.events; len(got) != 2 || got[0] != "start" || got[1] != "finish" || recorder.causes[0] != nil {
		t.Fatalf("events = %v causes = %v, want start/finish completing", got, recorder.causes)
	}
}

func TestHandleReplyWithTurn_ProcessingPerRecomposeAttempt(t *testing.T) {
	recorder := &processingRecorder{}
	svc := &fakeTurnService{recomposeRuns: 1}
	driver := NewDiscussDriver(DiscussDriverDeps{})

	driver.handleReplyWithTurn(context.Background(), processingTestSession(recorder, "private"), recomposeTestRC(), driver.logger, svc)

	want := []string{"start", "finish", "start", "finish"}
	if got := recorder.events; len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestNotifyRCLatestStartProcessingWins(t *testing.T) {
	driver := NewDiscussDriver(DiscussDriverDeps{})
	defer driver.StopSession("sess-1")
	var calledFirst, calledSecond bool
	first := func(context.Context) func(error) { calledFirst = true; return nil }
	second := func(context.Context) func(error) { calledSecond = true; return nil }

	driver.NotifyRC(context.Background(), "sess-1", timeline.RenderedContext{}, DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1", StartProcessing: first})
	driver.NotifyRC(context.Background(), "sess-1", timeline.RenderedContext{}, DiscussSessionConfig{BotID: "bot-1", ThreadID: "sess-1", StartProcessing: second})

	driver.mu.Lock()
	sess := driver.sessions["sess-1"]
	driver.mu.Unlock()
	startProcessing(context.Background(), driver.sessionConfigSnapshot(sess), true)(nil)

	if calledFirst || !calledSecond {
		t.Fatalf("calledFirst=%v calledSecond=%v, want only the latest message's starter", calledFirst, calledSecond)
	}
}
