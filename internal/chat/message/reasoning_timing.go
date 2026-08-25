package message

import (
	"encoding/json"
	"time"
)

const (
	// ReasoningTimingMetadataKey stores server-observed reasoning segment timing
	// on the assistant row that owns the corresponding reasoning content.
	ReasoningTimingMetadataKey = "reasoning_timing"
	ReasoningTimingVersion     = 1
)

// ReasoningTimingMetadata is the versioned persistence envelope stored in a
// bot_history_messages metadata JSONB value. Keeping it outside model content
// prevents presentation-only timing from being replayed to providers.
type ReasoningTimingMetadata struct {
	Version  int                      `json:"version"`
	Segments []ReasoningTimingSegment `json:"segments"`
}

// ReasoningTimingSegment describes one visible reasoning block. DurationMS is
// computed from Go's monotonic clock; the wall-clock timestamps are retained
// only for audit and correlation.
type ReasoningTimingSegment struct {
	SegmentID     string    `json:"segment_id"`
	Ordinal       int       `json:"ordinal"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       time.Time `json:"ended_at"`
	DurationMS    int64     `json:"duration_ms"`
	State         string    `json:"state"`
	StartBoundary string    `json:"start_boundary"`
	EndBoundary   string    `json:"end_boundary"`
	Measurement   string    `json:"measurement"`
}

// ReasoningTimingFromMetadata decodes the versioned timing envelope from a
// message metadata map. JSON round-tripping keeps this tolerant of values
// loaded from JSONB as map[string]any as well as typed values used in tests.
func ReasoningTimingFromMetadata(metadata map[string]any) []ReasoningTimingSegment {
	if len(metadata) == 0 {
		return nil
	}
	value, ok := metadata[ReasoningTimingMetadataKey]
	if !ok || value == nil {
		return nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	var envelope ReasoningTimingMetadata
	if err := json.Unmarshal(data, &envelope); err != nil || envelope.Version != ReasoningTimingVersion {
		return nil
	}
	valid := make([]ReasoningTimingSegment, 0, len(envelope.Segments))
	for _, segment := range envelope.Segments {
		if segment.Ordinal < 0 || segment.DurationMS < 0 || segment.StartedAt.IsZero() || segment.EndedAt.IsZero() {
			continue
		}
		valid = append(valid, segment)
	}
	return valid
}
