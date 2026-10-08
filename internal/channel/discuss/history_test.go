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
	messages  []messagepkg.Message
	sessionID string
	maxBytes  int64
}

func (*lightweightHistoryStub) MeasureActiveBySession(context.Context, string, time.Time) (messagepkg.ActiveMessagesMeasure, error) {
	return messagepkg.ActiveMessagesMeasure{MessageCount: 100, ContentBytes: 10000}, nil
}

func (s *lightweightHistoryStub) ListTurnResponseSourcesSinceBySessionWithinBytes(_ context.Context, sessionID string, _ time.Time, maxBytes int64) ([]messagepkg.Message, error) {
	s.sessionID = sessionID
	s.maxBytes = maxBytes
	return s.messages, nil
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
			entries, measure := reader.Load(context.Background(), "session")
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
