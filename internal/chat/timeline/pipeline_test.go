package timeline

import (
	"reflect"
	"testing"
	"time"
)

func TestPipelineIncrementalRenderMatchesFullRender(t *testing.T) {
	t.Parallel()
	params := RenderParams{BotUserID: "bot"}
	pipeline := NewPipelineWithOptions(params, PipelineOptions{MaxSessions: -1, MaxResidentByte: -1, TTL: -1})
	events := []CanonicalEvent{
		MessageEvent{SessionID: "s", MessageID: "m1", ReceivedAtMs: 1, TimestampSec: 1, Sender: &CanonicalUser{ID: "u1", DisplayName: "Alice"}, Content: []ContentNode{{Type: "text", Text: "one"}}},
		MessageEvent{SessionID: "s", MessageID: "m2", ReceivedAtMs: 2, TimestampSec: 2, Sender: &CanonicalUser{ID: "u1", DisplayName: "Alicia"}, Content: []ContentNode{{Type: "text", Text: "two"}}, ReplyToMessageID: "m1"},
		EditEvent{SessionID: "s", MessageID: "m1", ReceivedAtMs: 3, TimestampSec: 3, Content: []ContentNode{{Type: "text", Text: "edited"}}},
		DeleteEvent{SessionID: "s", MessageIDs: []string{"m2"}, ReceivedAtMs: 4, TimestampSec: 4},
		ServiceEvent{SessionID: "s", Action: ServiceChatRenamed, ReceivedAtMs: 5, TimestampSec: 5, NewTitle: "room"},
		MessageEvent{SessionID: "s", MessageID: "m1", ReceivedAtMs: 6, TimestampSec: 6, IsSelfSent: true, MentionsMe: true},
	}

	oracle := NewEmptyIC("s")
	for _, event := range events {
		oracle = Reduce(oracle, event)
		got := pipeline.PushEvent("s", event)
		want := Render(oracle, params)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("incremental render diverged after %#v\ngot:  %#v\nwant: %#v", event, got, want)
		}
	}
}

func TestPipelineRenderedSnapshotsStayImmutable(t *testing.T) {
	t.Parallel()
	pipeline := NewPipelineWithOptions(RenderParams{}, PipelineOptions{MaxSessions: -1, MaxResidentByte: -1, TTL: -1})
	first := pipeline.PushEvent("s", MessageEvent{SessionID: "s", MessageID: "m1", ReceivedAtMs: 1, Content: []ContentNode{{Type: "text", Text: "before"}}})
	want := first[0].Content[0].Text
	pipeline.PushEvent("s", EditEvent{SessionID: "s", MessageID: "m1", ReceivedAtMs: 2, Content: []ContentNode{{Type: "text", Text: "after"}}})
	if first[0].Content[0].Text != want {
		t.Fatalf("previous snapshot mutated: got %q want %q", first[0].Content[0].Text, want)
	}
}

func TestPipelineEvictsByLRUAndTTL(t *testing.T) {
	t.Parallel()
	now := time.Unix(100, 0)
	pipeline := NewPipelineWithOptions(RenderParams{}, PipelineOptions{
		MaxSessions: 2, MaxResidentByte: -1, TTL: time.Minute, Now: func() time.Time { return now },
	})
	message := func(session string) MessageEvent {
		return MessageEvent{SessionID: session, MessageID: session, Content: []ContentNode{{Type: "text", Text: session}}}
	}
	pipeline.PushEvent("a", message("a"))
	pipeline.PushEvent("b", message("b"))
	pipeline.GetRC("a")
	pipeline.PushEvent("c", message("c"))
	if pipeline.HasSession("b") {
		t.Fatal("least-recently-used session was not evicted")
	}
	now = now.Add(2 * time.Minute)
	if pipeline.HasSession("a") || pipeline.HasSession("c") {
		t.Fatal("expired sessions were not evicted")
	}
	stats := pipeline.Stats()
	if stats.Evictions != 3 || stats.ResidentSessions != 0 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
}

func TestPipelineEvictsByResidentBytesAndDropAll(t *testing.T) {
	t.Parallel()
	pipeline := NewPipelineWithOptions(RenderParams{}, PipelineOptions{MaxSessions: -1, MaxResidentByte: 1, TTL: -1})
	pipeline.PushEvent("oversized", MessageEvent{SessionID: "oversized", MessageID: "m", Content: []ContentNode{{Type: "text", Text: "payload"}}})
	if pipeline.HasSession("oversized") {
		t.Fatal("resident-byte limit did not evict oversized session")
	}

	pipeline = NewPipelineWithOptions(RenderParams{}, PipelineOptions{MaxSessions: -1, MaxResidentByte: -1, TTL: -1})
	pipeline.PushEvent("a", MessageEvent{SessionID: "a", MessageID: "a"})
	pipeline.PushEvent("b", MessageEvent{SessionID: "b", MessageID: "b"})
	pipeline.DropAll()
	if got := pipeline.Stats(); got.ResidentSessions != 0 || got.Evictions != 2 {
		t.Fatalf("DropAll stats = %+v", got)
	}
}

func FuzzPipelineIncrementalRenderEquivalence(f *testing.F) {
	f.Add([]byte{0, 0, 1, 2, 3, 4, 1, 0})
	f.Fuzz(func(t *testing.T, operations []byte) {
		if len(operations) > 128 {
			operations = operations[:128]
		}
		pipeline := NewPipelineWithOptions(RenderParams{}, PipelineOptions{MaxSessions: -1, MaxResidentByte: -1, TTL: -1})
		oracle := NewEmptyIC("s")
		for i, operation := range operations {
			messageID := "m1"
			if operation&1 != 0 {
				messageID = "m2"
			}
			var event CanonicalEvent
			switch operation % 5 {
			case 0:
				event = MessageEvent{SessionID: "s", MessageID: messageID, ReceivedAtMs: int64(i), TimestampSec: int64(i), Content: []ContentNode{{Type: "text", Text: string(rune('a' + operation%26))}}}
			case 1:
				event = EditEvent{SessionID: "s", MessageID: messageID, ReceivedAtMs: int64(i), TimestampSec: int64(i), Content: []ContentNode{{Type: "text", Text: string(rune('A' + operation%26))}}}
			case 2:
				event = DeleteEvent{SessionID: "s", MessageIDs: []string{messageID}, ReceivedAtMs: int64(i), TimestampSec: int64(i)}
			case 3:
				event = ServiceEvent{SessionID: "s", Action: ServiceChatRenamed, ReceivedAtMs: int64(i), TimestampSec: int64(i), NewTitle: messageID}
			default:
				event = ServiceEvent{SessionID: "s", Action: ServiceMessagePinned, ReceivedAtMs: int64(i), TimestampSec: int64(i), PinnedMessageID: messageID}
			}
			oracle = Reduce(oracle, event)
			if got, want := pipeline.PushEvent("s", event), Render(oracle, RenderParams{}); !reflect.DeepEqual(got, want) {
				t.Fatalf("operation %d diverged", i)
			}
		}
	})
}
