package timeline

import (
	"testing"
)

func TestAssignEventCursorStampsAllEventKinds(t *testing.T) {
	events := []CanonicalEvent{
		MessageEvent{SessionID: "s", MessageID: "m1", ReceivedAtMs: 1000},
		EditEvent{SessionID: "s", MessageID: "m1", ReceivedAtMs: 1100},
		DeleteEvent{SessionID: "s", MessageIDs: []string{"m1"}, ReceivedAtMs: 1200},
		ServiceEvent{SessionID: "s", Action: ServiceMemberLeft, ReceivedAtMs: 1300},
	}
	for _, event := range events {
		stamped, err := assignEventCursor(event, 5001)
		if err != nil {
			t.Fatalf("assign cursor to %T: %v", event, err)
		}
		if got := eventCursorOf(stamped); got != 5001 {
			t.Fatalf("%T cursor = %d, want 5001", stamped, got)
		}
	}
}

func eventCursorOf(event CanonicalEvent) int64 {
	switch typed := event.(type) {
	case MessageEvent:
		return typed.EventCursor
	case EditEvent:
		return typed.EventCursor
	case DeleteEvent:
		return typed.EventCursor
	case ServiceEvent:
		return typed.EventCursor
	default:
		return -1
	}
}

func TestAssignEventCursorRejectsInvalid(t *testing.T) {
	if _, err := assignEventCursor(nil, 1); err == nil {
		t.Fatal("expected error for nil event")
	}
	if _, err := assignEventCursor(MessageEvent{}, 0); err == nil {
		t.Fatal("expected error for non-positive cursor")
	}
	if _, err := assignEventCursor(MessageEvent{}, MaxJSONSafeEventCursor+1); err == nil {
		t.Fatal("expected error for cursor above the JSON-safe range")
	}
}

func TestPushEventThreadsCursorIntoSegments(t *testing.T) {
	pipeline := NewPipeline(RenderParams{})
	rc := pipeline.PushEvent("s1", MessageEvent{
		SessionID:    "s1",
		MessageID:    "m1",
		EventCursor:  7001,
		ReceivedAtMs: 1000,
		TimestampSec: 1,
		Content:      []ContentNode{{Type: "text", Text: "hello"}},
		Conversation: ConversationMeta{Channel: "telegram", ConversationType: "group"},
	})
	if len(rc) != 1 || rc[0].LastEventCursor != 7001 {
		t.Fatalf("expected segment cursor 7001, got %+v", rc)
	}

	rc = pipeline.PushEvent("s1", EditEvent{
		SessionID:    "s1",
		MessageID:    "m1",
		EventCursor:  7002,
		ReceivedAtMs: 2000,
		TimestampSec: 2,
		Content:      []ContentNode{{Type: "text", Text: "hello edited"}},
	})
	if len(rc) != 1 || rc[0].LastEventCursor != 7002 {
		t.Fatalf("expected edit to bump segment cursor to 7002, got %+v", rc)
	}
}

func TestDiscussCursorPositionCovers(t *testing.T) {
	position := DiscussCursorPosition{EventCursor: 7001, SourceCursor: 1500}

	withCursor := textSegment("m1", 1400, "cursor domain")
	withCursor.LastEventCursor = 7001
	if !position.Covers(withCursor) {
		t.Fatal("segment consumed in both domains must be covered")
	}
	fresh := textSegment("m2", 1400, "newer cursor")
	fresh.LastEventCursor = 7002
	if position.Covers(fresh) {
		t.Fatal("segment past the consumed event cursor must not be covered")
	}
	legacy := textSegment("m0", 1400, "legacy")
	if !position.Covers(legacy) {
		t.Fatal("cursor-less segment inside the source window must be covered")
	}
	degraded := textSegment("m3", 1600, "degraded ingest")
	if position.Covers(degraded) {
		t.Fatal("cursor-less segment past the source window must not be covered")
	}
}

