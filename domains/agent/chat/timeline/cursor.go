package timeline

import (
	"errors"
	"fmt"
)

// MaxJSONSafeEventCursor bounds legitimate event cursors; the allocation
// sequence never exceeds it, so larger payload values are corrupt data.
const MaxJSONSafeEventCursor int64 = 1<<53 - 1

// MaxTrustedEventCursor bounds cursors read back from payloads or restores.
// Clock-seeded allocation stays three orders of magnitude below it, while
// accepting values near the sequence MAXVALUE would poison consumed
// watermarks or exhaust the global sequence.
const MaxTrustedEventCursor = MaxJSONSafeEventCursor / 2

// DiscussCursorPosition tracks consumed discuss progress in both the durable
// event-cursor domain and the legacy source-timestamp domain. Segments are
// compared inside their own domain, so cursor magnitudes never race the wall
// clock and cursor-less ingest degrades to source-time coverage.
type DiscussCursorPosition struct {
	EventCursor  int64
	SourceCursor int64
}

// Covers reports whether the position already consumed the segment. Coverage
// must hold in every domain that carries information: cursor allocation order
// can invert against source order when concurrent workers stamp one thread, so
// a covered cursor alone never proves consumption, and a watermark seeded from
// persisted replies alone carries no cursor to compare against. Anything the
// watermark cannot prove consumed is treated as new.
func (p DiscussCursorPosition) Covers(seg RenderedSegment) bool {
	if seg.ReceivedAtMs > p.SourceCursor {
		return false
	}
	if seg.LastEventCursor > 0 && p.EventCursor > 0 {
		return seg.LastEventCursor <= p.EventCursor
	}
	return true
}

// Merge returns the component-wise maximum of both positions.
func (p DiscussCursorPosition) Merge(other DiscussCursorPosition) DiscussCursorPosition {
	if other.EventCursor > p.EventCursor {
		p.EventCursor = other.EventCursor
	}
	if other.SourceCursor > p.SourceCursor {
		p.SourceCursor = other.SourceCursor
	}
	return p
}

func assignEventCursor(event CanonicalEvent, cursor int64) (CanonicalEvent, error) {
	if event == nil {
		return nil, errors.New("canonical event is nil")
	}
	if cursor <= 0 || cursor > MaxJSONSafeEventCursor {
		return nil, fmt.Errorf("event cursor %d is outside the JSON-safe range", cursor)
	}
	switch typed := event.(type) {
	case MessageEvent:
		typed.EventCursor = cursor
		return typed, nil
	case EditEvent:
		typed.EventCursor = cursor
		return typed, nil
	case DeleteEvent:
		typed.EventCursor = cursor
		return typed, nil
	case ServiceEvent:
		typed.EventCursor = cursor
		return typed, nil
	default:
		return nil, fmt.Errorf("unsupported canonical event type %T", event)
	}
}

func sanitizedEventCursor(cursor int64) int64 {
	if cursor <= 0 || cursor > MaxTrustedEventCursor {
		return 0
	}
	return cursor
}

// HasUncoveredExternalEvent reports whether any non-self segment lies past the
// consumed position.
func HasUncoveredExternalEvent(rc RenderedContext, position DiscussCursorPosition) bool {
	for _, seg := range rc {
		if seg.IsMyself || seg.IsSelfSent {
			continue
		}
		if !position.Covers(seg) {
			return true
		}
	}
	return false
}

// ConsumedDiscussCursor is the position a discuss turn consumes when it
// replies to the given rendered timeline.
func ConsumedDiscussCursor(rc RenderedContext) DiscussCursorPosition {
	position := DiscussCursorPosition{}
	for _, seg := range rc {
		if seg.LastEventCursor > position.EventCursor {
			position.EventCursor = seg.LastEventCursor
		}
		if seg.ReceivedAtMs > position.SourceCursor {
			position.SourceCursor = seg.ReceivedAtMs
		}
	}
	return position
}
