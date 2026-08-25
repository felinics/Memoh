package message

import "encoding/json"

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
// observed by the server and intentionally excludes presentation-only details.
type ReasoningTimingSegment struct {
	Ordinal    int    `json:"ordinal"`
	DurationMS int64  `json:"duration_ms"`
	State      string `json:"state"`
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
		if segment.Ordinal < 0 || segment.DurationMS < 0 {
			continue
		}
		valid = append(valid, segment)
	}
	return valid
}
