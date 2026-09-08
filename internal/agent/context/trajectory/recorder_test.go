package trajectory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type memorySink struct {
	events   []Event
	contents map[string]string
	fail     bool
}

func (s *memorySink) Append(_ context.Context, event Event, contents []Content) error {
	if s.fail {
		return errors.New("unavailable")
	}
	if s.contents == nil {
		s.contents = make(map[string]string)
	}
	for _, content := range contents {
		s.contents[content.Hash] = string(content.Data)
	}
	s.events = append(s.events, event)
	return nil
}

func TestRecorderPreservesEveryVersionAndFullContent(t *testing.T) {
	sink := &memorySink{}
	recorder := NewRecorder(sink)
	recorder.Bind("run", "session")
	body := strings.Repeat("上下文🙂", 50000) + "LAST_FRAGMENT"
	recorder.Record(context.Background(), "trigger", nil, Block{Kind: "user", Label: "original", Content: body})
	recorder.Record(context.Background(), "selected", nil,
		Block{Kind: "user", Label: "selected", Content: body},
		Block{Kind: "hook", Label: "hook-a", Content: "same"},
		Block{Kind: "hook", Label: "hook-b", Content: "same"},
	)
	if len(sink.events) != 2 || sink.events[0].Sequence != 1 || sink.events[1].Sequence != 2 {
		t.Fatalf("events = %#v", sink.events)
	}
	for _, event := range sink.events {
		var restored strings.Builder
		for _, hash := range event.Blocks[0].Chunks {
			restored.WriteString(sink.contents[hash])
		}
		if restored.String() != body {
			t.Fatal("recorded input lost content")
		}
	}
	first, second := sink.events[0].Blocks[0], sink.events[1].Blocks[0]
	if first.Hash != second.Hash || first.Label != "original" || second.Label != "selected" {
		t.Fatal("content deduplication must preserve occurrence labels")
	}
	if sink.events[1].Blocks[1].Hash != sink.events[1].Blocks[2].Hash || sink.events[1].Blocks[2].Label != "hook-b" {
		t.Fatal("equal hook contents lost their distinct sources")
	}
	if stats := recorder.Stats(); stats.Events != 2 || stats.Errors != 0 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestRecorderReportsGapsWithoutReusingSequence(t *testing.T) {
	sink := &memorySink{fail: true}
	recorder := NewRecorder(sink)
	recorder.Bind("run", "session")
	recorder.Record(context.Background(), "trigger", nil, Block{Content: "original"})
	sink.fail = false
	recorder.Record(context.Background(), "provider_request", nil, Block{Content: "final"})
	if len(sink.events) != 1 || sink.events[0].Sequence != 2 || sink.events[0].CaptureErrors != 1 {
		t.Fatalf("missing capture was hidden: %#v", sink.events)
	}
	if stats := recorder.Stats(); stats.Events != 2 || stats.Errors != 1 {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestRecorderDoesNotChangeRunOwnership(t *testing.T) {
	sink := &memorySink{}
	recorder := NewRecorder(sink)
	recorder.Bind("run-a", "session-a")
	recorder.Bind("run-b", "session-b")
	recorder.Record(context.Background(), "trigger", nil)
	if len(sink.events) != 1 || sink.events[0].RunID != "run-a" || sink.events[0].SessionID != "session-a" {
		t.Fatal("recorder changed ownership")
	}
}