func TestHasUncoveredExternalEvent(t *testing.T) {
	position := DiscussCursorPosition{EventCursor: 7001, SourceCursor: 1500}
	covered := textSegment("m1", 1000, "covered")
	covered.LastEventCursor = 7001
	selfSent := textSegment("m2", 2000, "bot echo")
	selfSent.LastEventCursor = 7002
	selfSent.IsSelfSent = true
	myself := textSegment("m3", 3000, "bot own")
	myself.LastEventCursor = 7003
	myself.IsMyself = true
	rc := RenderedContext{covered, selfSent, myself}

	if HasUncoveredExternalEvent(rc, position) {
		t.Fatal("covered and self segments must not report new external activity")
	}
	fresh := textSegment("m4", 4000, "new")
	fresh.LastEventCursor = 7004
	if !HasUncoveredExternalEvent(append(rc, fresh), position) {
		t.Fatal("uncovered external segment must report new activity")
	}
	degraded := textSegment("m5", 1600, "no cursor")
	if !HasUncoveredExternalEvent(append(rc, degraded), position) {
		t.Fatal("cursor-less segment past the source window must report new activity")
	}
}

func TestDiscussCursorPositionMerge(t *testing.T) {
	merged := DiscussCursorPosition{EventCursor: 7001, SourceCursor: 900}.Merge(
		DiscussCursorPosition{EventCursor: 6000, SourceCursor: 1500})
	if merged.EventCursor != 7001 || merged.SourceCursor != 1500 {
		t.Fatalf("merged = %+v, want component-wise max", merged)
	}
}

func TestConsumedDiscussCursorTakesLatestGate(t *testing.T) {
	legacy := textSegment("m0", 900, "legacy")
	current := textSegment("m1", 1000, "current")
	current.LastEventCursor = 7001
	rc := RenderedContext{legacy, current}

	position := ConsumedDiscussCursor(rc)
	if position.EventCursor != 7001 || position.SourceCursor != 1000 {
		t.Fatalf("position = %+v, want cursor 7001 source 1000", position)
	}
}

func TestPushEventClampsOutOfRangeCursorOnEveryEventKind(t *testing.T) {
	poisoned := MaxTrustedEventCursor + 1
	pipeline := NewPipeline(RenderParams{})
	pipeline.PushEvent("s1", MessageEvent{
		SessionID: "s1", MessageID: "m1", EventCursor: 7001,
		ReceivedAtMs: 1000, TimestampSec: 1,
		Content: []ContentNode{{Type: "text", Text: "hello"}},
	})

	rc := pipeline.PushEvent("s1", EditEvent{
		SessionID: "s1", MessageID: "m1", EventCursor: poisoned,
		ReceivedAtMs: 2000, TimestampSec: 2,
		Content: []ContentNode{{Type: "text", Text: "edited"}},
	})
	if len(rc) != 1 || rc[0].LastEventCursor != 7001 {
		t.Fatalf("poisoned edit cursor must not bump the segment, got %+v", rc)
	}

	rc = pipeline.PushEvent("s1", DeleteEvent{
		SessionID: "s1", MessageIDs: []string{"m1"}, EventCursor: poisoned,
		ReceivedAtMs: 3000, TimestampSec: 3,
	})
	if len(rc) != 1 || rc[0].LastEventCursor != 7001 {
		t.Fatalf("poisoned delete cursor must not bump the segment, got %+v", rc)
	}

	rc = pipeline.PushEvent("s1", ServiceEvent{
		SessionID: "s1", Action: ServiceMemberLeft, EventCursor: poisoned,
		ReceivedAtMs: 4000, TimestampSec: 4,
		Member: &CanonicalUser{ID: "u1", DisplayName: "Someone"},
	})
	if len(rc) != 2 || rc[1].LastEventCursor != 0 {
		t.Fatalf("poisoned service cursor must be dropped, got %+v", rc)
	}
}

