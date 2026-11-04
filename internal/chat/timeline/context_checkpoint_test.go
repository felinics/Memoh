package timeline

import (
	"reflect"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
)

func textSegment(id string, atMs int64, text string) RenderedSegment {
	return RenderedSegment{
		MessageID:    id,
		ReceivedAtMs: atMs,
		Content:      []RenderedContentPiece{{Type: "text", Text: text}},
	}
}

func assistantTR(atMs int64, text string) TurnResponseEntry {
	return TurnResponseEntry{RequestedAtMs: atMs, Role: "assistant", Content: text}
}

func messageTexts(messages []ContextMessage) []string {
	texts := make([]string, 0, len(messages))
	for _, m := range messages {
		texts = append(texts, m.Role+":"+m.Content)
	}
	return texts
}

// A session without a checkpoint composes its whole history, inputs and
// responses by time, with nothing pinned.
func TestComposeContextWithoutCheckpointMergesTheWholeHistory(t *testing.T) {
	rc := RenderedContext{textSegment("m1", 1000, "hello"), textSegment("m2", 2000, "world")}
	trs := []TurnResponseEntry{assistantTR(1500, "reply")}

	composed := ComposeContextWithCheckpoint(rc, trs, HistoryCheckpoint{})
	if composed == nil {
		t.Fatal("expected a composed result")
	}
	if got, want := messageTexts(composed.Messages), []string{"user:hello", "assistant:reply", "user:world"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("composed %q, want %q", got, want)
	}
	for _, message := range composed.Messages {
		if message.Pinned {
			t.Fatalf("nothing is pinned without a checkpoint: %+v", message)
		}
	}
}

// The checkpoint opens the history: what it holds in its order, pinned (a summary, a kept input as its segment, a
// kept response), and only then what came after it, by time. A kept input
// renders once, in its place in the head.
func TestComposeContextWithCheckpointPinsWhatItHolds(t *testing.T) {
	kept := assistantTR(1500, "kept reply")
	kept.SourceMessageID = "h-kept"
	tail := assistantTR(4000, "tail reply")
	tail.SourceMessageID = "h-tail"
	held := HistoryCheckpoint{
		Position: messagepkg.HistoryPosition{Turn: 3, Seq: 2},
		Items: []CheckpointItem{
			{SummaryID: "s1", Summary: "  earlier work  "},
			{MessageID: "m-input"},
			{Response: &kept},
			{SummaryID: "s2", Summary: "later work"},
		},
		Messages: map[string]CheckpointMessage{"m-input": {Kept: true}, "m-new": {Kept: true}},
	}
	rc := RenderedContext{textSegment("m-input", 1000, "kept input"), textSegment("m-new", 3000, "new input")}

	composed := ComposeContextWithCheckpoint(rc, []TurnResponseEntry{tail}, held)
	if composed == nil {
		t.Fatal("expected a composed result")
	}
	want := []string{
		"user:" + turn.SummaryText("earlier work"),
		"user:kept input",
		"assistant:kept reply",
		"user:" + turn.SummaryText("later work"),
		"user:new input",
		"assistant:tail reply",
	}
	if got := messageTexts(composed.Messages); !reflect.DeepEqual(got, want) {
		t.Fatalf("composed %q, want %q", got, want)
	}
	for i, message := range composed.Messages {
		if message.Pinned != (i < 4) {
			t.Fatalf("message %d %q pinned = %v", i, message.Content, message.Pinned)
		}
	}
	if composed.Messages[0].CompactionArtifactID != "s1" || composed.Messages[3].CompactionArtifactID != "s2" {
		t.Fatalf("summaries lost their ids: %+v", composed.Messages)
	}
	if source := composed.Messages[1].Source; source == nil || source.Kind != "external" || source.ID != "m-input" || source.Current {
		t.Fatalf("kept input source = %+v, want the external message, not current", source)
	}
	if source := composed.Messages[2].Source; source == nil || source.Kind != "history" || source.ID != "h-kept" {
		t.Fatalf("kept response source = %+v, want its history row", source)
	}
	if source := composed.Messages[4].Source; source == nil || source.ID != "m-new" || !source.Current {
		t.Fatalf("newest input source = %+v, want it current", source)
	}
}

