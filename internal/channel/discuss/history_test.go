package discuss

import (
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

type lightweightHistoryStub struct {
	messages     []messagepkg.Message
	origin       messagepkg.HistoryOrigin
	sessionID    string
	maxBytes     int64
	measureSince time.Time
	since        time.Time
}

func (s *lightweightHistoryStub) HistoryOriginBySession(context.Context, string) (messagepkg.HistoryOrigin, error) {
	return s.origin, nil
}

func (s *lightweightHistoryStub) MeasureActiveBySession(_ context.Context, _ string, since time.Time) (messagepkg.ActiveMessagesMeasure, error) {
	s.measureSince = since
	return messagepkg.ActiveMessagesMeasure{MessageCount: 100, ContentBytes: 10000}, nil
}

func (s *lightweightHistoryStub) ListTurnResponseSourcesSinceBySessionWithinBytes(_ context.Context, sessionID string, since time.Time, maxBytes int64) ([]messagepkg.Message, error) {
	s.sessionID = sessionID
	s.since = since
	s.maxBytes = maxBytes
	return s.messages, nil
}

// A session's turn responses reach Discuss from its history origin on; a
// session without one is read from the start.
func TestLightweightHistoryLoadsFromTheHistoryOrigin(t *testing.T) {
	origin := messagepkg.HistoryOrigin{
		At:           time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC),
		OmittedCount: 40,
		OmittedFirst: time.Date(2026, 3, 1, 8, 0, 0, 0, time.UTC),
		OmittedLast:  time.Date(2026, 10, 1, 8, 59, 0, 0, time.UTC),
	}
	for name, tc := range map[string]struct {
		origin messagepkg.HistoryOrigin
		since  time.Time
	}{
		"origin":    {origin: origin, since: origin.At},
		"no origin": {since: time.Unix(0, 0).UTC()},
	} {
		t.Run(name, func(t *testing.T) {
			source := &lightweightHistoryStub{origin: tc.origin}
			reader := discussHistoryReader{messages: source, maxBytes: 4000, logger: slog.New(slog.DiscardHandler)}
			loaded, err := reader.Origin(context.Background(), "session")
			if err != nil || loaded != tc.origin {
				t.Fatalf("origin = %+v, %v; want %+v", loaded, err, tc.origin)
			}
			if _, _, err := reader.Load(context.Background(), "session", loaded); err != nil {
				t.Fatal(err)
			}
			if !source.since.Equal(tc.since) || !source.measureSince.Equal(tc.since) {
				t.Fatalf("history read since %v, measured since %v; want %v", source.since, source.measureSince, tc.since)
			}
		})
	}
}

func TestLightweightHistoryPreservesInterruptedReasoning(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "interrupted", true: "completed"}[completed], func(t *testing.T) {
			source := &lightweightHistoryStub{messages: []messagepkg.Message{{
				ID: "checkpoint", Role: "assistant", CreatedAt: time.Unix(1, 0),
				Content:  []byte(`{"role":"assistant","content":[{"type":"reasoning","text":"unfinished thought"}]}`),
				Metadata: map[string]any{messagepkg.AgentStepInterruptedMetadataKey: true},
			}}}
			if completed {
				source.messages = append(source.messages, messagepkg.Message{
					ID: "answer", Role: "assistant", CreatedAt: time.Unix(2, 0),
					Content: []byte(`{"role":"assistant","content":"completed answer"}`),
				})
			}
			reader := discussHistoryReader{messages: source, maxBytes: 4000000, logger: slog.New(slog.DiscardHandler)}
			entries, measure, err := reader.Load(context.Background(), "session", messagepkg.HistoryOrigin{})
			if err != nil {
				t.Fatal(err)
			}
			if source.sessionID != "session" || source.maxBytes != 4000000 {
				t.Fatal("history request lost session/budget")
			}
			if measure.TotalMessages != 100 || measure.TotalBytes != 10000 || measure.Loaded != len(source.messages) {
				t.Fatalf("incorrect history measure: %+v", measure)
			}
			var text strings.Builder
			for _, entry := range entries {
				text.Write(entry.RawContent)
			}
			if strings.Contains(text.String(), "unfinished thought") == completed {
				t.Fatalf("reasoning checkpoint projection incorrect: %s", text.String())
			}
			if completed && !strings.Contains(text.String(), "completed answer") {
				t.Fatal("completed answer missing")
			}
		})
	}
}