func TestPushEventClampsOutOfRangeCursor(t *testing.T) {
	for _, poisoned := range []int64{MaxJSONSafeEventCursor + 1, MaxJSONSafeEventCursor, MaxTrustedEventCursor + 1} {
		pipeline := NewPipeline(RenderParams{})
		rc := pipeline.PushEvent("s1", MessageEvent{
			SessionID:    "s1",
			MessageID:    "m1",
			EventCursor:  poisoned,
			ReceivedAtMs: 1000,
			TimestampSec: 1,
			Content:      []ContentNode{{Type: "text", Text: "poisoned"}},
		})
		if len(rc) != 1 || rc[0].LastEventCursor != 0 {
			t.Fatalf("untrusted payload cursor %d must be dropped, got %+v", poisoned, rc)
		}
	}
}

func TestCoversRequiresBothDomains(t *testing.T) {
	// Cursor allocation order can invert against source order when concurrent
	// inbound workers stamp events for one thread, so a lower cursor alone
	// must not prove consumption.
	position := DiscussCursorPosition{EventCursor: 101, SourceCursor: 1001}
	inverted := textSegment("m2", 1002, "allocated first, delivered late")
	inverted.LastEventCursor = 100

	if position.Covers(inverted) {
		t.Fatal("a segment newer in source time must not be covered by a higher cursor watermark")
	}
	if !HasUncoveredExternalEvent(RenderedContext{inverted}, position) {
		t.Fatal("the inverted segment must still report new external activity")
	}
}

func TestCoversFallsBackToSourceWhenWatermarkHasNoCursor(t *testing.T) {
	// Cold start seeds the watermark from persisted replies, which only carry
	// source timestamps; cursor-bearing segments must still be covered.
	position := DiscussCursorPosition{SourceCursor: 3000}
	answered := textSegment("m1", 1000, "already answered")
	answered.LastEventCursor = 5

	if !position.Covers(answered) {
		t.Fatal("a cursor-bearing segment inside the answered source window must be covered")
	}
	if HasUncoveredExternalEvent(RenderedContext{answered}, position) {
		t.Fatal("cold-start anchor must suppress already-answered context")
	}
}

func TestCoversStillTriggersOnEditedSegments(t *testing.T) {
	// The cursor domain exists so an edit to an old message re-triggers even
	// though its source timestamp stays inside the consumed window.
	position := DiscussCursorPosition{EventCursor: 102, SourceCursor: 1002}
	edited := textSegment("m1", 1000, "edited after consumption")
	edited.LastEventCursor = 104

	if position.Covers(edited) {
		t.Fatal("a segment whose latest event postdates the watermark must not be covered")
	}
}

// TestConsumedPositionCoversEverythingItConsumed pins the anti-loop invariant:
// whatever a turn consumed must read as covered next round, or the driver would
// re-trigger forever on its own context.
func TestConsumedPositionCoversEverythingItConsumed(t *testing.T) {
	cases := map[string]RenderedContext{
		"cursor stamped": {
			withCursor(textSegment("m1", 1000, "a"), 7001),
			withCursor(textSegment("m2", 2000, "b"), 7002),
		},
		"cursor-less legacy": {
			textSegment("m1", 1000, "a"),
			textSegment("m2", 2000, "b"),
		},
		"mixed ingest": {
			withCursor(textSegment("m1", 1000, "a"), 7001),
			textSegment("m2", 2000, "b"),
			withCursor(textSegment("m3", 3000, "c"), 7003),
		},
		"cursor order inverted against source order": {
			withCursor(textSegment("m1", 2000, "late source, early cursor"), 7001),
			withCursor(textSegment("m2", 1000, "early source, late cursor"), 7002),
		},
		"same millisecond": {
			withCursor(textSegment("m1", 1000, "a"), 7001),
			withCursor(textSegment("m2", 1000, "b"), 7002),
		},
	}

	for name, rc := range cases {
		position := ConsumedDiscussCursor(rc)
		for i, seg := range rc {
			if !position.Covers(seg) {
				t.Fatalf("%s: segment %d (%+v) not covered by its own consumed position %+v", name, i, seg, position)
			}
		}
		if HasUncoveredExternalEvent(rc, position) {
			t.Fatalf("%s: consumed context must not report new external activity", name)
		}
	}
}

func withCursor(seg RenderedSegment, cursor int64) RenderedSegment {
	seg.LastEventCursor = cursor
	return seg
}