// What the checkpoint does not hold composes after it when the checkpoint
// leaves it raw: an input with context rows when one of them is after the
// checkpoint or kept, and one without context rows. Whatever its time, it
// renders after the head.
func TestComposeContextWithCheckpointComposesWhatItLeavesRaw(t *testing.T) {
	rc := RenderedContext{
		textSegment("m-chatter-early", 2000, "early chatter"),
		textSegment("m-folded", 2500, "folded input"),
		textSegment("m-chatter-late", 2600, "late chatter"),
		textSegment("m-raw", 3000, "raw input"),
	}
	held := HistoryCheckpoint{
		Items:    []CheckpointItem{{SummaryID: "s1", Summary: "folded"}},
		Messages: map[string]CheckpointMessage{"m-folded": {}, "m-raw": {Kept: true}},
	}
	composed := ComposeContextWithCheckpoint(rc, nil, held)
	want := []string{
		"user:" + turn.SummaryText("folded"),
		"user:early chatter",
		"user:late chatter",
		"user:raw input",
	}
	if composed == nil || !reflect.DeepEqual(messageTexts(composed.Messages), want) {
		t.Fatalf("composed %q, want %q", messageTexts(composed.Messages), want)
	}
}

// A summary stands for a message it folded as its rows recorded it: an edit
// after the fold leaves the message folded.
func TestComposeContextWithCheckpointKeepsAnEditedMessageFolded(t *testing.T) {
	edited := textSegment("m-edited", 2000, "edited after the fold")
	edited.EditedAtMs = 5000
	edited.LastEventCursor = 8
	rc := RenderedContext{edited, textSegment("m-new", 3000, "new input")}
	held := HistoryCheckpoint{
		Items:    []CheckpointItem{{SummaryID: "s1", Summary: "folded"}},
		Messages: map[string]CheckpointMessage{"m-edited": {}, "m-new": {Kept: true}},
	}
	composed := ComposeContextWithCheckpoint(rc, nil, held)
	want := []string{"user:" + turn.SummaryText("folded"), "user:new input"}
	if composed == nil || !reflect.DeepEqual(messageTexts(composed.Messages), want) {
		t.Fatalf("composed %q, want %q", messageTexts(composed.Messages), want)
	}
}

// A kept input the rendered context no longer has renders nothing; the rest
// of what the checkpoint holds keeps its order.
func TestComposeContextWithCheckpointSkipsAKeptInputItCannotRender(t *testing.T) {
	held := HistoryCheckpoint{
		Items: []CheckpointItem{
			{SummaryID: "s1", Summary: "first"},
			{MessageID: "m-gone"},
			{SummaryID: "s2", Summary: "second"},
		},
	}
	composed := ComposeContextWithCheckpoint(RenderedContext{textSegment("m-new", 3000, "new input")}, nil, held)
	want := []string{"user:" + turn.SummaryText("first"), "user:" + turn.SummaryText("second"), "user:new input"}
	if composed == nil || !reflect.DeepEqual(messageTexts(composed.Messages), want) {
		t.Fatalf("composed %q, want %q", messageTexts(composed.Messages), want)
	}
}

// Responses after the checkpoint keep the order of their time, an input
// first on the same millisecond, all after what the checkpoint holds.
func TestComposeContextWithCheckpointOrdersTheRestByTime(t *testing.T) {
	first := assistantTR(1000, "first reply")
	first.SourceMessageID = "h1"
	second := assistantTR(1000, "second reply")
	second.SourceMessageID = "h2"
	held := HistoryCheckpoint{Items: []CheckpointItem{{SummaryID: "s1", Summary: "folded"}}}

	composed := ComposeContextWithCheckpoint(RenderedContext{textSegment("m1", 1000, "input")}, []TurnResponseEntry{first, second}, held)
	want := []string{"user:" + turn.SummaryText("folded"), "user:input", "assistant:first reply", "assistant:second reply"}
	if composed == nil || !reflect.DeepEqual(messageTexts(composed.Messages), want) {
		t.Fatalf("composed %q, want %q", messageTexts(composed.Messages), want)
	}
}

func TestComposeContextWithCheckpointEmptyInputs(t *testing.T) {
	if composed := ComposeContextWithCheckpoint(nil, nil, HistoryCheckpoint{}); composed != nil {
		t.Fatalf("expected nil result for empty inputs, got %+v", composed)
	}
	// A summary alone is history.
	summarized := HistoryCheckpoint{Items: []CheckpointItem{{SummaryID: "s1", Summary: "folded"}}}
	composed := ComposeContextWithCheckpoint(nil, nil, summarized)
	want := []string{"user:" + turn.SummaryText("folded")}
	if composed == nil || !reflect.DeepEqual(messageTexts(composed.Messages), want) {
		t.Fatalf("composed %q, want %q", messageTexts(composed.Messages), want)
	}
}
