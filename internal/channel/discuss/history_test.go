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
	sessionID    string
	maxBytes     int64
	measureAfter []messagepkg.HistoryPosition
	after        []messagepkg.HistoryPosition
}

func (s *lightweightHistoryStub) MeasureHistory(_ context.Context, _ string, after messagepkg.HistoryPosition) (messagepkg.ActiveMessagesMeasure, error) {
	s.measureAfter = append(s.measureAfter, after)
	return messagepkg.ActiveMessagesMeasure{MessageCount: 100, ContentBytes: 10000}, nil
}

func (s *lightweightHistoryStub) ListTurnResponseSourcesWithinBytes(_ context.Context, sessionID string, after messagepkg.HistoryPosition, maxBytes int64) (messagepkg.HistoryLoad, error) {
	s.sessionID = sessionID
	s.after = append(s.after, after)
	s.maxBytes = maxBytes
	return messagepkg.HistoryLoad{Messages: s.messages}, nil
}

// A session's turn responses reach Discuss from after its history checkpoint
// on; a session without one is read from the start.
func TestLightweightHistoryLoadsAfterTheCheckpoint(t *testing.T) {
	for name, after := range map[string]messagepkg.HistoryPosition{
		"checkpoint":    {Turn: 12, Seq: 3},
		"no checkpoint": {},
	} {
		t.Run(name, func(t *testing.T) {
			source := &lightweightHistoryStub{}
			reader := discussHistoryReader{messages: source, maxBytes: 4000, logger: slog.New(slog.DiscardHandler)}
			if _, _, err := reader.Load(context.Background(), "session", after); err != nil {
				t.Fatal(err)
			}
			if len(source.after) != 1 || source.after[0] != after || len(source.measureAfter) != 1 || source.measureAfter[0] != after {
				t.Fatalf("history read after %v, measured after %v; want %+v", source.after, source.measureAfter, after)
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
			entries, measure, err := reader.Load(context.Background(), "session", messagepkg.HistoryPosition{})
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
