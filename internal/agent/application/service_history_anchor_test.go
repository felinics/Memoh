package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type anchorRecordingMessageService struct {
	recordingMessageService
	anchor        time.Time
	anchorErr     error
	anchorBefore  []time.Time
	windows       []messagepkg.HistoryWindow
	responseSince []time.Time
}

func (s *anchorRecordingMessageService) HistoryAnchorBySession(_ context.Context, _ string, before time.Time) (time.Time, error) {
	s.anchorBefore = append(s.anchorBefore, before)
	return s.anchor, s.anchorErr
}

func (s *anchorRecordingMessageService) ListActiveBySessionWithinBytes(_ context.Context, _ string, window messagepkg.HistoryWindow) ([]messagepkg.Message, error) {
	s.windows = append(s.windows, window)
	return nil, nil
}

func (s *anchorRecordingMessageService) ListTurnResponseSourcesSinceBySessionWithinBytes(_ context.Context, _ string, since time.Time, _ int64) ([]messagepkg.Message, error) {
	s.responseSince = append(s.responseSince, since)
	return nil, nil
}

// Every row older than the compaction anchor reaches a request as its
// summary, so history and turn responses load from the anchor however old
// it is, never from a fixed window.
func TestThreadHistoryLoadsBeginAtTheCompactionAnchor(t *testing.T) {
	t.Parallel()

	anchor := time.Now().UTC().Add(-30 * 24 * time.Hour)
	messages := &anchorRecordingMessageService{anchor: anchor}
	svc := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}

	if _, err := svc.loadHistoryRecords(t.Context(), historyfrag.ScopeFallback{}, "session-1", 1000, true, compactionArtifactBoundary{}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.loadTurnResponses(t.Context(), "session-1", 1000); err != nil {
		t.Fatal(err)
	}
	if len(messages.anchorBefore) != 2 || !messages.anchorBefore[0].IsZero() || !messages.anchorBefore[1].IsZero() {
		t.Fatalf("anchor asked as of %v, want the live history twice", messages.anchorBefore)
	}
	if len(messages.windows) != 1 || !messages.windows[0].Since.Equal(anchor) || !messages.windows[0].Before.IsZero() || !messages.windows[0].FoldSummarized {
		t.Fatalf("history window = %+v, want a folded load from the anchor %v", messages.windows, anchor)
	}
	if len(messages.responseSince) != 1 || !messages.responseSince[0].Equal(anchor) {
		t.Fatalf("turn responses since %v, want the anchor %v", messages.responseSince, anchor)
	}
}

// An edit or retry reads the history as it stood at its cutoff, from the
// anchor of that history.
func TestCutHistoryLoadsFromTheAnchorAsOfTheCutoff(t *testing.T) {
	t.Parallel()

	cutoff := time.Now().UTC().Add(-time.Hour)
	anchor := cutoff.Add(-48 * time.Hour)
	messages := &anchorRecordingMessageService{anchor: anchor}
	svc := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}

	if _, err := svc.loadHistoryRecords(t.Context(), historyfrag.ScopeFallback{}, "session-1", 1000, true, compactionArtifactBoundary{hasCutoff: true, cutoff: cutoff}); err != nil {
		t.Fatal(err)
	}
	if len(messages.anchorBefore) != 1 || !messages.anchorBefore[0].Equal(cutoff) {
		t.Fatalf("anchor asked as of %v, want the cutoff %v", messages.anchorBefore, cutoff)
	}
	if len(messages.windows) != 1 || !messages.windows[0].Since.Equal(anchor) || !messages.windows[0].Before.Equal(cutoff) {
		t.Fatalf("history window = %+v, want from %v before %v", messages.windows, anchor, cutoff)
	}
}

func TestThreadHistoryFailsWhenTheAnchorDoesNotLoad(t *testing.T) {
	t.Parallel()

	messages := &anchorRecordingMessageService{anchorErr: errors.New("database unavailable")}
	svc := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
	if _, err := svc.loadHistoryRecords(t.Context(), historyfrag.ScopeFallback{}, "session-1", 1000, true, compactionArtifactBoundary{}); err == nil {
		t.Fatal("history loaded without its anchor")
	}
	if _, err := svc.loadTurnResponses(t.Context(), "session-1", 1000); err == nil {
		t.Fatal("turn responses loaded without their anchor")
	}
	if len(messages.windows) != 0 || len(messages.responseSince) != 0 {
		t.Fatalf("loads ran unbounded: history=%+v responses=%v", messages.windows, messages.responseSince)
	}
}

// History belongs to a thread: a request without one has none to read.
func TestThreadlessRequestsLoadNoHistory(t *testing.T) {
	t.Parallel()

	messages := &anchorRecordingMessageService{}
	svc := &Service{messageService: messages, logger: slog.New(slog.DiscardHandler)}
	records, err := svc.loadHistoryRecords(t.Context(), historyfrag.ScopeFallback{ChatID: "bot-1"}, "", 1000, true, compactionArtifactBoundary{})
	if err != nil || len(records) != 0 {
		t.Fatalf("records=%v err=%v, want none", records, err)
	}
	if len(messages.anchorBefore) != 0 || len(messages.windows) != 0 {
		t.Fatalf("a threadless request read history: anchors=%v windows=%+v", messages.anchorBefore, messages.windows)
	}
}
